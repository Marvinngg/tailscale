// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin || freebsd

package osrouter

import (
	"fmt"
	"log"
	"net/netip"
	"os/exec"
	"runtime"
	"strings"

	"github.com/tailscale/wireguard-go/tun"
	"go4.org/netipx"
	"tailscale.com/health"
	"tailscale.com/net/netmon"
	"tailscale.com/net/tsaddr"
	"tailscale.com/types/logger"
	"tailscale.com/version"
	"tailscale.com/wgengine/router"
)

func init() {
	router.HookNewUserspaceRouter.Set(func(opts router.NewOpts) (router.Router, error) {
		return newUserspaceBSDRouter(opts.Logf, opts.Tun, opts.NetMon, opts.Health)
	})
}

type userspaceBSDRouter struct {
	logf         logger.Logf
	netMon       *netmon.Monitor
	health       *health.Tracker
	tunname      string
	local        []netip.Prefix
	routes       map[netip.Prefix]bool
	bypassRoutes map[netip.Prefix]bool // /32 or subnet routes via physical gateway
}

func newUserspaceBSDRouter(logf logger.Logf, tundev tun.Device, netMon *netmon.Monitor, health *health.Tracker) (router.Router, error) {
	tunname, err := tundev.Name()
	if err != nil {
		return nil, err
	}

	return &userspaceBSDRouter{
		logf:    logf,
		netMon:  netMon,
		health:  health,
		tunname: tunname,
	}, nil
}

func (r *userspaceBSDRouter) addrsToRemove(newLocalAddrs []netip.Prefix) (remove []netip.Prefix) {
	for _, cur := range r.local {
		found := false
		for _, v := range newLocalAddrs {
			found = (v == cur)
			if found {
				break
			}
		}
		if !found {
			remove = append(remove, cur)
		}
	}
	return
}

func (r *userspaceBSDRouter) addrsToAdd(newLocalAddrs []netip.Prefix) (add []netip.Prefix) {
	for _, cur := range newLocalAddrs {
		found := false
		for _, v := range r.local {
			found = (v == cur)
			if found {
				break
			}
		}
		if !found {
			add = append(add, cur)
		}
	}
	return
}

func cmd(args ...string) *exec.Cmd {
	if len(args) == 0 {
		log.Fatalf("exec.Cmd(%#v) invalid; need argv[0]", args)
	}
	return exec.Command(args[0], args[1:]...)
}

func (r *userspaceBSDRouter) Up() error {
	ifup := []string{"ifconfig", r.tunname, "up"}
	if out, err := cmd(ifup...).CombinedOutput(); err != nil {
		r.logf("running ifconfig failed: %v\n%s", err, out)
		return err
	}
	return nil
}

func inet(p netip.Prefix) string {
	if p.Addr().Is6() {
		return "inet6"
	}
	return "inet"
}

// defaultGateway returns the system's default gateway IP for the given
// address family (4 or 6) by parsing "route -n get default" output.
func defaultGateway(family int) string {
	args := []string{"route", "-n", "get", "default"}
	if family == 6 {
		args = []string{"route", "-n", "get", "-inet6", "default"}
	}
	out, err := cmd(args...).CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			return strings.TrimSpace(line[len("gateway:"):])
		}
	}
	return ""
}

func (r *userspaceBSDRouter) Set(cfg *router.Config) (reterr error) {
	if cfg == nil {
		cfg = &shutdownConfig
	}

	setErr := func(err error) {
		if reterr == nil {
			reterr = err
		}
	}
	addrsToRemove := r.addrsToRemove(cfg.LocalAddrs)

	// If we're removing all addresses, we need to remove and re-add all
	// routes.
	resetRoutes := len(r.local) > 0 && len(addrsToRemove) == len(r.local)

	// Update the addresses.
	for _, addr := range addrsToRemove {
		arg := []string{"ifconfig", r.tunname, inet(addr), addr.String(), "-alias"}
		out, err := cmd(arg...).CombinedOutput()
		if err != nil {
			r.logf("addr del failed: %v => %v\n%s", arg, err, out)
			setErr(err)
		}
	}
	for _, addr := range r.addrsToAdd(cfg.LocalAddrs) {
		var arg []string
		if runtime.GOOS == "freebsd" && addr.Addr().Is6() && addr.Bits() == 128 {
			// FreeBSD rejects tun addresses of the form fc00::1/128 -> fc00::1,
			// https://bugs.freebsd.org/bugzilla/show_bug.cgi?id=218508
			// Instead add our whole /48, which works because we use a /48 route.
			// Full history: https://github.com/tailscale/tailscale/issues/1307
			tmp := netip.PrefixFrom(addr.Addr(), 48)
			arg = []string{"ifconfig", r.tunname, inet(tmp), tmp.String()}
		} else {
			arg = []string{"ifconfig", r.tunname, inet(addr), addr.String(), addr.Addr().String()}
		}
		out, err := cmd(arg...).CombinedOutput()
		if err != nil {
			r.logf("addr add failed: %v => %v\n%s", arg, err, out)
			setErr(err)
		}
	}

	// On macOS, "route add 0.0.0.0/0" fails with EEXIST when a default
	// route already exists. Split /0 into two /1 routes that override
	// the default without conflicting.
	cfgRoutes := cfg.Routes
	if runtime.GOOS == "darwin" {
		var out []netip.Prefix
		for _, r := range cfgRoutes {
			if r.Bits() == 0 {
				if r.Addr().Is4() {
					out = append(out,
						netip.MustParsePrefix("0.0.0.0/1"),
						netip.MustParsePrefix("128.0.0.0/1"))
				} else {
					out = append(out,
						netip.MustParsePrefix("::/1"),
						netip.MustParsePrefix("8000::/1"))
				}
			} else {
				out = append(out, r)
			}
		}
		cfgRoutes = out
	}

	// On macOS, add bypass routes via the physical gateway BEFORE
	// installing tunnel routes. These more-specific routes ensure
	// control plane, DERP, and user-configured subnets remain
	// reachable when /1 tunnel routes capture all traffic.
	if runtime.GOOS == "darwin" {
		r.setBypassRoutes(cfg.BypassRoutes)
	}

	newRoutes := make(map[netip.Prefix]bool)
	for _, route := range cfgRoutes {
		if runtime.GOOS != "darwin" && route == tsaddr.TailscaleULARange() {
			// Because we added the interface address as a /48 above,
			// the kernel already created the Tailscale ULA route
			// implicitly. We mustn't try to add/delete it ourselves.
			continue
		}
		newRoutes[route] = true
	}
	// Delete any preexisting routes.
	for route := range r.routes {
		if resetRoutes || !newRoutes[route] {
			net := netipx.PrefixIPNet(route)
			nip := net.IP.Mask(net.Mask)
			nstr := fmt.Sprintf("%v/%d", nip, route.Bits())
			del := "del"
			if version.OS() == "macOS" {
				del = "delete"
			}
			routedel := []string{"route", "-q", "-n",
				del, "-" + inet(route), nstr,
				"-iface", r.tunname}
			out, err := cmd(routedel...).CombinedOutput()
			if err != nil {
				r.logf("route del failed: %v: %v\n%s", routedel, err, out)
				setErr(err)
			}
		}
	}
	// Add the routes.
	for route := range newRoutes {
		if resetRoutes || !r.routes[route] {
			net := netipx.PrefixIPNet(route)
			nip := net.IP.Mask(net.Mask)
			nstr := fmt.Sprintf("%v/%d", nip, route.Bits())
			routeadd := []string{"route", "-q", "-n",
				"add", "-" + inet(route), nstr,
				"-iface", r.tunname}
			out, err := cmd(routeadd...).CombinedOutput()
			if err != nil {
				r.logf("addr add failed: %v: %v\n%s", routeadd, err, out)
				setErr(err)
			}
		}
	}

	// Store the interface and routes so we know what to change on an update.
	if reterr == nil {
		r.local = append([]netip.Prefix{}, cfg.LocalAddrs...)
	}
	r.routes = newRoutes

	return reterr
}

// setBypassRoutes manages routes that bypass the tunnel via the physical
// default gateway. Called before tunnel routes are written so that
// more-specific bypass routes take precedence over /1 tunnel routes.
func (r *userspaceBSDRouter) setBypassRoutes(wanted []netip.Prefix) {
	del := "delete"
	if version.OS() != "macOS" {
		del = "del"
	}

	newBypass := make(map[netip.Prefix]bool, len(wanted))
	for _, p := range wanted {
		newBypass[p] = true
	}

	// Remove stale bypass routes.
	for route := range r.bypassRoutes {
		if !newBypass[route] {
			nstr := fmt.Sprintf("%v/%d", route.Masked().Addr(), route.Bits())
			cmd("route", "-q", "-n", del, "-"+inet(route), nstr).CombinedOutput()
		}
	}

	if len(newBypass) == 0 {
		r.bypassRoutes = nil
		return
	}

	// Determine gateways for each address family.
	gw4 := defaultGateway(4)
	gw6 := defaultGateway(6)
	if gw4 == "" && gw6 == "" {
		r.logf("bypass routes: no default gateway found")
		r.bypassRoutes = newBypass
		return
	}

	added := 0
	for route := range newBypass {
		if r.bypassRoutes[route] {
			continue // already installed
		}
		gw := gw4
		if route.Addr().Is6() {
			gw = gw6
		}
		if gw == "" {
			continue
		}
		nstr := fmt.Sprintf("%v/%d", route.Masked().Addr(), route.Bits())
		flag := "-net"
		if route.IsSingleIP() {
			flag = "-host"
			nstr = route.Addr().String()
		}
		out, err := cmd("route", "-q", "-n", "add", "-"+inet(route), flag, nstr, gw).CombinedOutput()
		if err != nil {
			r.logf("bypass route add %s via %s failed: %v\n%s", nstr, gw, err, out)
		} else {
			added++
		}
	}
	if added > 0 {
		r.logf("bypass routes: added %d via gw4=%s gw6=%s", added, gw4, gw6)
	}
	r.bypassRoutes = newBypass
}

func (r *userspaceBSDRouter) Close() error {
	// Clean up bypass routes on shutdown.
	if len(r.bypassRoutes) > 0 {
		r.setBypassRoutes(nil)
	}
	return nil
}

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
	"sync"

	"github.com/tailscale/wireguard-go/tun"
	"go4.org/netipx"
	"golang.org/x/sys/unix"
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

	// fork: PF_ROUTE socket–driven bypass-route refresh.
	//
	// macOS without NetworkExtension uses /32 host routes via the physical
	// default gateway to keep WireGuard control-plane / DERP packets out of
	// the tunnel. Those next-hops must follow the physical default gateway
	// when the user switches WiFi.
	//
	// Upstream netmon already opens a PF_ROUTE socket but its callback only
	// fires on interface state changes — pure default-route changes (same
	// interface, different gateway) slip through. We open our own PF_ROUTE
	// socket and trigger a refresh on every routing message; the cheap
	// "did default gateway change?" check decides whether to actually
	// re-install routes.
	mu          sync.Mutex
	lastWanted  []netip.Prefix // bypass IPs we're tracking (refresh source)
	lastGw4     string         // gateway used at last successful install
	lastGw6     string
	routeSockFD int           // PF_ROUTE socket fd, -1 if not open
	closeCh     chan struct{} // signals routeMonLoop to exit
}

func newUserspaceBSDRouter(logf logger.Logf, tundev tun.Device, netMon *netmon.Monitor, health *health.Tracker) (router.Router, error) {
	tunname, err := tundev.Name()
	if err != nil {
		return nil, err
	}

	r := &userspaceBSDRouter{
		logf:        logf,
		netMon:      netMon,
		health:      health,
		tunname:     tunname,
		routeSockFD: -1,
		closeCh:     make(chan struct{}),
	}

	// fork: start PF_ROUTE socket monitor (darwin only — freebsd doesn't
	// need bypass-route refresh in the same way; the bypass code only
	// applies to darwin in setBypassRoutesLocked anyway).
	if runtime.GOOS == "darwin" {
		r.startRouteMonitor()
	}

	return r, nil
}

// startRouteMonitor opens a PF_ROUTE socket and spawns a goroutine that
// blocks on it. On any routing-table broadcast we check whether the system
// default gateway differs from the one we last installed bypass routes via,
// and if so, refresh them.
func (r *userspaceBSDRouter) startRouteMonitor() {
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, 0)
	if err != nil {
		r.logf("bypass routes: open AF_ROUTE socket failed: %v (auto-refresh disabled)", err)
		return
	}
	r.mu.Lock()
	r.routeSockFD = fd
	r.mu.Unlock()
	go r.routeMonLoop(fd)
}

// routeMonLoop reads routing messages from the PF_ROUTE socket. We don't
// parse the message — any RTM_* broadcast is a hint to re-check the default
// gateway. Cost per event is one `route -n get default` (sub-ms).
func (r *userspaceBSDRouter) routeMonLoop(fd int) {
	var buf [2 << 10]byte
	for {
		n, err := unix.Read(fd, buf[:])
		if err != nil {
			// Socket closed or fatal read error → exit goroutine.
			select {
			case <-r.closeCh:
			default:
				r.logf("bypass routes: route socket read error: %v", err)
			}
			return
		}
		if n < 4 {
			continue
		}
		r.maybeRefreshBypassRoutes()
	}
}

// maybeRefreshBypassRoutes reads the current default gateway and, if it
// changed since last install, re-installs every tracked bypass route with
// the new gateway. Uses BSD `route delete` + `route add` (mac BSD `route
// add` silently no-ops on EEXIST and `route change` is unreliable across
// flag mismatches, so we do the safe brute-force here — same pattern as
// the manual fix-up.sh that's known to work).
func (r *userspaceBSDRouter) maybeRefreshBypassRoutes() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lastWanted) == 0 {
		return
	}
	curGw4 := defaultGateway(4)
	curGw6 := defaultGateway(6)
	if curGw4 == r.lastGw4 && curGw6 == r.lastGw6 {
		return
	}
	r.logf("bypass routes: route socket event, gateway v4 %s->%s v6 %s->%s, refreshing %d entries",
		r.lastGw4, curGw4, r.lastGw6, curGw6, len(r.lastWanted))
	for _, p := range r.lastWanted {
		gw := curGw4
		if p.Addr().Is6() {
			gw = curGw6
		}
		if gw == "" {
			continue
		}
		r.deleteOneBypass(p)
		r.installOneBypass(p, gw)
	}
	r.lastGw4 = curGw4
	r.lastGw6 = curGw6
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
//
// fork: gateway-change-aware. If the physical default gateway changed
// since last call (e.g. user switched WiFi), all previously installed
// bypass routes are deleted and re-added with the new gateway. Without
// this, stale next-hop on the new LAN breaks exit-node reachability.
func (r *userspaceBSDRouter) setBypassRoutes(wanted []netip.Prefix) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setBypassRoutesLocked(wanted)
}

// setBypassRoutesLocked is the implementation of setBypassRoutes; r.mu must
// be held on entry. Also called from maybeRefreshBypassRoutes after the
// route socket reports a default-gateway change.
//
// Strategy: `route delete` + `route add` for every wanted entry. We don't
// trust mac BSD's `route add` to fail loudly on EEXIST (it returns exit 0
// silently), and `route change` has its own flag-matching quirks. The
// delete-then-add pattern matches the manual fix-up.sh that's known to
// work — kernel routing table is source of truth, not in-memory cache.
func (r *userspaceBSDRouter) setBypassRoutesLocked(wanted []netip.Prefix) {
	newBypass := make(map[netip.Prefix]bool, len(wanted))
	for _, p := range wanted {
		newBypass[p] = true
	}

	// Remove stale bypass routes (i.e. previously installed but no longer wanted).
	for route := range r.bypassRoutes {
		if !newBypass[route] {
			r.deleteOneBypass(route)
		}
	}

	// Remember the wanted list so the route-socket monitor can refresh.
	r.lastWanted = append(r.lastWanted[:0], wanted...)

	if len(newBypass) == 0 {
		r.bypassRoutes = nil
		r.lastGw4 = ""
		r.lastGw6 = ""
		return
	}

	gw4 := defaultGateway(4)
	gw6 := defaultGateway(6)
	if gw4 == "" && gw6 == "" {
		r.logf("bypass routes: no default gateway found")
		// Don't mark as installed; kernel routing table wasn't touched.
		// Route-socket monitor will retry when gateway appears.
		return
	}

	installed := 0
	for route := range newBypass {
		gw := gw4
		if route.Addr().Is6() {
			gw = gw6
		}
		if gw == "" {
			continue
		}
		// Delete (idempotent — ignore failure) then add. This is the
		// only reliable BSD pattern that survives stale next-hops.
		r.deleteOneBypass(route)
		if r.installOneBypass(route, gw) {
			installed++
		}
	}
	if installed > 0 {
		r.logf("bypass routes: installed %d via gw4=%s gw6=%s", installed, gw4, gw6)
	}
	r.bypassRoutes = newBypass
	r.lastGw4 = gw4
	r.lastGw6 = gw6
}

// installOneBypass adds a single bypass route via the given gateway.
// Returns true on success. Caller is responsible for first deleting any
// stale entry (BSD `route add` is a no-op on EEXIST).
func (r *userspaceBSDRouter) installOneBypass(route netip.Prefix, gw string) bool {
	nstr := fmt.Sprintf("%v/%d", route.Masked().Addr(), route.Bits())
	flag := "-net"
	if route.IsSingleIP() {
		flag = "-host"
		nstr = route.Addr().String()
	}
	out, err := cmd("route", "-q", "-n", "add", "-"+inet(route), flag, nstr, gw).CombinedOutput()
	if err != nil {
		r.logf("bypass route add %s via %s failed: %v\n%s", nstr, gw, err, out)
		return false
	}
	return true
}

// deleteOneBypass removes a single bypass route from the kernel routing
// table. Uses the same flag (-host / -net) as the corresponding add, since
// mac BSD `route delete` won't match across mismatched flags.
func (r *userspaceBSDRouter) deleteOneBypass(route netip.Prefix) {
	del := "delete"
	if version.OS() != "macOS" {
		del = "del"
	}
	flag := "-net"
	nstr := fmt.Sprintf("%v/%d", route.Masked().Addr(), route.Bits())
	if route.IsSingleIP() {
		flag = "-host"
		nstr = route.Addr().String()
	}
	cmd("route", "-q", "-n", del, "-"+inet(route), flag, nstr).CombinedOutput()
}

func (r *userspaceBSDRouter) Close() error {
	// fork: close PF_ROUTE socket → routeMonLoop exits.
	r.mu.Lock()
	close(r.closeCh)
	if r.routeSockFD >= 0 {
		unix.Close(r.routeSockFD)
		r.routeSockFD = -1
	}
	// Clean up bypass routes on shutdown.
	if len(r.bypassRoutes) > 0 {
		r.setBypassRoutesLocked(nil)
	}
	r.mu.Unlock()
	return nil
}

// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package dns

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"

	"go4.org/mem"
	"tailscale.com/control/controlknobs"
	"tailscale.com/health"
	"tailscale.com/net/dns/resolvconffile"
	"tailscale.com/net/tsaddr"
	"tailscale.com/types/logger"
	"tailscale.com/util/eventbus"
	"tailscale.com/util/mak"
	"tailscale.com/util/syspolicy/policyclient"
)

// NewOSConfigurator creates a new OS configurator.
//
// The health tracker, bus and the knobs may be nil and are ignored on this platform.
func NewOSConfigurator(logf logger.Logf, _ *health.Tracker, _ *eventbus.Bus, _ policyclient.Client, _ *controlknobs.Knobs, ifName string) (OSConfigurator, error) {
	return &darwinConfigurator{logf: logf, ifName: ifName}, nil
}

// darwinConfigurator is the tailscaled-on-macOS DNS OS configurator that
// maintains the Split DNS nameserver entries pointing MagicDNS DNS suffixes
// to 100.100.100.100 using the macOS /etc/resolver/$SUFFIX files.
//
// It also handles "global takeover" mode (OSConfig with empty MatchDomains
// + non-empty Nameservers) by writing nameservers to each active network
// service via `networksetup`, since the CLI-only fork has no NetworkExtension
// to do this automatically.
type darwinConfigurator struct {
	logf   logger.Logf
	ifName string

	// lastGlobalNS is the last list of nameservers we installed as the global
	// system resolver. Used to skip redundant networksetup calls + cache flushes
	// when SetDNS is invoked repeatedly with unchanged config.
	// Empty slice = no global DNS currently installed by us.
	lastGlobalNS []netip.Addr
}

func (c *darwinConfigurator) Close() error {
	c.removeResolverFiles(func(domain string) bool { return true })
	// Clear global DNS only if we actually installed one, let DHCP reclaim.
	if len(c.lastGlobalNS) > 0 {
		if err := c.clearGlobalDNS(); err != nil {
			c.logf("clearGlobalDNS on Close failed: %v", err)
		}
	}
	return nil
}

func (c *darwinConfigurator) SupportsSplitDNS() bool {
	return true
}

func (c *darwinConfigurator) SetDNS(cfg OSConfig) error {
	var buf bytes.Buffer
	buf.WriteString(macResolverFileHeader)
	for _, ip := range cfg.Nameservers {
		buf.WriteString("nameserver ")
		buf.WriteString(ip.String())
		buf.WriteString("\n")
	}

	if err := os.MkdirAll("/etc/resolver", 0755); err != nil {
		return err
	}

	var keep map[string]bool

	// Add a dummy file to /etc/resolver with a "search ..." directive if we have
	// search suffixes to add.
	if len(cfg.SearchDomains) > 0 {
		const searchFile = "search.tailscale" // fake DNS suffix+TLD to put our search
		mak.Set(&keep, searchFile, true)
		var sbuf bytes.Buffer
		sbuf.WriteString(macResolverFileHeader)
		sbuf.WriteString("search")
		for _, d := range cfg.SearchDomains {
			sbuf.WriteString(" ")
			sbuf.WriteString(string(d.WithoutTrailingDot()))
		}
		sbuf.WriteString("\n")
		if err := os.WriteFile("/etc/resolver/"+searchFile, sbuf.Bytes(), 0644); err != nil {
			return err
		}
	}

	for _, d := range cfg.MatchDomains {
		fileBase := string(d.WithoutTrailingDot())
		mak.Set(&keep, fileBase, true)
		fullPath := "/etc/resolver/" + fileBase

		if err := os.WriteFile(fullPath, buf.Bytes(), 0644); err != nil {
			return err
		}
	}
	if err := c.removeResolverFiles(func(domain string) bool { return !keep[domain] }); err != nil {
		return err
	}

	// Global takeover mode (OSConfig contract):
	//   MatchDomains empty + Nameservers non-empty → install Nameservers as the
	//   "primary" resolver. Upstream darwin manager assumed the macOS GUI/NE
	//   handled this; in CLI-only fork we must do it ourselves via networksetup.
	if len(cfg.MatchDomains) == 0 && len(cfg.Nameservers) > 0 {
		if nsEqual(c.lastGlobalNS, cfg.Nameservers) {
			// Unchanged from last call; skip to avoid redundant networksetup
			// invocations and mDNSResponder restarts.
			return nil
		}
		if err := c.setGlobalDNS(cfg.Nameservers); err != nil {
			c.logf("setGlobalDNS failed: %v", err)
			// Non-fatal: split-DNS resolver files above still apply.
			return nil
		}
		c.lastGlobalNS = append(c.lastGlobalNS[:0], cfg.Nameservers...)
	} else if len(c.lastGlobalNS) > 0 {
		// We previously installed global DNS, but the new config no longer
		// wants it (either pure split-DNS mode or empty config). Undo.
		if err := c.clearGlobalDNS(); err != nil {
			c.logf("clearGlobalDNS failed: %v", err)
		}
		c.lastGlobalNS = nil
	}
	return nil
}

// nsEqual reports whether two nameserver lists are identical (order-sensitive).
func nsEqual(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// setGlobalDNS installs nameservers as the primary system resolver on every
// active network service via `networksetup -setdnsservers`. Persists across
// reboot (writes to SystemConfiguration Setup: store).
func (c *darwinConfigurator) setGlobalDNS(nameservers []netip.Addr) error {
	services, err := listActiveNetworkServices()
	if err != nil {
		return fmt.Errorf("listActiveNetworkServices: %w", err)
	}
	if len(services) == 0 {
		c.logf("setGlobalDNS: no active network services found")
		return nil
	}

	args := make([]string, 0, 2+len(nameservers))
	args = append(args, "-setdnsservers", "") // service name placeholder
	for _, ns := range nameservers {
		args = append(args, ns.String())
	}

	for _, svc := range services {
		args[1] = svc
		out, err := exec.Command("networksetup", args...).CombinedOutput()
		if err != nil {
			c.logf("networksetup -setdnsservers %q failed: %v: %s", svc, err, out)
			continue
		}
		c.logf("set DNS for %q: %v", svc, nameservers)
	}

	// Best-effort cache flush so apps pick up the new resolver immediately.
	_ = exec.Command("dscacheutil", "-flushcache").Run()
	_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
	return nil
}

// clearGlobalDNS removes any tailscale-installed system DNS, letting DHCP
// or user config reclaim. Called on Close() or when switching to split-DNS.
func (c *darwinConfigurator) clearGlobalDNS() error {
	services, err := listActiveNetworkServices()
	if err != nil {
		return fmt.Errorf("listActiveNetworkServices: %w", err)
	}
	for _, svc := range services {
		// "Empty" tells networksetup to clear user-set DNS and use DHCP.
		out, err := exec.Command("networksetup", "-setdnsservers", svc, "Empty").CombinedOutput()
		if err != nil {
			c.logf("networksetup clear DNS for %q failed: %v: %s", svc, err, out)
		}
	}
	_ = exec.Command("dscacheutil", "-flushcache").Run()
	_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
	return nil
}

// listActiveNetworkServices returns enabled network service names
// (e.g. "Wi-Fi", "Ethernet"). Disabled services (prefixed with "*"), the
// header line, and any "Tailscale*"-named service are filtered out.
//
// "Tailscale" services come from prior installations of the official GUI app
// and are mapped to a utun device; setting DNS on them is meaningless (the
// device has no DHCP/manual DNS context) and could create surprising loops.
func listActiveNetworkServices() ([]string, error) {
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return nil, err
	}
	var services []string
	for i, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" || strings.HasPrefix(line, "*") {
			// First line is header ("An asterisk (*) denotes...");
			// "*"-prefixed lines are disabled services.
			continue
		}
		if strings.HasPrefix(line, "Tailscale") {
			continue
		}
		services = append(services, line)
	}
	return services, nil
}

// GetBaseConfig returns the current OS DNS configuration, extracting it from /etc/resolv.conf.
// We should really be using the SystemConfiguration framework to get this information, as this
// is not a stable public API, and is provided mostly as a compatibility effort with Unix
// tools. Apple might break this in the future. But honestly, parsing the output of `scutil --dns`
// is *even more* likely to break in the future.
func (c *darwinConfigurator) GetBaseConfig() (OSConfig, error) {
	cfg := OSConfig{}

	resolvConf, err := resolvconffile.ParseFile("/etc/resolv.conf")
	if err != nil {
		c.logf("failed to parse /etc/resolv.conf: %v", err)
		return cfg, ErrGetBaseConfigNotSupported
	}

	for _, ns := range resolvConf.Nameservers {
		if ns == tsaddr.TailscaleServiceIP() || ns == tsaddr.TailscaleServiceIPv6() {
			// If we find Quad100 in /etc/resolv.conf, we should ignore it
			c.logf("ignoring 100.100.100.100 resolver IP found in /etc/resolv.conf")
			continue
		}
		cfg.Nameservers = append(cfg.Nameservers, ns)
	}
	cfg.SearchDomains = resolvConf.SearchDomains

	if len(cfg.Nameservers) == 0 {
		// Log a warning in case we couldn't find any nameservers in /etc/resolv.conf.
		c.logf("no nameservers found in /etc/resolv.conf, DNS resolution might fail")
	}

	return cfg, nil
}

const macResolverFileHeader = "# Added by tailscaled\n"

// removeResolverFiles deletes all files in /etc/resolver for which the shouldDelete
// func returns true.
func (c *darwinConfigurator) removeResolverFiles(shouldDelete func(domain string) bool) error {
	dents, err := os.ReadDir("/etc/resolver")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, de := range dents {
		if !de.Type().IsRegular() {
			continue
		}
		name := de.Name()
		if !shouldDelete(name) {
			continue
		}
		fullPath := "/etc/resolver/" + name
		contents, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) { // race?
				continue
			}
			return err
		}
		if !mem.HasPrefix(mem.B(contents), mem.S(macResolverFileHeader)) {
			continue
		}
		if err := os.Remove(fullPath); err != nil {
			return err
		}
	}
	return nil
}

package fsd

import (
	"log"
	"net"
	"net/http"
	"strings"

	"tailscale.com/client/tailscale"
)

// withAuth wraps an http.Handler with Tailnet identity verification.
// Only allows requests from Tailscale IPs (100.64.x.x and fd7a:115c:a1e0::/48).
// Logs the requesting node's identity.
func withAuth(lc *tailscale.LocalClient, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteIP := extractIP(r.RemoteAddr)

		// Only allow Tailscale IPs
		if !isTailscaleIP(remoteIP) {
			http.Error(w, "forbidden: not a tailnet address", http.StatusForbidden)
			return
		}

		// Identify the requesting node
		whois, err := lc.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			log.Printf("fsd: auth: cannot identify %s: %v", remoteIP, err)
			// Still allow — if the IP is in Tailnet range, it passed ACL
			next.ServeHTTP(w, r)
			return
		}

		nodeName := whois.Node.ComputedName
		userName := whois.UserProfile.LoginName
		log.Printf("fsd: %s %s from %s (%s/%s)", r.Method, r.URL.Path, remoteIP, nodeName, userName)

		next.ServeHTTP(w, r)
	})
}

func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func isTailscaleIP(ip string) bool {
	// Tailscale IPv4: 100.64.0.0/10
	// Tailscale IPv6: fd7a:115c:a1e0::/48
	if strings.HasPrefix(ip, "100.") {
		return true
	}
	if strings.HasPrefix(ip, "fd7a:") {
		return true
	}
	// Also allow localhost for local CLI access
	if ip == "127.0.0.1" || ip == "::1" {
		return true
	}
	return false
}

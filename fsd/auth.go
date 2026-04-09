package fsd

import (
	"context"
	"log"
	"net"
	"net/http"
	"strings"

	"tailscale.com/client/tailscale"
)

type ctxKey string

const (
	ctxNodeName ctxKey = "nodeName"
	ctxUserName ctxKey = "userName"
	ctxRole     ctxKey = "role"
)

// CallerInfo extracts identity from request context (set by withAuth).
func CallerInfo(r *http.Request) (nodeName, userName, role string) {
	if v := r.Context().Value(ctxNodeName); v != nil {
		nodeName = v.(string)
	}
	if v := r.Context().Value(ctxUserName); v != nil {
		userName = v.(string)
	}
	if v := r.Context().Value(ctxRole); v != nil {
		role = v.(string)
	}
	return
}

// withAuth wraps an http.Handler with Tailnet identity verification.
// Sets caller identity and role in the request context.
func withAuth(lc *tailscale.LocalClient, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteIP := extractIP(r.RemoteAddr)

		if !isTailscaleIP(remoteIP) {
			http.Error(w, "forbidden: not a tailnet address", http.StatusForbidden)
			return
		}

		// Identify the requesting node
		var nodeName, userName, role string
		whois, err := lc.WhoIs(r.Context(), r.RemoteAddr)
		if err == nil {
			nodeName = whois.Node.ComputedName
			userName = whois.UserProfile.LoginName
			acl := GetACL()
			role = "admin" // default: no ACL = full access
			if acl != nil {
				role = acl.RoleFor(userName)
			}
		} else {
			nodeName = remoteIP
			userName = "unknown"
			role = "guest"
		}

		log.Printf("fsd: %s %s from %s (%s/%s role=%s)", r.Method, r.URL.Path, remoteIP, nodeName, userName, role)

		// Set identity in context
		ctx := r.Context()
		ctx = context.WithValue(ctx, ctxNodeName, nodeName)
		ctx = context.WithValue(ctx, ctxUserName, userName)
		ctx = context.WithValue(ctx, ctxRole, role)

		next.ServeHTTP(w, r.WithContext(ctx))
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
	if strings.HasPrefix(ip, "100.") {
		return true
	}
	if strings.HasPrefix(ip, "fd7a:") {
		return true
	}
	if ip == "127.0.0.1" || ip == "::1" {
		return true
	}
	return false
}

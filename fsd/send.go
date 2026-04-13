package fsd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/tailscale"
)

// SendRequest is the body of POST /send and /broadcast.
type SendRequest struct {
	File    string   `json:"file"`    // local file path to send
	Targets []string `json:"targets"` // node names or IPs (private send)
	Group   string   `json:"group"`   // user group name (group/broadcast)
}

// SendResult is the result per target.
type SendResult struct {
	Target string `json:"target"`
	Status string `json:"status"` // "ok", "skipped", or error message
}

// Send pushes a file to target nodes. Concurrent with short timeouts.
func Send(ctx context.Context, lc *tailscale.LocalClient, cfg Config, req SendRequest) ([]SendResult, error) {
	filePath := req.File
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cfg.SharedDir, filePath)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	fileName := filepath.Base(filePath)

	// Resolve targets
	targets := req.Targets
	if req.Group != "" {
		groupTargets, err := resolveGroup(ctx, lc, req.Group)
		if err != nil {
			return nil, fmt.Errorf("resolve group %q: %w", req.Group, err)
		}
		targets = append(targets, groupTargets...)
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no targets specified")
	}

	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, t := range targets {
		if !seen[t] {
			seen[t] = true
			unique = append(unique, t)
		}
	}

	// HTTP client with short connect timeout — nodes without fsd
	// fail in 3 seconds instead of 30.
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		},
	}

	// Send concurrently
	var mu sync.Mutex
	var results []SendResult
	var wg sync.WaitGroup

	for _, target := range unique {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()

			ip, err := resolveNodeIP(ctx, lc, target)
			if err != nil {
				mu.Lock()
				results = append(results, SendResult{Target: target, Status: fmt.Sprintf("resolve: %v", err)})
				mu.Unlock()
				return
			}

			url := fmt.Sprintf("http://%s:%d/inbox/%s", ip, cfg.Port, fileName)
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
			if err != nil {
				mu.Lock()
				results = append(results, SendResult{Target: target, Status: err.Error()})
				mu.Unlock()
				return
			}

			resp, err := client.Do(req)
			if err != nil {
				status := err.Error()
				// Shorten common error messages
				if strings.Contains(status, "connection refused") {
					status = "no fsd (port 7700 closed)"
				} else if strings.Contains(status, "timeout") || strings.Contains(status, "deadline") {
					status = "timeout (node unreachable or no fsd)"
				}
				mu.Lock()
				results = append(results, SendResult{Target: target, Status: status})
				mu.Unlock()
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			status := "ok"
			if resp.StatusCode != 200 {
				status = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			mu.Lock()
			results = append(results, SendResult{Target: target, Status: status})
			mu.Unlock()
		}(target)
	}

	wg.Wait()
	return results, nil
}

// resolveGroup returns online peer hostnames matching a group/user.
//
// Special values:
//   - "all": all online peers
//   - ACL role name: matches peers whose Headscale user maps to that role
//   - Headscale user name: matches peers owned by that user
//
// ACL-aware: if an ACL is loaded, only returns peers whose role the
// sender's role is allowed to reach (broadcast permission check is
// done at the handler level, not here).
func resolveGroup(ctx context.Context, lc *tailscale.LocalClient, group string) ([]string, error) {
	st, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}

	selfID := st.Self.ID
	var nodes []string

	for _, peer := range st.Peer {
		if string(peer.ID) == string(selfID) {
			continue
		}
		if !peer.Online {
			continue
		}

		if group == "all" {
			nodes = append(nodes, peer.HostName)
			continue
		}

		// Match by Headscale user login name
		if u, ok := st.User[peer.UserID]; ok {
			loginName := u.LoginName
			if loginName == group || strings.HasPrefix(loginName, group+"@") {
				nodes = append(nodes, peer.HostName)
				continue
			}
		}

		// Match by fsd role (from node tags)
		if group == "admin" || group == "agent" || group == "user" {
			if len(peer.TailscaleIPs) > 0 {
				peerAddr := peer.TailscaleIPs[0].String() + ":1"
				if whois, err := lc.WhoIs(ctx, peerAddr); err == nil {
					var tags []string
					for _, t := range whois.Node.Tags {
						tags = append(tags, t)
					}
					if RoleFromTags(tags) == group {
						nodes = append(nodes, peer.HostName)
					}
				}
			}
		}
	}

	return nodes, nil
}

// resolveNodeIP returns the Tailnet IP for a node hostname or IP.
// Prefers online nodes when multiple share the same name.
func resolveNodeIP(ctx context.Context, lc *tailscale.LocalClient, hostname string) (string, error) {
	// If it's already an IP, use directly
	if net.ParseIP(hostname) != nil {
		return hostname, nil
	}

	st, err := lc.Status(ctx)
	if err != nil {
		return "", err
	}

	hostname = strings.ToLower(hostname)
	var fallbackIP string
	for _, peer := range st.Peer {
		hn := strings.ToLower(peer.HostName)
		dn := strings.ToLower(trimDot(peer.DNSName))
		if hn == hostname || dn == hostname {
			if len(peer.TailscaleIPs) > 0 {
				if peer.Online {
					return peer.TailscaleIPs[0].String(), nil
				}
				if fallbackIP == "" {
					fallbackIP = peer.TailscaleIPs[0].String()
				}
			}
		}
	}
	if fallbackIP != "" {
		return fallbackIP, nil
	}
	return "", fmt.Errorf("node %q not found", hostname)
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

package fsd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tailscale.com/client/tailscale"
)

// SendRequest is the body of POST /send.
type SendRequest struct {
	File    string   `json:"file"`    // local file path to send
	Targets []string `json:"targets"` // node names (private send)
	Group   string   `json:"group"`   // user group name (group send)
}

// SendResult is the result per target.
type SendResult struct {
	Target string `json:"target"`
	Status string `json:"status"` // "ok" or error message
}

// Send pushes a file to target nodes.
func Send(ctx context.Context, lc *tailscale.LocalClient, cfg Config, req SendRequest) ([]SendResult, error) {
	// Resolve file path
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

	// Push to each target
	var results []SendResult
	client := &http.Client{Timeout: 30 * time.Second}

	for _, target := range unique {
		ip, err := resolveNodeIP(ctx, lc, target)
		if err != nil {
			results = append(results, SendResult{Target: target, Status: fmt.Sprintf("resolve failed: %v", err)})
			continue
		}

		url := fmt.Sprintf("http://%s:%d/inbox/%s", ip, cfg.Port, fileName)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err != nil {
			results = append(results, SendResult{Target: target, Status: err.Error()})
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			results = append(results, SendResult{Target: target, Status: err.Error()})
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			results = append(results, SendResult{Target: target, Status: "ok"})
		} else {
			results = append(results, SendResult{Target: target, Status: fmt.Sprintf("HTTP %d", resp.StatusCode)})
		}
	}

	return results, nil
}

// resolveGroup returns all node hostnames belonging to a Headscale user/group.
// In Headscale, "user" is the grouping unit. When group="admin-marvin",
// all nodes owned by user "admin-marvin" are returned.
// Special group "all" returns all online peers.
func resolveGroup(ctx context.Context, lc *tailscale.LocalClient, group string) ([]string, error) {
	st, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}

	selfID := st.Self.ID
	var nodes []string

	for _, peer := range st.Peer {
		if string(peer.ID) == string(selfID) {
			continue // skip self
		}
		if !peer.Online {
			continue // skip offline
		}

		if group == "all" {
			nodes = append(nodes, peer.HostName)
			continue
		}

		// Match by Headscale user login name
		// peer.UserID → st.User[peer.UserID].LoginName
		if u, ok := st.User[peer.UserID]; ok {
			loginName := u.LoginName
			// Headscale login format: "username@headscale"
			// Match if login starts with the group name
			if loginName == group || strings.HasPrefix(loginName, group+"@") {
				nodes = append(nodes, peer.HostName)
			}
		}
	}

	return nodes, nil
}

// resolveNodeIP returns the Tailnet IP for a node hostname.
// Prefers online nodes when multiple share the same name.
func resolveNodeIP(ctx context.Context, lc *tailscale.LocalClient, hostname string) (string, error) {
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

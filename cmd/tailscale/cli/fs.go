package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
)

const fsdPort = 7700

var fsCmd = &ffcli.Command{
	Name:       "fs",
	ShortUsage: "tailscale fs <subcommand>",
	ShortHelp:  "File service: send, get, list, inbox",
	Subcommands: []*ffcli.Command{
		fsSendCmd,
		fsGetCmd,
		fsLsCmd,
		fsPutCmd,
		fsInboxCmd,
	},
	Exec: func(ctx context.Context, args []string) error {
		return flag.ErrHelp
	},
}

// --- send ---

var fsSendCmd = &ffcli.Command{
	Name:       "send",
	ShortUsage: "tailscale fs send <file> <target1> [target2...]\n  tailscale fs send <file> --group=<group>",
	ShortHelp:  "Send a file to one or more nodes",
	Exec:       runFsSend,
}

func runFsSend(ctx context.Context, args []string) error {
	if len(args) < 2 {
		// Check if --group flag is used
		return fmt.Errorf("usage: tailscale fs send <file> <target> [target...]\n       tailscale fs send <file> --group=<groupname>")
	}

	filePath := args[0]
	var targets []string
	var group string

	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "--group=") {
			group = strings.TrimPrefix(arg, "--group=")
		} else {
			targets = append(targets, arg)
		}
	}

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	// Build request
	body := map[string]interface{}{
		"file": filePath,
	}
	if group != "" {
		body["group"] = group
	}
	if len(targets) > 0 {
		body["targets"] = targets
	}

	// Get our own Tailnet IP
	st, err := localClient.Status(ctx)
	if err != nil {
		return err
	}
	if len(st.TailscaleIPs) == 0 {
		return fmt.Errorf("not connected to tailnet")
	}
	myIP := st.TailscaleIPs[0].String()

	// If sending to specific targets, push directly via their fsd
	if len(targets) > 0 && group == "" {
		fileName := filepath.Base(filePath)
		for _, target := range targets {
			ip, err := resolveTarget(ctx, target)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", target, err)
				continue
			}
			url := fmt.Sprintf("http://%s:%d/inbox/%s", ip, fsdPort, fileName)
			req, _ := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", target, err)
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				fmt.Printf("  %s: ok\n", target)
			} else {
				fmt.Fprintf(os.Stderr, "  %s: HTTP %d\n", target, resp.StatusCode)
			}
		}
		return nil
	}

	// Group send via our own fsd's /send endpoint
	reqBody, _ := json.Marshal(body)
	url := fmt.Sprintf("http://%s:%d/send", myIP, fsdPort)
	resp, err := http.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var results []struct {
		Target string `json:"target"`
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&results)
	for _, r := range results {
		fmt.Printf("  %s: %s\n", r.Target, r.Status)
	}
	return nil
}

// --- get ---

var fsGetCmd = &ffcli.Command{
	Name:       "get",
	ShortUsage: "tailscale fs get [node:]<path> [local-path]",
	ShortHelp:  "Download a file from shared space",
	Exec:       runFsGet,
}

func runFsGet(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: tailscale fs get [node:]<path> [local-path]")
	}

	remote := args[0]
	node, path := parseNodePath(ctx, remote)

	ip, err := resolveTarget(ctx, node)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s:%d/files/%s", ip, fsdPort, path)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Write to local file
	localPath := filepath.Base(path)
	if len(args) > 1 {
		localPath = args[1]
	}

	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	fmt.Printf("  saved %s (%d bytes)\n", localPath, n)
	return nil
}

// --- put ---

var fsPutCmd = &ffcli.Command{
	Name:       "put",
	ShortUsage: "tailscale fs put <local-file> [node:]<remote-path>",
	ShortHelp:  "Upload a file to shared space",
	Exec:       runFsPut,
}

func runFsPut(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: tailscale fs put <local-file> [node:]<path>")
	}

	localFile := args[0]
	remote := args[1]
	node, path := parseNodePath(ctx, remote)

	data, err := os.ReadFile(localFile)
	if err != nil {
		return err
	}

	ip, err := resolveTarget(ctx, node)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s:%d/files/%s", ip, fsdPort, path)
	req, _ := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		fmt.Printf("  uploaded %s to %s:%s (%d bytes)\n", localFile, node, path, len(data))
	} else {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// --- ls ---

var fsLsCmd = &ffcli.Command{
	Name:       "ls",
	ShortUsage: "tailscale fs ls [node:][path]",
	ShortHelp:  "List files in shared space",
	Exec:       runFsLs,
}

func runFsLs(ctx context.Context, args []string) error {
	remote := ""
	if len(args) > 0 {
		remote = args[0]
	}

	node, path := parseNodePath(ctx, remote)
	ip, err := resolveTarget(ctx, node)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s:%d/files/%s", ip, fsdPort, path)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var entries []struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	json.NewDecoder(resp.Body).Decode(&entries)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, e := range entries {
		kind := "file"
		if e.IsDir {
			kind = "dir"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%d\n", e.Name, kind, e.Size)
	}
	tw.Flush()
	return nil
}

// --- inbox ---

var fsInboxCmd = &ffcli.Command{
	Name:       "inbox",
	ShortUsage: "tailscale fs inbox",
	ShortHelp:  "List received files (auto-saved to Downloads)",
	Exec:       runFsInbox,
}

func runFsInbox(ctx context.Context, args []string) error {
	st, err := localClient.Status(ctx)
	if err != nil {
		return err
	}
	if len(st.TailscaleIPs) == 0 {
		return fmt.Errorf("not connected")
	}
	myIP := st.TailscaleIPs[0].String()

	url := fmt.Sprintf("http://%s:%d/inbox", myIP, fsdPort)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var entries []struct {
		File string `json:"file"`
		Size int64  `json:"size"`
		From string `json:"from"`
		Time string `json:"time"`
		Path string `json:"path"`
	}
	json.NewDecoder(resp.Body).Decode(&entries)

	if len(entries) == 0 {
		fmt.Println("  no received files")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  FILE\tSIZE\tFROM\tSAVED TO\n")
	for _, e := range entries {
		fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\n", e.File, e.Size, e.From, e.Path)
	}
	tw.Flush()
	return nil
}

// --- helpers ---

// parseNodePath splits "node:path" into node and path.
// If no node prefix, uses self.
func parseNodePath(ctx context.Context, s string) (node, path string) {
	if i := strings.Index(s, ":"); i > 0 {
		return s[:i], s[i+1:]
	}
	return "localhost", s
}

func resolveTarget(ctx context.Context, name string) (string, error) {
	st, err := localClient.Status(ctx)
	if err != nil {
		return "", err
	}

	if name == "localhost" || name == "" {
		if len(st.TailscaleIPs) > 0 {
			return st.TailscaleIPs[0].String(), nil
		}
		return "127.0.0.1", nil
	}

	// Check self
	if st.Self != nil {
		if matchNode(st.Self.HostName, st.Self.DNSName, name) {
			if len(st.TailscaleIPs) > 0 {
				return st.TailscaleIPs[0].String(), nil
			}
		}
	}

	// Check peers
	name = strings.ToLower(name)
	for _, p := range st.Peer {
		if matchNode(p.HostName, p.DNSName, name) {
			if len(p.TailscaleIPs) > 0 {
				return p.TailscaleIPs[0].String(), nil
			}
		}
	}
	return "", fmt.Errorf("node %q not found in tailnet", name)
}

func matchNode(hostName, dnsName, target string) bool {
	target = strings.ToLower(target)
	if strings.ToLower(hostName) == target {
		return true
	}
	dns := strings.ToLower(strings.TrimSuffix(dnsName, "."))
	if dns == target {
		return true
	}
	// Also match first component of DNS name (e.g. "marvin-old-win" from "marvin-old-win.ts.example.com")
	if i := strings.Index(dns, "."); i > 0 {
		if dns[:i] == target {
			return true
		}
	}
	return false
}

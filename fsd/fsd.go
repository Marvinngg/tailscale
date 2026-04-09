// Package fsd implements a file service daemon that runs inside tailscaled.
// It provides file distribution across the Tailscale mesh network.
//
// Two interfaces:
//   - HTTP API on Tailnet IP:7700 (for programmatic/AI access)
//   - CLI subcommands via tailscale fs (for human/AI CLI access)
package fsd

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"tailscale.com/client/tailscale"
)

// Service is the file service running inside tailscaled.
type Service struct {
	mu       sync.RWMutex
	cfg      Config
	lc       tailscale.LocalClient
	server   *http.Server
	handlers *Handlers
}

// Config defines the file service configuration.
type Config struct {
	Port       int    `json:"port"`       // HTTP listen port, default 7700
	SharedDir  string `json:"sharedDir"`  // shared space path
	InboxDir   string `json:"inboxDir"`   // inbox metadata path
	ReceiveDir string `json:"receiveDir"` // auto-receive directory (e.g. ~/Downloads)
}

// DefaultConfig returns a default configuration based on OS.
func DefaultConfig() Config {
	var base, receiveDir string
	if runtime.GOOS == "windows" {
		base = filepath.Join(os.Getenv("ProgramData"), "Tailscale")
		receiveDir = filepath.Join(os.Getenv("USERPROFILE"), "Downloads")
	} else {
		base = "/var/lib/tailscale"
		home, _ := os.UserHomeDir()
		if home == "" || home == "/" {
			// daemon runs as root, find the real user's home
			home = "/Users/" + os.Getenv("SUDO_USER")
			if home == "/Users/" {
				home = "/tmp"
			}
		}
		receiveDir = filepath.Join(home, "Downloads")
	}
	return Config{
		Port:       7700,
		SharedDir:  filepath.Join(base, "shared"),
		InboxDir:   filepath.Join(base, "inbox"),
		ReceiveDir: receiveDir,
	}
}

// New creates a new file service.
func New(cfg Config) *Service {
	if cfg.Port == 0 {
		cfg.Port = 7700
	}
	return &Service{
		cfg: cfg,
	}
}

// Start begins serving. Blocks until context is cancelled or error.
// Call with go s.Start(ctx) from tailscaled.
func (s *Service) Start(ctx context.Context) error {
	// Ensure directories exist
	os.MkdirAll(s.cfg.SharedDir, 0755)
	os.MkdirAll(s.cfg.InboxDir, 0755)

	s.handlers = NewHandlers(s.cfg, &s.lc)

	mux := http.NewServeMux()
	mux.HandleFunc("/files/", s.handlers.HandleFiles)
	mux.HandleFunc("/send", s.handlers.HandleSend)
	mux.HandleFunc("/inbox", s.handlers.HandleInbox)
	mux.HandleFunc("/inbox/", s.handlers.HandleInbox)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Retry loop: wait for Tailnet IP and bind, retrying if the IP
	// isn't yet assigned to the tun interface.
	for {
		ip, err := s.waitForTailnetIP(ctx)
		if err != nil {
			return fmt.Errorf("fsd: %w", err)
		}

		addr := fmt.Sprintf("%s:%d", ip, s.cfg.Port)
		s.server = &http.Server{
			Addr:    addr,
			Handler: withAuth(&s.lc, mux),
		}

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("fsd: listen %s: %v, retrying in 3s...", addr, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
				continue
			}
		}

		log.Printf("fsd: serving on %s (shared=%s, inbox=%s)", addr, s.cfg.SharedDir, s.cfg.InboxDir)

		go func() {
			<-ctx.Done()
			s.server.Close()
		}()

		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

// waitForTailnetIP polls tailscaled until we have a Tailnet IP.
func (s *Service) waitForTailnetIP(ctx context.Context) (string, error) {
	for {
		st, err := s.lc.Status(ctx)
		if err == nil && len(st.TailscaleIPs) > 0 {
			return st.TailscaleIPs[0].String(), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

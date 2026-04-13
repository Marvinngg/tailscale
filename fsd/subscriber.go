package fsd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tailscale.com/client/tailscale"
)

// Subscriber connects to a server's SSE endpoint and handles
// incoming events (broadcast file downloads, etc).
type Subscriber struct {
	lc         *tailscale.LocalClient
	cfg        Config
	serverAddr string // e.g. "100.64.0.1:7700"
}

// NewSubscriber creates a new SSE subscriber.
// serverAddr is the Tailscale IP:port of the fsd server (e.g. "100.64.0.1:7700").
func NewSubscriber(lc *tailscale.LocalClient, cfg Config, serverAddr string) *Subscriber {
	return &Subscriber{lc: lc, cfg: cfg, serverAddr: serverAddr}
}

// Run connects to the server's SSE endpoint and processes events.
// Reconnects automatically on disconnection. Blocks until ctx is cancelled.
func (s *Subscriber) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := s.connect(ctx)
		if err != nil {
			log.Printf("fsd: SSE connection to %s failed: %v, retrying in 10s", s.serverAddr, err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (s *Subscriber) connect(ctx context.Context) error {
	url := fmt.Sprintf("http://%s/events", s.serverAddr)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0} // no timeout for SSE
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	log.Printf("fsd: SSE connected to %s", s.serverAddr)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line

	var eventType string
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			s.handleEvent(ctx, eventType, data)
			eventType = ""
			continue
		}
	}

	return scanner.Err()
}

func (s *Subscriber) handleEvent(ctx context.Context, eventType, data string) {
	switch eventType {
	case "connected":
		log.Printf("fsd: SSE server confirmed connection")
	case "broadcast":
		s.handleBroadcast(ctx, data)
	default:
		log.Printf("fsd: SSE unknown event: %s", eventType)
	}
}

func (s *Subscriber) handleBroadcast(ctx context.Context, data string) {
	var event Event
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		log.Printf("fsd: SSE broadcast parse error: %v", err)
		return
	}

	payloadBytes, _ := json.Marshal(event.Payload)
	var payload BroadcastPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		log.Printf("fsd: SSE broadcast payload error: %v", err)
		return
	}

	log.Printf("fsd: broadcast received: %s from %s (%d bytes)", payload.File, payload.From, payload.Size)

	// Download the file from the server
	if payload.URL == "" {
		log.Printf("fsd: broadcast has no download URL")
		return
	}

	resp, err := http.Get(payload.URL)
	if err != nil {
		log.Printf("fsd: broadcast download failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("fsd: broadcast download HTTP %d", resp.StatusCode)
		return
	}

	// Save to ReceiveDir
	os.MkdirAll(s.cfg.ReceiveDir, 0755)
	destPath := filepath.Join(s.cfg.ReceiveDir, payload.File)

	// Handle name collision
	if _, err := os.Stat(destPath); err == nil {
		ext := filepath.Ext(payload.File)
		base := strings.TrimSuffix(payload.File, ext)
		for i := 1; ; i++ {
			destPath = filepath.Join(s.cfg.ReceiveDir, fmt.Sprintf("%s(%d)%s", base, i, ext))
			if _, err := os.Stat(destPath); os.IsNotExist(err) {
				break
			}
		}
	}

	f, err := os.Create(destPath)
	if err != nil {
		log.Printf("fsd: broadcast save failed: %v", err)
		return
	}
	n, _ := io.Copy(f, resp.Body)
	f.Close()

	// Record in inbox
	os.MkdirAll(s.cfg.InboxDir, 0755)
	meta := map[string]interface{}{
		"file": payload.File,
		"size": n,
		"from": payload.From,
		"time": time.Now().Format(time.RFC3339),
		"path": destPath,
		"type": "broadcast",
	}
	metaJSON, _ := json.Marshal(meta)
	metaFile := filepath.Join(s.cfg.InboxDir, payload.File+".json")
	os.WriteFile(metaFile, metaJSON, 0644)

	log.Printf("fsd: broadcast saved: %s → %s (%d bytes)", payload.File, destPath, n)
}

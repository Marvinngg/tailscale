// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/ipn/ipnstate"
)

func init() {
	voiceCmd = getVoiceCmd
}

// voiceConfig is persisted to disk for auto-start.
type voiceConfig struct {
	Target     string `json:"target"`
	Hotkey     string `json:"hotkey"`
	Token      string `json:"token"`
	SampleRate int    `json:"sample_rate"`
}

func voiceConfigPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "Tailscale", "voice.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tailscale", "voice.json")
}

func loadVoiceConfig() (*voiceConfig, error) {
	data, err := os.ReadFile(voiceConfigPath())
	if err != nil {
		return nil, err
	}
	var cfg voiceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveVoiceConfig(cfg *voiceConfig) error {
	path := voiceConfigPath()
	os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, data, 0644)
}

func getVoiceCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "voice",
		ShortUsage: "tailscale voice [--target <host:port>] | setup | stop | status",
		ShortHelp:  "Voice relay: record audio and send to a remote WE instance",
		LongHelp: strings.TrimSpace(`
Voice relay service. Records audio from the local microphone when a
hotkey is pressed, sends it to a remote WE instance for speech recognition.

First time: tailscale voice setup --target mac-dev:9800
  Saves config and creates auto-start entry. Runs in background from then on.

Manual run: tailscale voice --target mac-dev:9800
  Runs in foreground (Ctrl+C to exit).

Other: tailscale voice status  — show current config
       tailscale voice stop    — remove auto-start
`),
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("voice")
			fs.StringVar(&voiceArgs.target, "target", "", "target host:port (e.g. mac-dev:9800)")
			fs.StringVar(&voiceArgs.hotkey, "hotkey", "RAlt", "hotkey (RAlt, RCtrl, F13)")
			fs.StringVar(&voiceArgs.token, "token", "", "auth token")
			fs.IntVar(&voiceArgs.sampleRate, "rate", 16000, "sample rate Hz")
			return fs
		})(),
		Subcommands: []*ffcli.Command{
			{
				Name:      "setup",
				ShortHelp: "Save config and enable auto-start",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("voice setup")
					fs.StringVar(&voiceArgs.target, "target", "", "target host:port")
					fs.StringVar(&voiceArgs.hotkey, "hotkey", "RAlt", "hotkey")
					fs.StringVar(&voiceArgs.token, "token", "", "auth token")
					fs.IntVar(&voiceArgs.sampleRate, "rate", 16000, "sample rate Hz")
					return fs
				})(),
				Exec: runVoiceSetup,
			},
			{
				Name:      "stop",
				ShortHelp: "Remove auto-start",
				Exec:      runVoiceStop,
			},
			{
				Name:      "status",
				ShortHelp: "Show voice relay config",
				Exec:      runVoiceStatus,
			},
		},
		Exec: runVoice,
	}
}

var voiceArgs struct {
	target     string
	hotkey     string
	token      string
	sampleRate int
}

func runVoice(ctx context.Context, args []string) error {
	target := voiceArgs.target

	// If no target specified, try loading saved config
	if target == "" {
		if cfg, err := loadVoiceConfig(); err == nil && cfg.Target != "" {
			target = cfg.Target
			if voiceArgs.hotkey == "RAlt" && cfg.Hotkey != "" {
				voiceArgs.hotkey = cfg.Hotkey
			}
			if voiceArgs.sampleRate == 16000 && cfg.SampleRate != 0 {
				voiceArgs.sampleRate = cfg.SampleRate
			}
			if voiceArgs.token == "" && cfg.Token != "" {
				voiceArgs.token = cfg.Token
			}
		}
	}

	if target == "" {
		return fmt.Errorf("no target configured. Run: tailscale voice setup --target <host:port>")
	}

	// Resolve target
	if !strings.Contains(target, ":") {
		target += ":9800"
	}
	host, _, _ := strings.Cut(target, ":")
	if !strings.Contains(host, ".") && host != "localhost" {
		ip, err := resolveNode(ctx, host)
		if err != nil {
			return fmt.Errorf("cannot resolve %q: %w", host, err)
		}
		_, port, _ := strings.Cut(target, ":")
		target = ip + ":" + port
		printf("Resolved %s → %s\n", host, target)
	}

	targetURL := fmt.Sprintf("http://%s/transcribe", target)

	printf("Voice relay ready\n")
	printf("  Target:  %s\n", targetURL)
	printf("  Hotkey:  %s (press to toggle recording)\n", voiceArgs.hotkey)
	printf("  Rate:    %d Hz\n", voiceArgs.sampleRate)
	printf("  Ctrl+C to exit\n\n")

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	return voiceLoop(ctx, voiceArgs.hotkey, voiceArgs.sampleRate, func(pcmData []byte) {
		wav := pcmToWAV(pcmData, voiceArgs.sampleRate, 1, 16)
		printf("Sending %d bytes...", len(wav))
		if err := sendWAV(targetURL, voiceArgs.token, wav); err != nil {
			printf(" error: %v\n", err)
		} else {
			printf(" ok\n")
		}
	})
}

func runVoiceSetup(ctx context.Context, args []string) error {
	if voiceArgs.target == "" {
		return fmt.Errorf("--target is required")
	}

	cfg := &voiceConfig{
		Target:     voiceArgs.target,
		Hotkey:     voiceArgs.hotkey,
		Token:      voiceArgs.token,
		SampleRate: voiceArgs.sampleRate,
	}

	if err := saveVoiceConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	printf("Config saved: %s\n", voiceConfigPath())

	// Create auto-start entry
	if err := voiceAutoStart(true); err != nil {
		printf("Warning: auto-start setup failed: %v\n", err)
		printf("You can still run manually: tailscale voice\n")
	} else {
		printf("Auto-start enabled\n")
	}

	printf("\nSetup complete. Voice relay will start automatically on login.\n")
	printf("To start now: tailscale voice\n")
	printf("To remove: tailscale voice stop\n")
	return nil
}

func runVoiceStop(ctx context.Context, args []string) error {
	if err := voiceAutoStart(false); err != nil {
		return err
	}
	printf("Auto-start removed\n")
	return nil
}

func runVoiceStatus(ctx context.Context, args []string) error {
	cfg, err := loadVoiceConfig()
	if err != nil {
		printf("Not configured. Run: tailscale voice setup --target <host:port>\n")
		return nil
	}
	printf("Config: %s\n", voiceConfigPath())
	printf("  Target:  %s\n", cfg.Target)
	printf("  Hotkey:  %s\n", cfg.Hotkey)
	printf("  Rate:    %d Hz\n", cfg.SampleRate)

	autoStart := voiceAutoStartEnabled()
	printf("  Auto-start: %v\n", autoStart)
	return nil
}

// resolveNode resolves a Tailscale node name to its Tailscale IP.
func resolveNode(ctx context.Context, name string) (string, error) {
	st, err := localClient.Status(ctx)
	if err != nil {
		return "", err
	}
	name = strings.ToLower(name)
	for _, peer := range st.Peer {
		if matchesPeer(peer, name) && len(peer.TailscaleIPs) > 0 {
			return peer.TailscaleIPs[0].String(), nil
		}
	}
	return "", fmt.Errorf("node %q not found in tailnet", name)
}

func matchesPeer(peer *ipnstate.PeerStatus, name string) bool {
	if strings.ToLower(peer.HostName) == name {
		return true
	}
	if strings.HasPrefix(strings.ToLower(peer.DNSName), name+".") {
		return true
	}
	return false
}

func sendWAV(url, token string, wav []byte) error {
	req, err := http.NewRequest("POST", url, bytes.NewReader(wav))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "audio/wav")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return nil
}

func pcmToWAV(pcm []byte, sampleRate, channels, bitsPerSample int) []byte {
	dataSize := len(pcm)
	blockAlign := channels * bitsPerSample / 8
	byteRate := sampleRate * blockAlign

	var buf bytes.Buffer
	buf.Grow(44 + dataSize)

	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(byteRate))
	binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	binary.Write(&buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(pcm)

	return buf.Bytes()
}

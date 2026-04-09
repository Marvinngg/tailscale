// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/ipn/ipnstate"
)

func init() {
	voiceCmd = getVoiceCmd
}

func getVoiceCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "voice",
		ShortUsage: "tailscale voice --target <host:port> [--hotkey RAlt]",
		ShortHelp:  "Voice relay: record audio and send to a remote WE instance",
		LongHelp: strings.TrimSpace(`
Record audio from the local microphone and send it to a remote WE
instance for speech recognition. The target should be a Tailscale
node running WE with remote inbox enabled.

Press the hotkey to start recording, release to stop and send.
Press Ctrl+C to exit.
`),
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("voice")
			fs.StringVar(&voiceArgs.target, "target", "", "target host:port (e.g. mac-dev:9800 or 100.64.0.5:9800)")
			fs.StringVar(&voiceArgs.hotkey, "hotkey", "RAlt", "hotkey to hold for recording (RAlt, RCtrl, F13)")
			fs.StringVar(&voiceArgs.token, "token", "", "auth token for WE remote inbox")
			fs.IntVar(&voiceArgs.sampleRate, "rate", 16000, "audio sample rate in Hz")
			return fs
		})(),
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
	if voiceArgs.target == "" {
		return fmt.Errorf("--target is required (e.g. --target mac-dev:9800)")
	}

	// Resolve target: if no port specified, default to 9800
	target := voiceArgs.target
	if !strings.Contains(target, ":") {
		target += ":9800"
	}

	// If target looks like a Tailscale hostname (no dots), resolve via status
	host, _, _ := strings.Cut(target, ":")
	if !strings.Contains(host, ".") {
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
	printf("  Hotkey:  %s (hold to record, release to send)\n", voiceArgs.hotkey)
	printf("  Rate:    %d Hz\n", voiceArgs.sampleRate)
	printf("  Ctrl+C to exit\n\n")

	// Platform-specific hotkey + recording loop
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
	// Match DNS name prefix (e.g. "mac-dev" matches "mac-dev.ts.example.com")
	if strings.HasPrefix(strings.ToLower(peer.DNSName), name+".") {
		return true
	}
	return false
}

// sendWAV sends a WAV file to the WE remote inbox.
func sendWAV(url, token string, wav []byte) error {
	req, err := http.NewRequest("POST", url, bytes.NewReader(wav))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "audio/wav")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}
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

// pcmToWAV wraps raw PCM data in a WAV header.
func pcmToWAV(pcm []byte, sampleRate, channels, bitsPerSample int) []byte {
	dataSize := len(pcm)
	blockAlign := channels * bitsPerSample / 8
	byteRate := sampleRate * blockAlign

	var buf bytes.Buffer
	buf.Grow(44 + dataSize)

	// RIFF header
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")

	// fmt chunk
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(byteRate))
	binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	binary.Write(&buf, binary.LittleEndian, uint16(bitsPerSample))

	// data chunk
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(pcm)

	return buf.Bytes()
}

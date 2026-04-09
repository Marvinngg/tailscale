// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

//go:build !windows

package cli

import (
	"context"
	"fmt"
)

func voiceLoop(ctx context.Context, hotkey string, sampleRate int, onRecorded func(pcm []byte)) error {
	return fmt.Errorf("tailscale voice is only supported on Windows")
}

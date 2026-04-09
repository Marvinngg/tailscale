// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procRegisterHotKey  = user32.NewProc("RegisterHotKey")
	procGetMessage      = user32.NewProc("GetMessageW")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")

	winmm               = syscall.NewLazyDLL("winmm.dll")
	procWaveInOpen       = winmm.NewProc("waveInOpen")
	procWaveInClose      = winmm.NewProc("waveInClose")
	procWaveInStart      = winmm.NewProc("waveInStart")
	procWaveInStop       = winmm.NewProc("waveInStop")
	procWaveInReset      = winmm.NewProc("waveInReset")
	procWaveInPrepareHeader   = winmm.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHeader = winmm.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer       = winmm.NewProc("waveInAddBuffer")
)

// Windows constants
const (
	wmHotkey       = 0x0312
	waveMapperID   = 0xFFFFFFFF
	callbackNull   = 0
	whdrDone       = 0x00000001
)

// MOD key modifiers for RegisterHotKey
const (
	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004
)

// Virtual key codes
const (
	vkRMenu   = 0xA5 // Right Alt
	vkRControl = 0xA3 // Right Control
	vkF13     = 0x7C
)

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

type waveHdr struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// audioRecorder records audio using Windows waveIn API.
type audioRecorder struct {
	mu         sync.Mutex
	handle     uintptr
	buffers    []*recordBuffer
	recording  bool
	sampleRate int
}

type recordBuffer struct {
	data   []byte
	header waveHdr
}

const (
	bufferSize  = 32000 // ~1 second at 16kHz 16bit mono
	bufferCount = 30    // up to 30 seconds
)

func newAudioRecorder(sampleRate int) *audioRecorder {
	return &audioRecorder{sampleRate: sampleRate}
}

func (r *audioRecorder) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	wfx := waveFormatEx{
		FormatTag:      1, // WAVE_FORMAT_PCM
		Channels:       1,
		SamplesPerSec:  uint32(r.sampleRate),
		AvgBytesPerSec: uint32(r.sampleRate * 2), // 16-bit mono
		BlockAlign:     2,
		BitsPerSample:  16,
		Size:           0,
	}

	ret, _, _ := procWaveInOpen.Call(
		uintptr(unsafe.Pointer(&r.handle)),
		uintptr(waveMapperID),
		uintptr(unsafe.Pointer(&wfx)),
		0, 0,
		uintptr(callbackNull),
	)
	if ret != 0 {
		return fmt.Errorf("waveInOpen failed: %d", ret)
	}

	// Prepare buffers
	r.buffers = make([]*recordBuffer, bufferCount)
	for i := range r.buffers {
		buf := &recordBuffer{
			data: make([]byte, bufferSize),
		}
		buf.header.Data = uintptr(unsafe.Pointer(&buf.data[0]))
		buf.header.BufferLength = uint32(bufferSize)

		procWaveInPrepareHeader.Call(r.handle, uintptr(unsafe.Pointer(&buf.header)), unsafe.Sizeof(buf.header))
		procWaveInAddBuffer.Call(r.handle, uintptr(unsafe.Pointer(&buf.header)), unsafe.Sizeof(buf.header))

		r.buffers[i] = buf
	}

	ret, _, _ = procWaveInStart.Call(r.handle)
	if ret != 0 {
		procWaveInClose.Call(r.handle)
		return fmt.Errorf("waveInStart failed: %d", ret)
	}

	r.recording = true
	return nil
}

func (r *audioRecorder) stop() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.recording {
		return nil
	}
	r.recording = false

	procWaveInStop.Call(r.handle)
	procWaveInReset.Call(r.handle)

	// Collect recorded data from all buffers that have data
	var pcm []byte
	for _, buf := range r.buffers {
		if buf.header.Flags&whdrDone != 0 && buf.header.BytesRecorded > 0 {
			pcm = append(pcm, buf.data[:buf.header.BytesRecorded]...)
		}
		procWaveInUnprepareHeader.Call(r.handle, uintptr(unsafe.Pointer(&buf.header)), unsafe.Sizeof(buf.header))
	}

	procWaveInClose.Call(r.handle)
	r.buffers = nil

	return pcm
}

// voiceLoop is the platform-specific hotkey + recording event loop.
func voiceLoop(ctx context.Context, hotkey string, sampleRate int, onRecorded func(pcm []byte)) error {
	// Parse hotkey to virtual key code + modifiers
	vk, mod, err := parseHotkey(hotkey)
	if err != nil {
		return err
	}

	// Register global hotkey
	const hotkeyID = 1
	ret, _, _ := procRegisterHotKey.Call(0, hotkeyID, uintptr(mod), uintptr(vk))
	if ret == 0 {
		return fmt.Errorf("RegisterHotKey failed (is %s already in use?)", hotkey)
	}
	defer procUnregisterHotKey.Call(0, hotkeyID)

	recorder := newAudioRecorder(sampleRate)
	recording := false

	// Message loop in a goroutine, cancel via context
	go func() {
		<-ctx.Done()
		// Post WM_QUIT to break GetMessage loop
		procPostQuitMessage := user32.NewProc("PostThreadMessageW")
		tid := syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")
		// This is best-effort; the process will exit anyway
		_ = procPostQuitMessage
		_ = tid
	}()

	var m msg
	for {
		// Check context before blocking on GetMessage
		select {
		case <-ctx.Done():
			if recording {
				pcm := recorder.stop()
				if len(pcm) > 0 {
					onRecorded(pcm)
				}
			}
			return nil
		default:
		}

		// PeekMessage with PM_REMOVE to avoid blocking forever
		procPeekMessage := user32.NewProc("PeekMessageW")
		ret, _, _ := procPeekMessage.Call(
			uintptr(unsafe.Pointer(&m)),
			0, 0, 0,
			1, // PM_REMOVE
		)
		if ret == 0 {
			// No message, sleep briefly and check context
			select {
			case <-ctx.Done():
				if recording {
					pcm := recorder.stop()
					if len(pcm) > 0 {
						onRecorded(pcm)
					}
				}
				return nil
			default:
				time.Sleep(50 * time.Millisecond) // 50ms polling
				continue
			}
		}

		if m.Message == wmHotkey && m.WParam == hotkeyID {
			if !recording {
				// Start recording
				if err := recorder.start(); err != nil {
					printf("Recording error: %v\n", err)
					continue
				}
				recording = true
				printf("● Recording...\n")
			} else {
				// Stop recording and send
				pcm := recorder.stop()
				recording = false
				if len(pcm) > 0 {
					printf("■ Stopped (%d bytes)\n", len(pcm))
					onRecorded(pcm)
				} else {
					printf("■ Stopped (empty, skipped)\n")
				}
			}
		}
	}
}

func parseHotkey(s string) (vk, mod int, err error) {
	switch strings.ToLower(s) {
	case "ralt":
		return vkRMenu, modAlt, nil
	case "rctrl":
		return vkRControl, modControl, nil
	case "f13":
		return vkF13, 0, nil
	default:
		return 0, 0, fmt.Errorf("unsupported hotkey %q (use RAlt, RCtrl, or F13)", s)
	}
}

// voiceAutoStart creates/removes a Windows startup registry entry.
func voiceAutoStart(enable bool) error {
	key, _, err := regCreateKey(regCurrentUser, `Software\Microsoft\Windows\CurrentVersion\Run`)
	if err != nil {
		return fmt.Errorf("open registry: %w", err)
	}
	defer regCloseKey(key)

	const valueName = "TailscaleVoice"
	if enable {
		exe, _ := os.Executable()
		cmd := fmt.Sprintf(`"%s" voice`, exe)
		return regSetString(key, valueName, cmd)
	}
	return regDeleteValue(key, valueName)
}

func voiceAutoStartEnabled() bool {
	key, err := regOpenKey(regCurrentUser, `Software\Microsoft\Windows\CurrentVersion\Run`)
	if err != nil {
		return false
	}
	defer regCloseKey(key)
	_, err = regGetString(key, "TailscaleVoice")
	return err == nil
}

// Minimal registry helpers (avoid importing golang.org/x/sys/windows/registry)
var (
	advapi32         = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKey = advapi32.NewProc("RegCreateKeyExW")
	procRegOpenKey   = advapi32.NewProc("RegOpenKeyExW")
	procRegSetValue  = advapi32.NewProc("RegSetValueExW")
	procRegDelValue  = advapi32.NewProc("RegDeleteValueW")
	procRegQueryValue = advapi32.NewProc("RegQueryValueExW")
	procRegCloseKey  = advapi32.NewProc("RegCloseKey")
)

const regCurrentUser = 0x80000001 // HKEY_CURRENT_USER

func regCreateKey(root uintptr, path string) (uintptr, bool, error) {
	pathW, _ := syscall.UTF16PtrFromString(path)
	var key uintptr
	var disp uint32
	ret, _, _ := procRegCreateKey.Call(root, uintptr(unsafe.Pointer(pathW)),
		0, 0, 0, 0xF003F, 0, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&disp)))
	if ret != 0 {
		return 0, false, fmt.Errorf("RegCreateKeyEx: %d", ret)
	}
	return key, disp == 1, nil
}

func regOpenKey(root uintptr, path string) (uintptr, error) {
	pathW, _ := syscall.UTF16PtrFromString(path)
	var key uintptr
	ret, _, _ := procRegOpenKey.Call(root, uintptr(unsafe.Pointer(pathW)), 0, 0x20019, uintptr(unsafe.Pointer(&key)))
	if ret != 0 {
		return 0, fmt.Errorf("RegOpenKeyEx: %d", ret)
	}
	return key, nil
}

func regSetString(key uintptr, name, value string) error {
	nameW, _ := syscall.UTF16PtrFromString(name)
	valueW, _ := syscall.UTF16FromString(value)
	ret, _, _ := procRegSetValue.Call(key, uintptr(unsafe.Pointer(nameW)),
		0, 1, // REG_SZ
		uintptr(unsafe.Pointer(&valueW[0])), uintptr(len(valueW)*2))
	if ret != 0 {
		return fmt.Errorf("RegSetValueEx: %d", ret)
	}
	return nil
}

func regDeleteValue(key uintptr, name string) error {
	nameW, _ := syscall.UTF16PtrFromString(name)
	ret, _, _ := procRegDelValue.Call(key, uintptr(unsafe.Pointer(nameW)))
	if ret != 0 {
		return fmt.Errorf("RegDeleteValue: %d", ret)
	}
	return nil
}

func regGetString(key uintptr, name string) (string, error) {
	nameW, _ := syscall.UTF16PtrFromString(name)
	var dataType, size uint32
	ret, _, _ := procRegQueryValue.Call(key, uintptr(unsafe.Pointer(nameW)),
		0, uintptr(unsafe.Pointer(&dataType)), 0, uintptr(unsafe.Pointer(&size)))
	if ret != 0 {
		return "", fmt.Errorf("RegQueryValueEx: %d", ret)
	}
	buf := make([]uint16, size/2)
	ret, _, _ = procRegQueryValue.Call(key, uintptr(unsafe.Pointer(nameW)),
		0, uintptr(unsafe.Pointer(&dataType)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ret != 0 {
		return "", fmt.Errorf("RegQueryValueEx: %d", ret)
	}
	return syscall.UTF16ToString(buf), nil
}

func regCloseKey(key uintptr) {
	procRegCloseKey.Call(key)
}

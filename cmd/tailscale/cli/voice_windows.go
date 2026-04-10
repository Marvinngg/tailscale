// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procSetWindowsHookEx    = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetMessage          = user32.NewProc("GetMessageW")
	procPostThreadMessage   = user32.NewProc("PostThreadMessageW")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
	procSleep               = kernel32.NewProc("Sleep")

	winmm                     = syscall.NewLazyDLL("winmm.dll")
	procWaveInOpen            = winmm.NewProc("waveInOpen")
	procWaveInClose           = winmm.NewProc("waveInClose")
	procWaveInStart           = winmm.NewProc("waveInStart")
	procWaveInStop            = winmm.NewProc("waveInStop")
	procWaveInReset           = winmm.NewProc("waveInReset")
	procWaveInPrepareHeader   = winmm.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHeader = winmm.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer       = winmm.NewProc("waveInAddBuffer")
)

const (
	whKeyboardLL = 13
	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105
	wmQuit       = 0x0012
	waveMapperID = 0xFFFFFFFF
	callbackNull = 0
	whdrDone     = 0x00000001
)

const (
	vkRMenu    = 0xA5
	vkRControl = 0xA3
	vkF9       = 0x78
	vkF13      = 0x7C
)

type kbdLLHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// --- Global hook state ---
var (
	hookTargetVK uint32
	hookKeyDown  func()
	hookKeyUp    func()
	hookHandle   uintptr
	hookThreadID uint32 // saved for WM_QUIT from signal handler
)

func hookCallback(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode >= 0 {
		kb := (*kbdLLHookStruct)(unsafe.Pointer(lParam))

		// Debug: log ALL key events to find what VkCode remote desktop sends
		if hookDebug.Load() && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
			printf("  [debug] vk=0x%X scan=0x%X flags=0x%X msg=0x%X\n",
				kb.VkCode, kb.ScanCode, kb.Flags, wParam)
		}

		if kb.VkCode == hookTargetVK {
			switch wParam {
			case wmKeyDown, wmSysKeyDown:
				if hookKeyDown != nil {
					hookKeyDown()
				}
			case wmKeyUp, wmSysKeyUp:
				if hookKeyUp != nil {
					hookKeyUp()
				}
			}
			// Swallow the key — don't pass to remote desktop or any app.
			return 1
		}
	}
	ret, _, _ := procCallNextHookEx.Call(hookHandle, uintptr(nCode), wParam, lParam)
	return ret
}

// --- Audio Recorder ---

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

// audioRecorder uses a circular buffer pool — 4 buffers rotate,
// filled buffers are drained and re-queued. No time limit.
type audioRecorder struct {
	mu         sync.Mutex
	handle     uintptr
	buffers    [numBufs]recordBuffer
	pcm        []byte // accumulated PCM data (grows dynamically)
	recording  atomic.Bool
	stopDrain  chan struct{}
	sampleRate int
}

type recordBuffer struct {
	data   []byte
	header waveHdr
}

const (
	bufChunkSize = 16000 // 0.5s at 16kHz 16bit mono
	numBufs      = 4     // rotating pool
)

func newAudioRecorder(sampleRate int) *audioRecorder {
	return &audioRecorder{sampleRate: sampleRate}
}

func (r *audioRecorder) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.pcm = nil
	r.stopDrain = make(chan struct{})

	wfx := waveFormatEx{
		FormatTag:      1,
		Channels:       1,
		SamplesPerSec:  uint32(r.sampleRate),
		AvgBytesPerSec: uint32(r.sampleRate * 2),
		BlockAlign:     2,
		BitsPerSample:  16,
		Size:           0,
	}

	ret, _, err := procWaveInOpen.Call(
		uintptr(unsafe.Pointer(&r.handle)),
		uintptr(waveMapperID),
		uintptr(unsafe.Pointer(&wfx)),
		0, 0, uintptr(callbackNull),
	)
	if ret != 0 {
		return fmt.Errorf("waveInOpen failed: %d (%v)", ret, err)
	}

	for i := range r.buffers {
		r.buffers[i].data = make([]byte, bufChunkSize)
		r.buffers[i].header = waveHdr{}
		r.buffers[i].header.Data = uintptr(unsafe.Pointer(&r.buffers[i].data[0]))
		r.buffers[i].header.BufferLength = uint32(bufChunkSize)
		procWaveInPrepareHeader.Call(r.handle, uintptr(unsafe.Pointer(&r.buffers[i].header)), unsafe.Sizeof(r.buffers[i].header))
		procWaveInAddBuffer.Call(r.handle, uintptr(unsafe.Pointer(&r.buffers[i].header)), unsafe.Sizeof(r.buffers[i].header))
	}

	ret, _, err = procWaveInStart.Call(r.handle)
	if ret != 0 {
		procWaveInClose.Call(r.handle)
		return fmt.Errorf("waveInStart failed: %d (%v)", ret, err)
	}
	r.recording.Store(true)

	// Background goroutine: drain filled buffers and re-queue them.
	go r.drainLoop()

	return nil
}

// drainLoop polls for completed buffers, copies data out, re-queues them.
func (r *audioRecorder) drainLoop() {
	for {
		select {
		case <-r.stopDrain:
			return
		default:
		}

		r.mu.Lock()
		for i := range r.buffers {
			hdr := &r.buffers[i].header
			if hdr.Flags&whdrDone != 0 && hdr.BytesRecorded > 0 {
				// Copy recorded data
				r.pcm = append(r.pcm, r.buffers[i].data[:hdr.BytesRecorded]...)
				// Reset and re-queue
				procWaveInUnprepareHeader.Call(r.handle, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
				hdr.BytesRecorded = 0
				hdr.Flags = 0
				procWaveInPrepareHeader.Call(r.handle, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
				procWaveInAddBuffer.Call(r.handle, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
			}
		}
		r.mu.Unlock()

		procSleep.Call(50) // poll every 50ms
	}
}

func (r *audioRecorder) stop() []byte {
	if !r.recording.Load() {
		return nil
	}
	r.recording.Store(false)

	// Stop drain goroutine
	close(r.stopDrain)

	r.mu.Lock()
	defer r.mu.Unlock()

	procWaveInStop.Call(r.handle)
	procWaveInReset.Call(r.handle)
	procSleep.Call(100)

	// Drain any remaining buffers
	for i := range r.buffers {
		hdr := &r.buffers[i].header
		if hdr.BytesRecorded > 0 {
			r.pcm = append(r.pcm, r.buffers[i].data[:hdr.BytesRecorded]...)
		}
		procWaveInUnprepareHeader.Call(r.handle, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
	}

	procWaveInClose.Call(r.handle)

	result := r.pcm
	r.pcm = nil
	return result
}

// --- Voice Loop ---

func voiceLoop(ctx context.Context, hotkey string, sampleRate int, onRecorded func(pcm []byte)) error {
	// Pin goroutine to OS thread — required for Windows hooks.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vk, err := parseHotkey(hotkey)
	if err != nil {
		return err
	}

	// Save this thread's ID BEFORE installing hook.
	tid, _, _ := procGetCurrentThreadId.Call()
	hookThreadID = uint32(tid)

	recorder := newAudioRecorder(sampleRate)
	recording := false

	hookTargetVK = uint32(vk)
	hookKeyDown = func() {
		if !recording {
			if err := recorder.start(); err != nil {
				printf("  Recording error: %v\n", err)
				return
			}
			recording = true
			printf("  ● Recording... (release %s to send)\n", hotkey)
		}
	}
	hookKeyUp = func() {
		if recording {
			pcm := recorder.stop()
			recording = false
			if len(pcm) > 0 {
				printf("  ■ Stopped (%d bytes, %.1fs)\n", len(pcm), float64(len(pcm))/float64(sampleRate*2))
				go onRecorded(pcm)
			} else {
				printf("  ■ Stopped (empty)\n")
			}
		}
	}

	// Install low-level keyboard hook on THIS thread.
	modHandle, _, _ := procGetModuleHandle.Call(0)
	hookCB := syscall.NewCallback(hookCallback)

	installHook := func() {
		if hookHandle != 0 {
			procUnhookWindowsHookEx.Call(hookHandle)
			hookHandle = 0
		}
		hk, _, _ := procSetWindowsHookEx.Call(whKeyboardLL, hookCB, modHandle, 0)
		hookHandle = hk
	}

	installHook()
	if hookHandle == 0 {
		return fmt.Errorf("SetWindowsHookEx failed")
	}
	defer func() {
		if hookHandle != 0 {
			procUnhookWindowsHookEx.Call(hookHandle)
		}
	}()

	printf("  Hook installed (vk=0x%X, thread=%d)\n\n", vk, hookThreadID)

	// Monitor foreground window changes. When a remote desktop app
	// gains focus, it installs its own keyboard hook with higher
	// priority. We re-install ours AFTER to regain priority.
	// This is the proven technique from the AutoHotkey community.
	procGetForegroundWindow := user32.NewProc("GetForegroundWindow")
	procGetClassNameW := user32.NewProc("GetClassNameW")

	var lastFgWindow uintptr
	isRemoteDesktopClass := func(hwnd uintptr) bool {
		if hwnd == 0 {
			return false
		}
		buf := make([]uint16, 256)
		procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 256)
		cls := syscall.UTF16ToString(buf)
		// Known remote desktop window classes:
		// TscShellContainerClass = mstsc.exe
		// RAIL_WINDOW = RemoteApp
		// OPContainerClass = Remote Desktop Manager embedded
		// QWidget, Qt5152QWindowIcon = various VNC clients
		// Also match anything with "remote", "vnc", "rdp" in class name
		switch cls {
		case "TscShellContainerClass", "RAIL_WINDOW", "OPContainerClass":
			return true
		}
		lowerCls := strings.ToLower(cls)
		if strings.Contains(lowerCls, "remote") || strings.Contains(lowerCls, "vnc") ||
			strings.Contains(lowerCls, "rdp") || strings.Contains(lowerCls, "parsec") {
			return true
		}
		return false
	}

	// Timer: re-install hook when remote desktop gains focus.
	procSetTimer := user32.NewProc("SetTimer")
	procSetTimer.Call(0, 1, 500, 0) // 500ms timer → generates WM_TIMER

	// Ctrl+C handler: post WM_QUIT to THIS thread's message queue.
	go func() {
		<-ctx.Done()
		procPostThreadMessage.Call(uintptr(hookThreadID), wmQuit, 0, 0)
	}()

	// Message pump — MUST run on the same thread as the hook.
	const wmTimer = 0x0113
	var m msg
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if ret == 0 || ret == uintptr(^uintptr(0)) {
			break
		}

		// On WM_TIMER: check if foreground window changed to remote desktop
		if m.Message == wmTimer {
			fgWnd, _, _ := procGetForegroundWindow.Call()
			if fgWnd != lastFgWindow {
				lastFgWindow = fgWnd
				if isRemoteDesktopClass(fgWnd) {
					// Remote desktop just gained focus — wait for it to
					// install its hook, then re-install ours on top.
					procSleep.Call(100)
					installHook()
					if hookDebug.Load() {
						printf("  [debug] re-installed hook (remote desktop focused)\n")
					}
				}
			}
		}
	}

	// Cleanup: stop recording if still active
	if recording {
		pcm := recorder.stop()
		if len(pcm) > 0 {
			printf("  Sending final recording...\n")
			onRecorded(pcm)
		}
	}

	printf("  Voice relay stopped.\n")
	return nil
}

func parseHotkey(s string) (int, error) {
	switch strings.ToLower(s) {
	case "ralt":
		return vkRMenu, nil
	case "rctrl":
		return vkRControl, nil
	case "f9":
		return vkF9, nil
	case "f13":
		return vkF13, nil
	default:
		return 0, fmt.Errorf("unsupported hotkey %q (use RAlt, RCtrl, F9, F13)", s)
	}
}

// --- Auto-start (registry) ---

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

// --- Registry helpers ---

var (
	advapi32          = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKey  = advapi32.NewProc("RegCreateKeyExW")
	procRegOpenKey    = advapi32.NewProc("RegOpenKeyExW")
	procRegSetValue   = advapi32.NewProc("RegSetValueExW")
	procRegDelValue   = advapi32.NewProc("RegDeleteValueW")
	procRegQueryValue = advapi32.NewProc("RegQueryValueExW")
	procRegCloseKey   = advapi32.NewProc("RegCloseKey")
)

const regCurrentUser = 0x80000001

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
		0, 1, uintptr(unsafe.Pointer(&valueW[0])), uintptr(len(valueW)*2))
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

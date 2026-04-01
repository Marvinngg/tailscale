// setup.exe: install Enhanced Tailscale.
// 1. Run official installer silently (sets up driver, firewall, service)
// 2. Replace binaries with our enhanced versions
// 3. Restart service
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const installDir = `C:\Program Files\Tailscale`

func main() {
	if !isAdmin() {
		relaunchAsAdmin()
		return
	}

	fmt.Println()
	fmt.Println("  Enhanced Tailscale Setup")
	fmt.Println()

	srcDir := filepath.Dir(os.Args[0])
	if srcDir == "." || srcDir == "" {
		srcDir, _ = os.Getwd()
	}

	// Verify files
	for _, f := range []string{"tailscale-install.exe", "tailscaled.exe", "tailscale.exe"} {
		if _, err := os.Stat(filepath.Join(srcDir, f)); err != nil {
			fmt.Printf("  Missing: %s\n", f)
			wait()
			return
		}
	}

	// Step 1: Run official installer silently
	fmt.Println("  [1/4] Installing base system (driver, firewall, service)...")
	installer := filepath.Join(srcDir, "tailscale-install.exe")
	out, err := exec.Command(installer, "/S").CombinedOutput()
	if err != nil {
		// Try /quiet for MSI-style
		out, err = exec.Command(installer, "/quiet", "/norestart").CombinedOutput()
	}
	if err != nil {
		fmt.Printf("  Installer output: %s (%v)\n", strings.TrimSpace(string(out)), err)
	}

	// Wait for installation to complete and service to start
	fmt.Println("  Waiting for service to start...")
	for i := 0; i < 30; i++ {
		out, _ := exec.Command("sc.exe", "query", "Tailscale").CombinedOutput()
		if strings.Contains(string(out), "RUNNING") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(3 * time.Second)

	// Step 2: Stop service and replace binaries
	fmt.Println("  [2/4] Replacing with enhanced version...")
	exec.Command("taskkill", "/F", "/IM", "tailscale-ipn.exe").Run()
	exec.Command("net", "stop", "Tailscale").Run()
	time.Sleep(3 * time.Second)

	for _, f := range []string{"tailscaled.exe", "tailscale.exe"} {
		src := filepath.Join(srcDir, f)
		dst := filepath.Join(installDir, f)
		if err := copyFile(src, dst); err != nil {
			fmt.Printf("  ERROR replacing %s: %v\n", f, err)
			wait()
			return
		}
	}

	// Copy GUI if present
	guiSrc := filepath.Join(srcDir, "tailscale-gui.exe")
	if _, err := os.Stat(guiSrc); err == nil {
		copyFile(guiSrc, filepath.Join(installDir, "tailscale-gui.exe"))
	}

	// Create shared directories
	dataDir := filepath.Join(os.Getenv("ProgramData"), "Tailscale")
	os.MkdirAll(filepath.Join(dataDir, "shared"), 0755)
	os.MkdirAll(filepath.Join(dataDir, "inbox"), 0755)

	// Step 3: Start service + GUI
	fmt.Println("  [3/4] Starting enhanced service...")
	exec.Command("net", "start", "Tailscale").Run()
	time.Sleep(5 * time.Second)

	// Launch official GUI
	exec.Command(filepath.Join(installDir, "tailscale-ipn.exe")).Start()

	// Step 4: Verify
	fmt.Println("  [4/4] Verifying...")
	out, _ = exec.Command(filepath.Join(installDir, "tailscale.exe"), "version").CombinedOutput()
	ver := strings.TrimSpace(string(out))

	out, _ = exec.Command("sc.exe", "query", "Tailscale").CombinedOutput()
	running := strings.Contains(string(out), "RUNNING")

	fmt.Println()
	fmt.Println("  ========================================")
	if running {
		fmt.Println("  Done!")
	} else {
		fmt.Println("  Warning: service may not be running")
	}
	fmt.Printf("  Version: %s\n", ver)
	fmt.Println()
	fmt.Println("  Connect: tailscale up --login-server=YOUR_SERVER --authkey=YOUR_KEY")
	fmt.Println("  ========================================")
	wait()
}

func isAdmin() bool {
	_, err := os.Open(`\\.\PHYSICALDRIVE0`)
	return err == nil
}

func relaunchAsAdmin() {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	verb, _ := syscall.UTF16PtrFromString("runas")
	exePath, _ := syscall.UTF16PtrFromString(exe)
	cwdPath, _ := syscall.UTF16PtrFromString(cwd)
	windows.ShellExecute(0, verb, exePath, nil, cwdPath, 1)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}

func wait() {
	fmt.Println()
	fmt.Println("  Press Enter to close...")
	fmt.Scanln()
}

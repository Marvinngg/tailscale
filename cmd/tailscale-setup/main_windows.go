// setup.exe: install Marvin Tailscale on Windows.
//
// Architecture: install official MSI first (provides tailscale-ipn.exe,
// WinTun driver, service framework, session management), then replace
// tailscaled.exe and tailscale.exe with our enhanced versions.
//
// tailscale-ipn.exe is REQUIRED on Windows — it maintains the daemon
// session. Without it, every CLI connection triggers profile switch loops.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	installDir = `C:\Program Files\Tailscale`
	msiURL     = "https://pkgs.tailscale.com/stable/tailscale-setup-latest-amd64.msi"
	msiFile    = "tailscale-setup.msi"
)

func main() {
	if !isAdmin() {
		relaunchAsAdmin()
		return
	}

	fmt.Println()
	fmt.Println("  Marvin Tailscale Setup")
	fmt.Println()

	srcDir := filepath.Dir(os.Args[0])
	if srcDir == "." || srcDir == "" {
		srcDir, _ = os.Getwd()
	}

	// Verify our binaries exist
	for _, f := range []string{"tailscaled.exe", "tailscale.exe"} {
		if _, err := os.Stat(filepath.Join(srcDir, f)); err != nil {
			fmt.Printf("  Missing: %s\n", f)
			wait()
			return
		}
	}

	// Step 1: Get MSI (download if not present)
	msiPath := filepath.Join(srcDir, msiFile)
	if fi, err := os.Stat(msiPath); err != nil || fi.Size() < 10*1024*1024 {
		fmt.Println("  [1/4] Downloading official Tailscale MSI (~35MB)...")
		if err := downloadFile(msiPath, msiURL); err != nil {
			fmt.Printf("  Download failed: %v\n", err)
			fmt.Println("  You can manually download:")
			fmt.Printf("    %s\n", msiURL)
			wait()
			return
		}
		fi, _ = os.Stat(msiPath)
		fmt.Printf("  Downloaded (%d MB).\n", fi.Size()/1024/1024)
	} else {
		fmt.Printf("  [1/4] MSI found (%d MB).\n", fi.Size()/1024/1024)
	}

	// Step 2: Install MSI silently
	fmt.Println("  [2/4] Installing base system...")
	exec.Command("taskkill", "/F", "/IM", "tailscale-ipn.exe").Run()
	exec.Command("net", "stop", "Tailscale").Run()
	time.Sleep(2 * time.Second)

	out, err := exec.Command("msiexec", "/i", msiPath, "/quiet", "/norestart").CombinedOutput()
	if err != nil {
		fmt.Printf("  MSI: %s (%v)\n", strings.TrimSpace(string(out)), err)
	}

	// Wait for service
	fmt.Println("  Waiting for service...")
	for i := 0; i < 60; i++ {
		out, _ := exec.Command("sc.exe", "query", "Tailscale").CombinedOutput()
		if strings.Contains(string(out), "RUNNING") {
			break
		}
		if strings.Contains(string(out), "STOPPED") {
			exec.Command("net", "start", "Tailscale").Run()
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(3 * time.Second)

	// Step 3: Stop and replace our binaries
	fmt.Println("  [3/4] Replacing with enhanced version...")
	exec.Command("taskkill", "/F", "/IM", "tailscale-ipn.exe").Run()
	exec.Command("net", "stop", "Tailscale").Run()
	time.Sleep(3 * time.Second)

	for _, f := range []string{"tailscaled.exe", "tailscale.exe"} {
		src := filepath.Join(srcDir, f)
		dst := filepath.Join(installDir, f)
		if err := copyFile(src, dst); err != nil {
			fmt.Printf("  ERROR: %s: %v\n", f, err)
			wait()
			return
		}
	}

	// Create data directories
	dataDir := filepath.Join(os.Getenv("ProgramData"), "Tailscale")
	os.MkdirAll(filepath.Join(dataDir, "shared"), 0755)
	os.MkdirAll(filepath.Join(dataDir, "inbox"), 0755)

	// Step 4: Start service + GUI
	fmt.Println("  [4/4] Starting service...")
	exec.Command("net", "start", "Tailscale").Run()
	time.Sleep(3 * time.Second)

	// Launch the GUI (maintains session, prevents profile switch loops)
	ipnPath := filepath.Join(installDir, "tailscale-ipn.exe")
	if _, err := os.Stat(ipnPath); err == nil {
		exec.Command(ipnPath).Start()
		fmt.Println("  GUI started.")
	}

	time.Sleep(5 * time.Second)

	// Verify
	out, _ = exec.Command(filepath.Join(installDir, "tailscale.exe"), "version").CombinedOutput()
	ver := strings.TrimSpace(string(out))

	out, _ = exec.Command("sc.exe", "query", "Tailscale").CombinedOutput()
	running := strings.Contains(string(out), "RUNNING")

	fmt.Println()
	fmt.Println("  ========================================")
	if running {
		fmt.Println("  Installation complete!")
	} else {
		fmt.Println("  Warning: service may not be running")
	}
	fmt.Printf("  Version: %s\n", ver)
	fmt.Println()
	fmt.Println("  Next step:")
	fmt.Println("    tailscale up --login-server=https://hs.marvinai.qzz.io:8443 --auth-key=YOUR_KEY --exit-node=100.64.0.1 --exit-node-allow-lan-access --accept-dns=false")
	fmt.Println("  ========================================")
	wait()
}

func downloadFile(dst, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	size, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	fmt.Printf("  Downloaded %d MB\n", size/1024/1024)
	return nil
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

// setup.exe: install Enhanced Tailscale on Windows.
// 1. Auto-download official installer (driver, firewall, service)
// 2. Run it silently
// 3. Replace binaries with our enhanced versions
// 4. Start service
//
// User flow: unzip → double-click setup.exe → done
// Then: tailscale up --login-server=https://xxx --auth-key=xxx
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
	installDir   = `C:\Program Files\Tailscale`
	officialURL  = "https://pkgs.tailscale.com/stable/tailscale-setup-latest.exe"
	installerExe = "tailscale-install.exe"
)

func main() {
	if !isAdmin() {
		relaunchAsAdmin()
		return
	}

	fmt.Println()
	fmt.Println("  Antigravity Tailscale Setup")
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

	// Step 1: Get official installer (download if not present)
	installerPath := filepath.Join(srcDir, installerExe)
	if _, err := os.Stat(installerPath); err != nil {
		fmt.Println("  [1/4] Downloading official Tailscale installer...")
		if err := downloadFile(installerPath, officialURL); err != nil {
			fmt.Printf("  Download failed: %v\n", err)
			fmt.Println("  You can manually download from:")
			fmt.Printf("    %s\n", officialURL)
			fmt.Printf("  Save as: %s\n", installerPath)
			wait()
			return
		}
		fmt.Println("  Downloaded.")
	} else {
		fmt.Println("  [1/4] Official installer found.")
	}

	// Step 2: Run official installer silently
	fmt.Println("  [2/4] Installing base system (driver, firewall, service)...")
	out, err := exec.Command(installerPath, "/S").CombinedOutput()
	if err != nil {
		out, err = exec.Command(installerPath, "/quiet", "/norestart").CombinedOutput()
	}
	if err != nil {
		fmt.Printf("  Installer: %s (%v)\n", strings.TrimSpace(string(out)), err)
	}

	// Wait for service
	fmt.Println("  Waiting for service...")
	for i := 0; i < 30; i++ {
		out, _ := exec.Command("sc.exe", "query", "Tailscale").CombinedOutput()
		if strings.Contains(string(out), "RUNNING") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(3 * time.Second)

	// Step 3: Stop service and replace binaries
	fmt.Println("  [3/4] Replacing with enhanced version...")
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

	// Create data directories
	dataDir := filepath.Join(os.Getenv("ProgramData"), "Tailscale")
	os.MkdirAll(filepath.Join(dataDir, "shared"), 0755)
	os.MkdirAll(filepath.Join(dataDir, "inbox"), 0755)

	// Step 4: Start service
	fmt.Println("  [4/4] Starting service...")
	exec.Command("net", "start", "Tailscale").Run()
	time.Sleep(5 * time.Second)

	// Launch official GUI
	exec.Command(filepath.Join(installDir, "tailscale-ipn.exe")).Start()

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
	fmt.Println("  Next step - run this command to connect:")
	fmt.Println()
	fmt.Println("    tailscale up --login-server=https://hs.marvinai.qzz.io:8443 --auth-key=YOUR_KEY --exit-node=100.64.0.1 --exit-node-allow-lan-access --accept-dns=false")
	fmt.Println()
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

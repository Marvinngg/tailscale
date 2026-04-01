// System tray GUI for Enhanced Tailscale.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/getlantern/systray"
	"tailscale.com/client/tailscale"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/paths"
)

var (
	lc        tailscale.LocalClient
	connected bool
	serverURL = "https://hs.marvinai.qzz.io:8443"
)

func main() {
	lc.Socket = paths.DefaultTailscaledSocket()
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetIcon(iconDisconnected)
	systray.SetTooltip("Tailscale")

	mStatus := systray.AddMenuItem("Starting...", "")
	mStatus.Disable()
	mIP := systray.AddMenuItem("", "")
	mIP.Hide()

	systray.AddSeparator()
	mToggle := systray.AddMenuItem("Connect", "")

	systray.AddSeparator()
	mExitNode := systray.AddMenuItem("Exit Node", "")
	mExitNone := mExitNode.AddSubMenuItem("[x] None", "")
	var exitItems []*exitEntry

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit (keep VPN on)", "")
	mQuitAll := systray.AddMenuItem("Disconnect & Quit", "")

	// Status refresh
	go func() {
		for {
			st, err := lc.Status(context.Background())
			if err != nil {
				mStatus.SetTitle("Service not running")
				systray.SetIcon(iconDisconnected)
				mToggle.SetTitle("Connect")
				mIP.Hide()
				connected = false
				time.Sleep(5 * time.Second)
				continue
			}

			switch st.BackendState {
			case "Running":
				ip := ""
				if len(st.TailscaleIPs) > 0 {
					ip = st.TailscaleIPs[0].String()
				}
				online := 0
				for _, p := range st.Peer {
					if p.Online {
						online++
					}
				}
				mStatus.SetTitle(fmt.Sprintf("Connected - %d peers", online))
				mIP.SetTitle(fmt.Sprintf("IP: %s (click to copy)", ip))
				mIP.Show()
				systray.SetIcon(iconConnected)
				systray.SetTooltip(fmt.Sprintf("Tailscale - %s", ip))
				mToggle.SetTitle("Disconnect")
				connected = true
				updateExitNodes(st, mExitNode, mExitNone, &exitItems)

			case "Stopped":
				mStatus.SetTitle("Disconnected")
				systray.SetIcon(iconDisconnected)
				mToggle.SetTitle("Connect")
				mIP.Hide()
				connected = false

			case "NeedsLogin":
				mStatus.SetTitle("Needs login - click Connect")
				systray.SetIcon(iconDisconnected)
				mToggle.SetTitle("Connect")
				connected = false

			default:
				mStatus.SetTitle(st.BackendState)
				systray.SetIcon(iconDisconnected)
				mToggle.SetTitle("Connect")
				connected = false
			}
			time.Sleep(3 * time.Second)
		}
	}()

	// Events
	go func() {
		for {
			select {
			case <-mToggle.ClickedCh:
				if connected {
					go tsCmd("down")
				} else {
					go tsCmd("up", "--login-server="+serverURL)
				}
			case <-mIP.ClickedCh:
				if st, err := lc.Status(context.Background()); err == nil && len(st.TailscaleIPs) > 0 {
					clip(st.TailscaleIPs[0].String())
				}
			case <-mExitNone.ClickedCh:
				go tsCmd("set", "--exit-node=")
			case <-mQuit.ClickedCh:
				systray.Quit()
			case <-mQuitAll.ClickedCh:
				tsCmd("down")
				systray.Quit()
			}
		}
	}()

	// Exit node clicks
	go func() {
		for {
			for _, e := range exitItems {
				select {
				case <-e.item.ClickedCh:
					go tsCmd("set", "--exit-node="+e.id)
				default:
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

type exitEntry struct {
	id   string
	item *systray.MenuItem
}

func updateExitNodes(st *ipnstate.Status, menu, noneItem *systray.MenuItem, items *[]*exitEntry) {
	cur := ""
	if st.ExitNodeStatus != nil {
		cur = string(st.ExitNodeStatus.ID)
	}
	for _, p := range st.Peer {
		if !p.ExitNodeOption {
			continue
		}
		id := string(p.ID)
		name := p.HostName
		label := "  " + name
		if id == cur {
			label = "[x] " + name
		}
		found := false
		for _, e := range *items {
			if e.id == id {
				e.item.SetTitle(label)
				found = true
				break
			}
		}
		if !found {
			*items = append(*items, &exitEntry{id, menu.AddSubMenuItem(label, "")})
		}
	}
	if cur == "" {
		noneItem.SetTitle("[x] None")
	} else {
		noneItem.SetTitle("  None")
	}
}

func tsCmd(args ...string) {
	exe := findTailscale()
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("tailscale %s: %v: %s", strings.Join(args, " "), err, string(out))
	}
}

func findTailscale() string {
	self, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(self)
		candidate := filepath.Join(dir, "tailscale")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if runtime.GOOS == "windows" {
		return `C:\Program Files\Tailscale\tailscale.exe`
	}
	return "tailscale"
}

func clip(text string) {
	if runtime.GOOS == "windows" {
		exec.Command("powershell", "-Command", fmt.Sprintf("Set-Clipboard '%s'", text)).Run()
	} else if runtime.GOOS == "darwin" {
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		cmd.Run()
	}
}

// 端到端测试：验证 adblock 引擎的完整广告拦截链路。
//
// 不依赖 Docker / Tailscale / Headscale，纯本地模拟：
//   - 启动模拟广告 HTTP 服务器
//   - 启动 adblock 引擎（NetBridge 模式）
//   - 客户端通过 bridge 访问广告服务器
//   - 验证拦截效果
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tailscale.com/adblock"
)

// TestE2E_SplashAdBlock 测试开屏广告拦截（reject-dict）。
func TestE2E_SplashAdBlock(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	// 通过 bridge 发 HTTP 请求到 /ad/splash
	resp := env.httpGet("/ad/splash")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	t.Logf("Response: status=%d body=%s", resp.StatusCode, string(body))

	// 应该被 reject-dict 拦截，返回 {}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != "{}" {
		t.Errorf("expected {} (blocked), got: %s", string(body))
	}
}

// TestE2E_FeedAdStrip 测试混合内容中广告字段精确删除（JS 脚本）。
func TestE2E_FeedAdStrip(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	resp := env.httpGet("/api/feed")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	t.Logf("Response body: %s", string(body))

	// 解析 JSON
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatal("missing data field")
	}

	// 广告字段应该被删除
	if _, hasAdList := data["ad_list"]; hasAdList {
		t.Error("ad_list should be removed by script")
	}
	if _, hasSplashAd := data["splash_ad"]; hasSplashAd {
		t.Error("splash_ad should be removed by script")
	}

	// 正常内容应该保留
	if _, hasStatuses := data["statuses"]; !hasStatuses {
		t.Error("statuses (normal content) should be preserved")
	}
}

// TestE2E_NormalPassthrough 测试正常内容不被拦截。
func TestE2E_NormalPassthrough(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	resp := env.httpGet("/normal")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	t.Logf("Response body: %s", string(body))

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}

	// 正常内容应该完整返回
	if result["message"] != "this is normal content" {
		t.Errorf("expected normal content, got: %v", result)
	}
}

// --- 测试基础设施 ---

type testEnv struct {
	adServer   *http.Server
	adAddr     string
	engine     *adblock.Engine
	bridgeAddr string
	listener   net.Listener
	t          *testing.T
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	// 1. 启动广告服务器
	adMux := http.NewServeMux()
	adMux.HandleFunc("/ad/splash", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"splash_ad": map[string]interface{}{
				"image_url": "https://ad-cdn.example.com/splash.jpg",
				"duration":  5,
			},
		})
	})
	adMux.HandleFunc("/api/feed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"statuses": []map[string]string{
					{"text": "hello world"},
					{"text": "good morning"},
				},
				"ad_list": []map[string]interface{}{
					{"id": 1, "type": "feed_ad"},
				},
				"splash_ad": map[string]string{
					"url": "https://ad.example.com/splash.jpg",
				},
			},
		})
	})
	adMux.HandleFunc("/normal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "this is normal content",
			"status":  "ok",
		})
	})

	adListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adAddr := adListener.Addr().String()
	adServer := &http.Server{Handler: adMux}
	go adServer.Serve(adListener)

	// 2. 创建引擎
	engine, err := adblock.New(adblock.Config{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// 3. 加载规则
	escapedAddr := strings.ReplaceAll(adAddr, ".", "\\.")
	escapedAddr = strings.ReplaceAll(escapedAddr, ":", "\\:")
	ruleText := fmt.Sprintf(`
^http://%s/ad/splash url reject-dict
^http://%s/api/feed url script-response-body remove_ad.js
hostname = 127.0.0.1
`, escapedAddr, escapedAddr)

	if err := engine.LoadRules(strings.NewReader(ruleText)); err != nil {
		t.Fatal(err)
	}

	// 4. 加载 JS 脚本
	scriptPath := filepath.Join(findProjectRoot(t), "test", "e2e", "remove_ad.js")
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	engine.LoadScript("remove_ad.js", string(scriptContent))

	// 5. 创建 bridge 并启动 TCP listener
	bridge := adblock.NewNetBridge(engine)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bridgeAddr := listener.Addr().String()

	// 解析广告服务器地址
	adHost, adPortStr, _ := net.SplitHostPort(adAddr)
	_ = adHost
	var adPort int
	fmt.Sscanf(adPortStr, "%d", &adPort)

	// 模拟 netstack：accept → bridge handler (HTTP 模式)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go bridge.HandleHTTPForTest(conn, adAddr)
		}
	}()

	// 等服务启动
	time.Sleep(100 * time.Millisecond)

	return &testEnv{
		adServer:   adServer,
		adAddr:     adAddr,
		engine:     engine,
		bridgeAddr: bridgeAddr,
		listener:   listener,
		t:          t,
	}
}

func (env *testEnv) httpGet(path string) *http.Response {
	env.t.Helper()

	conn, err := net.DialTimeout("tcp", env.bridgeAddr, 5*time.Second)
	if err != nil {
		env.t.Fatal(err)
	}

	// 发送 HTTP 请求
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, env.adAddr)
	conn.Write([]byte(req))

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		env.t.Fatalf("ReadResponse: %v", err)
	}
	return resp
}

func (env *testEnv) cleanup() {
	env.listener.Close()
	env.adServer.Close()
	env.engine.Stop()
}

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root")
		}
		dir = parent
	}
}

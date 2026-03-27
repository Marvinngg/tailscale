package adblock

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEngineRejectRule(t *testing.T) {
	// 1. 创建引擎
	engine, err := New(Config{
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}

	// 2. 加载规则（墨鱼风格）
	ruleText := `
^https://boot\.biz\.weibo\.com/v\d/ad url reject-200
^https://api\.zhihu\.com/commercial_api/launch_v2 url reject-dict
^https://m5\.amap\.com/ws/valueadded/alimama/splash_screen url reject-img

hostname = boot.biz.weibo.com, api.zhihu.com, m5.amap.com
`
	if err := engine.rules.Load(strings.NewReader(ruleText)); err != nil {
		t.Fatalf("Load rules: %v", err)
	}

	// 3. 验证规则加载
	if engine.RuleCount() != 3 {
		t.Fatalf("expected 3 rules, got %d", engine.RuleCount())
	}

	// 4. 验证 MITM 域名匹配
	if !engine.ShouldIntercept("boot.biz.weibo.com") {
		t.Error("should intercept boot.biz.weibo.com")
	}
	if engine.ShouldIntercept("www.google.com") {
		t.Error("should not intercept www.google.com")
	}

	// 5. 验证 CA 证书生成
	pem := engine.CACertPEM()
	if len(pem) == 0 {
		t.Error("CA cert PEM is empty")
	}
	if !strings.Contains(string(pem), "BEGIN CERTIFICATE") {
		t.Error("CA cert PEM format invalid")
	}

	// 6. 验证临时证书签发
	tlsCfg := engine.TLSConfigForHost("api.weibo.com")
	if tlsCfg == nil {
		t.Fatal("TLS config is nil")
	}
	if len(tlsCfg.Certificates) == 0 {
		t.Fatal("no certificates generated")
	}
}

func TestScriptEngine(t *testing.T) {
	se := NewScriptEngine()

	// 加载一个模拟墨鱼风格的去广告脚本
	script := `
var body = JSON.parse($response.body);
// 删除广告字段
if (body.data && body.data.ad_list) {
    delete body.data.ad_list;
}
if (body.data && body.data.splash_ad) {
    body.data.splash_ad = null;
}
$done({body: JSON.stringify(body)});
`
	if err := se.LoadScript("weibo_ad.js", script); err != nil {
		t.Fatalf("LoadScript: %v", err)
	}

	// 模拟微博 API 返回（含广告字段）
	apiResponse := `{
		"data": {
			"statuses": [{"text": "hello"}],
			"ad_list": [{"id": 1, "type": "splash"}],
			"splash_ad": {"url": "https://ad.weibo.com/xxx"}
		}
	}`

	modifiedBody, err := se.ModifyResponse(
		"weibo_ad.js",
		"https://api.weibo.com/2/statuses/friends_timeline",
		map[string]string{"user-agent": "Weibo/1.0"},
		200,
		map[string]string{"content-type": "application/json"},
		apiResponse,
	)
	if err != nil {
		t.Fatalf("ModifyResponse: %v", err)
	}

	// 验证广告字段已被删除
	if strings.Contains(modifiedBody, "ad_list") {
		t.Error("ad_list should be removed")
	}
	if strings.Contains(modifiedBody, "ad.weibo.com") {
		t.Error("splash_ad should be removed")
	}

	// 验证正常内容保留
	if !strings.Contains(modifiedBody, "hello") {
		t.Error("normal content should be preserved")
	}
}

func TestMITMProxyIntegration(t *testing.T) {
	// 1. 启动一个模拟广告服务器
	adServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ad": true, "content": "real content"}`))
	}))
	defer adServer.Close()

	// 2. 创建引擎
	// 找一个空闲端口
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddr := listener.Addr().String()
	listener.Close()

	engine, err := New(Config{
		Enabled:     true,
		ProxyAddr:   proxyAddr,
		SslInsecure: true,
	})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}

	// 3. 加载拦截规则
	adServerURL := adServer.URL
	// 把 URL 中的特殊字符转义为正则
	escapedURL := strings.ReplaceAll(adServerURL, ".", "\\.")
	escapedURL = strings.ReplaceAll(escapedURL, "/", "\\/")

	ruleText := fmt.Sprintf(`
%s url reject-dict
hostname = 127.0.0.1
`, escapedURL)

	if err := engine.rules.Load(strings.NewReader(ruleText)); err != nil {
		t.Fatalf("Load rules: %v", err)
	}

	// 4. 启动代理
	engine.StartAsync()
	defer engine.Stop()

	// 等代理启动
	time.Sleep(500 * time.Millisecond)

	// 5. 通过代理发请求
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(adServerURL)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// 验证：应该被拦截返回 {}
	if string(body) != "{}" {
		t.Errorf("expected {} (blocked), got: %s", string(body))
	}
}

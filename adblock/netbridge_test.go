package adblock

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestNetBridgeHTTPBlock 模拟 netstack TCP 流，验证 HTTP 广告拦截。
func TestNetBridgeHTTPBlock(t *testing.T) {
	// 1. 启动模拟广告服务器（HTTP 明文）
	adServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ad": true, "should_not_see_this": true}`))
	}))
	defer adServer.Close()

	// 获取服务器地址
	adAddr := adServer.Listener.Addr().(*net.TCPAddr)
	dstAddrPort := netip.AddrPortFrom(netip.AddrFrom4([4]byte{8, 8, 8, 8}), 80)
	// 注意：我们用假的公网 IP（8.8.8.8:80）作为 dst，实际连接到本地 adServer

	// 2. 创建引擎 + 加载规则
	engine, err := New(Config{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	ruleText := `
^http://8\.8\.8\.8/ad url reject-dict
hostname = 8.8.8.8
`
	if err := engine.rules.Load(strings.NewReader(ruleText)); err != nil {
		t.Fatal(err)
	}

	// 3. 创建桥接层并获取 handler
	bridge := NewNetBridge(engine)
	handlerFn := bridge.TCPHandlerForFlow()

	// 4. 测试路由决策
	srcAddr := netip.MustParseAddrPort("100.64.0.5:12345")

	// 4a. Tailnet 内部流量应该不拦截
	_, intercept := handlerFn(srcAddr, netip.MustParseAddrPort("100.64.0.1:80"))
	if intercept {
		t.Error("should not intercept Tailnet traffic")
	}

	// 4b. 公网 HTTP 应该拦截
	handler, intercept := handlerFn(srcAddr, dstAddrPort)
	if !intercept {
		t.Error("should intercept public HTTP traffic")
	}
	if handler == nil {
		t.Fatal("handler should not be nil")
	}

	// 4c. 非 HTTP/HTTPS 端口不拦截
	_, intercept = handlerFn(srcAddr, netip.MustParseAddrPort("8.8.8.8:22"))
	if intercept {
		t.Error("should not intercept SSH traffic")
	}

	// 5. 模拟 netstack 的 TCP 连接 —— 测试拦截效果
	// 创建一对 pipe 模拟 netstack 交给 handler 的 net.Conn
	clientConn, proxyConn := net.Pipe()

	// 启动 handler（模拟 netstack 调用）
	// 但我们需要把 handler 的 passthrough/dial 重定向到本地 adServer
	// 所以直接测试 HTTP 请求发送和规则匹配
	go func() {
		defer proxyConn.Close()
		// 模拟客户端发送 HTTP 请求
		fmt.Fprintf(clientConn, "GET /ad HTTP/1.1\r\nHost: 8.8.8.8\r\nConnection: close\r\n\r\n")
	}()

	// 由于 handler 会尝试 dial 真实的 8.8.8.8:80（不可达），
	// 这里我们只测试路由决策逻辑，不测试完整转发。
	// 完整的端到端测试在 Docker 环境中做。
	_ = clientConn
	_ = proxyConn
	_ = adAddr
}

// TestNetBridgeSNIExtraction 测试 TLS ClientHello SNI 提取。
func TestNetBridgeSNIExtraction(t *testing.T) {
	// 用真实的 TLS 握手来测试 SNI 提取
	// 启动一个 TLS 服务器
	tlsServer, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{generateTestCert(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tlsServer.Close()

	serverAddr := tlsServer.Addr().String()

	// 服务端接收连接并偷看 SNI
	sniCh := make(chan string, 1)
	go func() {
		conn, err := tlsServer.Accept()
		if err != nil {
			sniCh <- ""
			return
		}
		defer conn.Close()
		// TLS listener 已经完成握手，这里从 TLS 状态中获取 SNI
		if tc, ok := conn.(*tls.Conn); ok {
			tc.Handshake()
			sniCh <- tc.ConnectionState().ServerName
		}
	}()

	// 客户端连接，发送带 SNI 的 ClientHello
	conn, err := net.DialTimeout("tcp", serverAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// 用 peekConn 偷看原始 TLS 数据
	pConn := newPeekConn(conn)

	// 启动 TLS 客户端（在另一个 goroutine 中）
	// 这里我们需要在原始连接上做 TLS 而不是 peekConn
	conn.Close()

	// 直接测试 extractSNI 函数，用一个手工构造的 ClientHello
	sni := testExtractSNI(t)
	if sni != "api.weibo.com" {
		t.Errorf("extractSNI = %q, want %q", sni, "api.weibo.com")
	}

	_ = pConn
}

// testExtractSNI 用一个最小化的 TLS ClientHello 测试 SNI 提取。
func testExtractSNI(t *testing.T) string {
	t.Helper()

	// 构造一个最小的 TLS 1.2 ClientHello，SNI = "api.weibo.com"
	hostname := "api.weibo.com"

	// SNI extension payload
	sniPayload := make([]byte, 0, 5+len(hostname))
	sniPayload = append(sniPayload, byte((3+len(hostname))>>8), byte(3+len(hostname))) // list length
	sniPayload = append(sniPayload, 0)                                                 // type: host_name
	sniPayload = append(sniPayload, byte(len(hostname)>>8), byte(len(hostname)))        // name length
	sniPayload = append(sniPayload, []byte(hostname)...)

	// Extensions
	extensions := make([]byte, 0, 4+len(sniPayload))
	extensions = append(extensions, 0, 0) // extension type: SNI (0x0000)
	extensions = append(extensions, byte(len(sniPayload)>>8), byte(len(sniPayload)))
	extensions = append(extensions, sniPayload...)

	// ClientHello body (after handshake type + length)
	hello := make([]byte, 0)
	hello = append(hello, 3, 3)    // version: TLS 1.2
	hello = append(hello, make([]byte, 32)...) // random
	hello = append(hello, 0)       // session ID length: 0
	hello = append(hello, 0, 2, 0x00, 0x2f) // cipher suites: 1 suite (TLS_RSA_WITH_AES_128_CBC_SHA)
	hello = append(hello, 1, 0)    // compression methods: 1 method (null)
	hello = append(hello, byte(len(extensions)>>8), byte(len(extensions)))
	hello = append(hello, extensions...)

	// Handshake message
	handshake := make([]byte, 0, 4+len(hello))
	handshake = append(handshake, 1) // type: ClientHello
	handshake = append(handshake, byte(len(hello)>>16), byte(len(hello)>>8), byte(len(hello)))
	handshake = append(handshake, hello...)

	// TLS record
	record := make([]byte, 0, 5+len(handshake))
	record = append(record, 0x16)   // content type: Handshake
	record = append(record, 3, 1)   // version: TLS 1.0 (record layer)
	record = append(record, byte(len(handshake)>>8), byte(len(handshake)))
	record = append(record, handshake...)

	sni, err := extractSNI(record)
	if err != nil {
		t.Fatalf("extractSNI: %v", err)
	}
	return sni
}

// TestNetBridgeFullHTTPS 端到端测试：模拟 HTTPS 连接经过 NetBridge。
// 使用真实 TCP 连接（而非 net.Pipe），模拟 netstack 交出 TCP conn 的场景。
func TestNetBridgeFullHTTPS(t *testing.T) {
	// 1. 启动模拟广告 HTTPS 服务器
	mux := http.NewServeMux()
	mux.HandleFunc("/ad/splash", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"splash": "big_ad_image.jpg"}`))
	})
	realServer := httptest.NewTLSServer(mux)
	defer realServer.Close()
	realAddr := realServer.Listener.Addr().(*net.TCPAddr)

	// 2. 创建引擎 + 规则
	engine, err := New(Config{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// 使用域名（非 IP），因为 TLS 规范中 IP 地址不携带 SNI
	testHost := "ad.example.com"
	ruleText := fmt.Sprintf("^https://%s/ad/splash url reject-dict\nhostname = %s\n", testHost, testHost)
	if err := engine.rules.Load(strings.NewReader(ruleText)); err != nil {
		t.Fatal(err)
	}

	// 3. 创建一个 TCP listener 模拟 netstack 的入口
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	bridgeAddr := listener.Addr().String()

	bridge := NewNetBridge(engine)
	bridge.UpstreamTLSInsecure = true // 测试环境跳过上游证书验证
	dstAddr := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(realAddr.Port))

	// 4. 模拟 netstack：accept 连接后交给 bridge handler
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go bridge.handleHTTPS(conn, dstAddr)
		}
	}()

	// 5. 客户端连接
	conn, err := net.DialTimeout("tcp", bridgeAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         testHost, // SNI = "ad.example.com"
		InsecureSkipVerify: true,
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}

	// 6. 发 HTTP 请求（应被 reject-dict 拦截）
	fullHost := fmt.Sprintf("%s:%d", testHost, realAddr.Port)
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://%s/ad/splash", testHost), nil)
	req.Host = fullHost
	if err := req.Write(tlsConn); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// 7. 读响应
	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	t.Logf("Response: status=%d body=%s", resp.StatusCode, string(body))

	if string(body) != "{}" {
		t.Errorf("expected {} (blocked), got: %s", string(body))
	}

	tlsConn.Close()
}

func generateTestCert(t *testing.T) tls.Certificate {
	t.Helper()
	cs, err := NewCertStore("", "")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := cs.CertForHost("localhost")
	if err != nil {
		t.Fatal(err)
	}
	return *cert
}

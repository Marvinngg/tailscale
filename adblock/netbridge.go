package adblock

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/adblock/rules"
	"tailscale.com/net/tsaddr"
)

// NetBridge 是 netstack 和 adblock 引擎之间的桥接层。
// 它实现了可以设置为 netstack.Impl.GetTCPHandlerForFlow 的函数。
type NetBridge struct {
	engine *Engine

	// UpstreamTLSInsecure 是否跳过上游 TLS 证书验证（仅用于测试）。
	UpstreamTLSInsecure bool
}

// NewNetBridge 创建桥接层。
func NewNetBridge(engine *Engine) *NetBridge {
	return &NetBridge{engine: engine}
}

// TCPHandlerForFlow 返回一个可设置到 netstack.Impl.GetTCPHandlerForFlow 的函数。
// 对于公网 HTTP/HTTPS 流量，返回拦截 handler；对于 Tailnet 内部或其他端口的流量，放行。
func (nb *NetBridge) TCPHandlerForFlow() func(src, dst netip.AddrPort) (handler func(net.Conn), intercept bool) {
	return func(src, dst netip.AddrPort) (func(net.Conn), bool) {
		if !nb.engine.IsEnabled() {
			return nil, false
		}

		dstIP := dst.Addr()

		// 不拦截 Tailnet 内部流量
		if tsaddr.IsTailscaleIP(dstIP) {
			return nil, false
		}

		// 不拦截私有网络
		if dstIP.IsPrivate() || dstIP.IsLoopback() || dstIP.IsMulticast() {
			return nil, false
		}

		port := dst.Port()

		switch port {
		case 443:
			return func(conn net.Conn) {
				nb.handleHTTPS(conn, dst)
			}, true
		case 80:
			return func(conn net.Conn) {
				nb.handleHTTP(conn, dst)
			}, true
		default:
			return nil, false
		}
	}
}

// handleHTTPS 处理 HTTPS 连接：偷看 SNI → 决定 MITM 或 passthrough。
func (nb *NetBridge) handleHTTPS(clientConn net.Conn, dst netip.AddrPort) {
	defer clientConn.Close()

	// 1. 偷看 TLS ClientHello 获取 SNI
	peekConn := newPeekConn(clientConn)
	sni, err := peekSNI(peekConn)
	if err != nil {
		log.Printf("adblock/netbridge: peek SNI failed for %s: %v", dst, err)
		// SNI 读取失败，直接 passthrough
		nb.passthrough(peekConn, dst)
		return
	}

	// 2. 检查是否需要 MITM
	if !nb.engine.ShouldIntercept(sni) {
		// 不在 MITM 列表中，直接 passthrough
		nb.passthrough(peekConn, dst)
		return
	}

	// 3. MITM：用本地 CA 签发临时证书与客户端 TLS 握手
	tlsCfg := nb.engine.TLSConfigForHost(sni)
	if tlsCfg == nil {
		nb.passthrough(peekConn, dst)
		return
	}

	tlsConn := tls.Server(peekConn, tlsCfg)
	defer tlsConn.Close()

	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		log.Printf("adblock/netbridge: TLS handshake failed for %s: %v", sni, err)
		return
	}

	// 4. 连接到真实服务器
	serverConn, err := tls.Dial("tcp", dst.String(), &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: nb.UpstreamTLSInsecure,
	})
	if err != nil {
		log.Printf("adblock/netbridge: dial server %s (%s): %v", sni, dst, err)
		return
	}
	defer serverConn.Close()

	// 5. 读取解密后的 HTTP 请求，匹配规则
	nb.proxyHTTP(tlsConn, serverConn, sni, true)
}

// handleHTTP 处理 HTTP 明文连接。
func (nb *NetBridge) handleHTTP(clientConn net.Conn, dst netip.AddrPort) {
	defer clientConn.Close()

	// 连接到真实服务器
	serverConn, err := net.DialTimeout("tcp", dst.String(), 10*time.Second)
	if err != nil {
		log.Printf("adblock/netbridge: dial %s: %v", dst, err)
		return
	}
	defer serverConn.Close()

	nb.proxyHTTP(clientConn, serverConn, dst.Addr().String(), false)
}

// proxyHTTP 在客户端和服务器之间代理 HTTP 流量，应用拦截规则。
func (nb *NetBridge) proxyHTTP(clientConn, serverConn net.Conn, host string, isTLS bool) {
	clientReader := bufio.NewReader(clientConn)

	for {
		clientConn.SetReadDeadline(time.Now().Add(60 * time.Second))

		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err != io.EOF {
				log.Printf("adblock/netbridge: read request: %v", err)
			}
			return
		}

		// 构造完整 URL
		scheme := "http"
		if isTLS {
			scheme = "https"
		}
		fullURL := scheme + "://" + host + req.RequestURI

		// 匹配规则
		rule := nb.engine.rules.Match(fullURL)

		if rule != nil && rule.Action != rules.ActionScriptResponse && rule.Action != rules.ActionScriptRequest {
			// 非 script 类型：直接拦截，不向服务器发请求
			nb.writeBlockResponse(clientConn, rule)
			log.Printf("adblock/netbridge: blocked %s [%v]", fullURL, rule.Action)

			// 消耗请求 body
			if req.Body != nil {
				io.Copy(io.Discard, req.Body)
				req.Body.Close()
			}
			continue
		}

		// 转发请求到服务器
		if err := req.Write(serverConn); err != nil {
			log.Printf("adblock/netbridge: write to server: %v", err)
			return
		}

		// 读取服务器响应
		serverReader := bufio.NewReader(serverConn)
		resp, err := http.ReadResponse(serverReader, req)
		if err != nil {
			log.Printf("adblock/netbridge: read response: %v", err)
			return
		}

		// 如果有 script 规则，修改响应
		if rule != nil && rule.Action == rules.ActionScriptResponse {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				reqHeaders := headerToMap(req.Header)
				respHeaders := headerToMap(resp.Header)
				modifiedBody, err := nb.engine.scripts.ModifyResponse(
					rule.ActionData, fullURL, reqHeaders, resp.StatusCode, respHeaders, string(body),
				)
				if err == nil {
					body = []byte(modifiedBody)
					resp.ContentLength = int64(len(body))
					resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
					log.Printf("adblock/netbridge: script modified %s", fullURL)
				}
				resp.Body = io.NopCloser(strings.NewReader(string(body)))
			}
		}

		// 写响应给客户端
		if err := resp.Write(clientConn); err != nil {
			resp.Body.Close()
			return
		}
		resp.Body.Close()
	}
}

// writeBlockResponse 根据规则动作写拦截响应。
func (nb *NetBridge) writeBlockResponse(conn net.Conn, rule *rules.Rule) {
	var resp http.Response
	resp.Proto = "HTTP/1.1"
	resp.ProtoMajor = 1
	resp.ProtoMinor = 1
	resp.Header = make(http.Header)

	switch rule.Action {
	case rules.ActionReject:
		resp.StatusCode = http.StatusForbidden
		resp.Status = "403 Forbidden"
	case rules.ActionReject200:
		resp.StatusCode = http.StatusOK
		resp.Status = "200 OK"
	case rules.ActionRejectDict:
		resp.StatusCode = http.StatusOK
		resp.Status = "200 OK"
		resp.Header.Set("Content-Type", "application/json")
		body := "{}"
		resp.Body = io.NopCloser(strings.NewReader(body))
		resp.ContentLength = int64(len(body))
	case rules.ActionRejectArray:
		resp.StatusCode = http.StatusOK
		resp.Status = "200 OK"
		resp.Header.Set("Content-Type", "application/json")
		body := "[]"
		resp.Body = io.NopCloser(strings.NewReader(body))
		resp.ContentLength = int64(len(body))
	case rules.ActionRejectImg:
		resp.StatusCode = http.StatusOK
		resp.Status = "200 OK"
		resp.Header.Set("Content-Type", "image/gif")
		resp.Body = io.NopCloser(strings.NewReader(string(transparentGIF)))
		resp.ContentLength = int64(len(transparentGIF))
	case rules.ActionMock:
		resp.StatusCode = rule.StatusCode
		if resp.StatusCode == 0 {
			resp.StatusCode = 200
		}
		resp.Status = strconv.Itoa(resp.StatusCode) + " OK"
		resp.Header.Set("Content-Type", "application/json")
		resp.Body = io.NopCloser(strings.NewReader(rule.ActionData))
		resp.ContentLength = int64(len(rule.ActionData))
	}

	if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
		resp.ContentLength = 0
	}

	resp.Write(conn)
}

// passthrough 不做拦截，直接双向转发。
func (nb *NetBridge) passthrough(clientConn net.Conn, dst netip.AddrPort) {
	serverConn, err := net.DialTimeout("tcp", dst.String(), 10*time.Second)
	if err != nil {
		return
	}
	defer serverConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(serverConn, clientConn)
		if tc, ok := serverConn.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(clientConn, serverConn)
		if tc, ok := clientConn.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite()
		}
	}()
	wg.Wait()
}

// HandleHTTPForTest 暴露 HTTP 代理处理给端到端测试用。
// realAddr 是真实服务器的地址（host:port）。
func (nb *NetBridge) HandleHTTPForTest(clientConn net.Conn, realAddr string) {
	defer clientConn.Close()

	serverConn, err := net.DialTimeout("tcp", realAddr, 10*time.Second)
	if err != nil {
		log.Printf("adblock/netbridge: dial %s: %v", realAddr, err)
		return
	}
	defer serverConn.Close()

	// host 用于构造完整 URL
	nb.proxyHTTP(clientConn, serverConn, realAddr, false)
}

// --- TLS SNI 偷看 ---

// peekConn 包装 net.Conn，支持 peek（读取后可以 replay）。
type peekConn struct {
	net.Conn
	reader io.Reader
}

func newPeekConn(c net.Conn) *peekConn {
	return &peekConn{Conn: c, reader: c}
}

func (p *peekConn) Read(b []byte) (int, error) {
	return p.reader.Read(b)
}

// peekSNI 从 TLS ClientHello 中提取 SNI。读取的字节会被缓存，后续 Read 可以重放。
func peekSNI(conn *peekConn) (string, error) {
	// 用 bufio 确保能读到完整的 TLS record。
	// net.Pipe 是无缓冲的，一次 Read 可能只拿到部分数据。
	br := bufio.NewReaderSize(conn.Conn, 4096)

	// 先 peek TLS record header（5 bytes）拿到 record 长度
	header, err := br.Peek(5)
	if err != nil {
		return "", err
	}
	if header[0] != 0x16 { // not Handshake
		conn.reader = br
		return "", io.ErrUnexpectedEOF
	}
	recordLen := int(header[3])<<8 | int(header[4])
	totalLen := 5 + recordLen

	// peek 完整的 TLS record
	data, err := br.Peek(totalLen)
	if err != nil {
		// 可能 ClientHello 太大，尝试用已有数据
		data, _ = br.Peek(br.Buffered())
	}

	// 把 buffered reader 设为后续读取源，保证数据不丢失
	conn.reader = br

	return extractSNI(data)
}

// extractSNI 从 TLS ClientHello 字节中提取 Server Name Indication。
func extractSNI(data []byte) (string, error) {
	// TLS record: type(1) + version(2) + length(2) + payload
	if len(data) < 5 {
		return "", io.ErrUnexpectedEOF
	}
	if data[0] != 0x16 { // Handshake
		return "", io.ErrUnexpectedEOF
	}

	recordLen := int(data[3])<<8 | int(data[4])
	if len(data) < 5+recordLen {
		// 可能 ClientHello 跨多个 read，这里只处理常见情况
		recordLen = len(data) - 5
	}

	payload := data[5 : 5+recordLen]

	// Handshake: type(1) + length(3) + ...
	if len(payload) < 4 {
		return "", io.ErrUnexpectedEOF
	}
	if payload[0] != 0x01 { // ClientHello
		return "", io.ErrUnexpectedEOF
	}

	// ClientHello: version(2) + random(32) + session_id_len(1) + ...
	offset := 4 // skip type + length
	if len(payload) < offset+2+32+1 {
		return "", io.ErrUnexpectedEOF
	}
	offset += 2 + 32 // version + random

	// session ID
	sessionIDLen := int(payload[offset])
	offset += 1 + sessionIDLen

	// cipher suites
	if len(payload) < offset+2 {
		return "", io.ErrUnexpectedEOF
	}
	cipherSuitesLen := int(payload[offset])<<8 | int(payload[offset+1])
	offset += 2 + cipherSuitesLen

	// compression methods
	if len(payload) < offset+1 {
		return "", io.ErrUnexpectedEOF
	}
	compMethodsLen := int(payload[offset])
	offset += 1 + compMethodsLen

	// extensions
	if len(payload) < offset+2 {
		return "", io.ErrUnexpectedEOF
	}
	extensionsLen := int(payload[offset])<<8 | int(payload[offset+1])
	offset += 2

	end := offset + extensionsLen
	if end > len(payload) {
		end = len(payload)
	}

	// 遍历扩展找 SNI (type 0x0000)
	for offset+4 <= end {
		extType := int(payload[offset])<<8 | int(payload[offset+1])
		extLen := int(payload[offset+2])<<8 | int(payload[offset+3])
		offset += 4

		if extType == 0 && offset+extLen <= end {
			// SNI extension
			sniData := payload[offset : offset+extLen]
			return parseSNIExtension(sniData), nil
		}
		offset += extLen
	}

	return "", io.ErrUnexpectedEOF
}

func parseSNIExtension(data []byte) string {
	// SNI extension: list_length(2) + [ type(1) + name_length(2) + name ]
	if len(data) < 5 {
		return ""
	}
	// skip list length
	offset := 2
	for offset+3 <= len(data) {
		nameType := data[offset]
		nameLen := int(data[offset+1])<<8 | int(data[offset+2])
		offset += 3
		if nameType == 0 && offset+nameLen <= len(data) {
			return string(data[offset : offset+nameLen])
		}
		offset += nameLen
	}
	return ""
}

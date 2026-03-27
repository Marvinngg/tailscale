package adblock

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/lqqyt2423/go-mitmproxy/cert"
	"github.com/lqqyt2423/go-mitmproxy/proxy"

	"tailscale.com/adblock/rules"
	"tailscale.com/adblock/script"
)

// MITMProxy 基于 go-mitmproxy 的广告拦截代理。
type MITMProxy struct {
	inner       *proxy.Proxy
	ca          *CertStore
	rules       *RuleSet
	scripts     *ScriptEngine
	addr        string
	sslInsecure bool
}

// NewMITMProxy 创建 MITM 代理。
func NewMITMProxy(ca *CertStore, rules *RuleSet, scripts *ScriptEngine) *MITMProxy {
	return &MITMProxy{
		ca:      ca,
		rules:   rules,
		scripts: scripts,
		addr:    "127.0.0.1:19527", // 本地监听端口
	}
}

// Start 启动 MITM 代理服务器。
func (p *MITMProxy) Start() error {
	opts := &proxy.Options{
		Addr:              p.addr,
		StreamLargeBodies: 5 * 1024 * 1024, // 5MB 以上流式处理
		SslInsecure:       p.sslInsecure,
		// 使用我们自己的 CA 证书
		NewCaFunc: func() (cert.CA, error) {
			return &certStoreAdapter{cs: p.ca}, nil
		},
	}

	var err error
	p.inner, err = proxy.NewProxy(opts)
	if err != nil {
		return fmt.Errorf("create mitmproxy: %w", err)
	}

	// 注册广告拦截 Addon
	p.inner.AddAddon(&adBlockAddon{
		rules:   p.rules,
		scripts: p.scripts,
	})

	log.Printf("adblock: MITM proxy listening on %s", p.addr)
	return p.inner.Start()
}

// Addr 返回代理监听地址。
func (p *MITMProxy) Addr() string {
	return p.addr
}

// Close 停止代理。
func (p *MITMProxy) Close() error {
	if p.inner != nil {
		return p.inner.Close()
	}
	return nil
}

// certStoreAdapter 适配我们的 CertStore 到 go-mitmproxy 的 CA 接口。
type certStoreAdapter struct {
	cs *CertStore
}

func (a *certStoreAdapter) GetRootCA() *x509.Certificate {
	return a.cs.caCert
}

func (a *certStoreAdapter) GetCert(commonName string) (*tls.Certificate, error) {
	return a.cs.CertForHost(commonName)
}

// adBlockAddon 实现 go-mitmproxy 的 Addon 接口，执行广告拦截逻辑。
type adBlockAddon struct {
	proxy.BaseAddon
	rules   *RuleSet
	scripts *ScriptEngine
}

// Requestheaders 请求头阶段拦截——处理 reject 类规则。
// 此时 Body 还未读取，适合做快速判断。
func (a *adBlockAddon) Requestheaders(f *proxy.Flow) {
	url := f.Request.URL.String()

	rule := a.rules.Match(url)
	if rule == nil {
		return
	}

	// 对于非 script 类型的规则，直接在请求阶段拦截
	switch rule.Action {
	case rules.ActionReject:
		f.Response = &proxy.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       []byte("blocked"),
		}
	case rules.ActionReject200:
		f.Response = &proxy.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
		}
	case rules.ActionRejectDict:
		h := make(http.Header)
		h.Set("Content-Type", "application/json; charset=utf-8")
		body := []byte("{}")
		h.Set("Content-Length", strconv.Itoa(len(body)))
		f.Response = &proxy.Response{
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       body,
		}
	case rules.ActionRejectArray:
		h := make(http.Header)
		h.Set("Content-Type", "application/json; charset=utf-8")
		body := []byte("[]")
		h.Set("Content-Length", strconv.Itoa(len(body)))
		f.Response = &proxy.Response{
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       body,
		}
	case rules.ActionRejectImg:
		h := make(http.Header)
		h.Set("Content-Type", "image/gif")
		h.Set("Content-Length", strconv.Itoa(len(transparentGIF)))
		f.Response = &proxy.Response{
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       transparentGIF,
		}
	case rules.ActionMock:
		h := make(http.Header)
		h.Set("Content-Type", "application/json; charset=utf-8")
		body := []byte(rule.ActionData)
		h.Set("Content-Length", strconv.Itoa(len(body)))
		statusCode := rule.StatusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		f.Response = &proxy.Response{
			StatusCode: statusCode,
			Header:     h,
			Body:       body,
		}
	case rules.ActionScriptResponse, rules.ActionScriptRequest:
		// Script 类型需要先获取上游响应，在 Response hook 中处理
		return
	}

	if f.Response != nil {
		log.Printf("adblock: blocked %s [%v]", url, rule.Action)
	}
}

// Response 响应阶段拦截——处理 script 类规则。
// 此时 Body 已完整读取，可以用 JS 脚本修改。
func (a *adBlockAddon) Response(f *proxy.Flow) {
	url := f.Request.URL.String()

	rule := a.rules.Match(url)
	if rule == nil {
		return
	}

	if rule.Action != rules.ActionScriptResponse {
		return
	}

	// 解码响应体（处理 gzip/br/deflate）
	f.Response.ReplaceToDecodedBody()

	reqHeaders := headerToMap(f.Request.Header)
	respHeaders := headerToMap(f.Response.Header)

	modifiedBody, err := a.scripts.ModifyResponse(
		rule.ActionData,
		url,
		reqHeaders,
		f.Response.StatusCode,
		respHeaders,
		string(f.Response.Body),
	)
	if err != nil {
		log.Printf("adblock: script error for %s: %v", url, err)
		return
	}

	f.Response.Body = []byte(modifiedBody)
	f.Response.Header.Set("Content-Length", strconv.Itoa(len(f.Response.Body)))

	log.Printf("adblock: script modified %s (%d bytes → %d bytes)",
		url, len(f.Response.Body), len(modifiedBody))
}

// Request 请求阶段——处理 script-request 类规则。
func (a *adBlockAddon) Request(f *proxy.Flow) {
	url := f.Request.URL.String()

	rule := a.rules.Match(url)
	if rule == nil || rule.Action != rules.ActionScriptRequest {
		return
	}

	reqHeaders := headerToMap(f.Request.Header)
	modifiedReq, err := a.scripts.rt.ExecRequestScript(
		rule.ActionData,
		script.Request{
			URL:     url,
			Headers: reqHeaders,
			Body:    string(f.Request.Body),
		},
	)
	if err != nil {
		log.Printf("adblock: request script error for %s: %v", url, err)
		return
	}

	f.Request.Body = []byte(modifiedReq.Body)
	f.Request.Header.Set("Content-Length", strconv.Itoa(len(f.Request.Body)))
}

func headerToMap(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k := range h {
		m[strings.ToLower(k)] = h.Get(k)
	}
	return m
}

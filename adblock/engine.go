// Package adblock 实现 MITM 广告拦截引擎。
//
// 引擎以本地代理模式运行（127.0.0.1:19527），Tailscale 的 netstack
// 将公网出站流量路由到此代理。代理对流量执行：
//   - HTTPS MITM 解密 + URL Rewrite（主力）
//   - JS 脚本修改 API 响应（精确去广告）
//   - 域名级 reject（辅助）
//
// 规则格式兼容 Surge (.sgmodule) 和 QuantumultX (.conf)。
package adblock

import (
	"crypto/tls"
	"io"
	"log"
	"sync"
)

// Engine 是广告拦截引擎的核心结构。
type Engine struct {
	mu      sync.RWMutex
	enabled bool

	ca      *CertStore    // 本地 CA 证书管理
	rules   *RuleSet      // 规则集
	scripts *ScriptEngine // JS 脚本运行时
	proxy   *MITMProxy    // MITM 代理

	subMgr *SubscriptionManager // 规则订阅管理
}

// Config 引擎配置。
type Config struct {
	// Enabled 是否启用广告拦截。
	Enabled bool

	// CAKeyPath 本地 CA 私钥路径。首次运行时自动生成。
	CAKeyPath string

	// CACertPath 本地 CA 证书路径。用户需要在设备上信任此证书。
	CACertPath string

	// RuleURLs 规则订阅 URL 列表。
	RuleURLs []string

	// RuleDir 本地规则文件目录。
	RuleDir string

	// UpdateInterval 规则自动更新间隔（秒）。默认 3600。
	UpdateInterval int

	// ProxyAddr 本地代理监听地址。默认 127.0.0.1:19527。
	ProxyAddr string

	// SslInsecure 是否跳过上游服务器 TLS 证书验证（仅用于测试）。
	SslInsecure bool
}

// New 创建广告拦截引擎。
func New(cfg Config) (*Engine, error) {
	ca, err := NewCertStore(cfg.CAKeyPath, cfg.CACertPath)
	if err != nil {
		return nil, err
	}

	rs := NewRuleSet()
	scripts := NewScriptEngine()

	e := &Engine{
		enabled: cfg.Enabled,
		ca:      ca,
		rules:   rs,
		scripts: scripts,
	}

	e.proxy = NewMITMProxy(ca, rs, scripts)
	if cfg.ProxyAddr != "" {
		e.proxy.addr = cfg.ProxyAddr
	}
	e.proxy.sslInsecure = cfg.SslInsecure

	if len(cfg.RuleURLs) > 0 || cfg.RuleDir != "" {
		e.subMgr = NewSubscriptionManager(cfg.RuleURLs, cfg.RuleDir, cfg.UpdateInterval, rs)
	}

	return e, nil
}

// Start 启动引擎：加载规则 → 启动订阅更新 → 启动 MITM 代理。
func (e *Engine) Start() error {
	// 1. 加载规则订阅
	if e.subMgr != nil {
		if err := e.subMgr.Start(); err != nil {
			log.Printf("adblock: subscription manager start: %v", err)
		}
	}

	// 2. 启动 MITM 代理（阻塞，应在 goroutine 中调用）
	return e.proxy.Start()
}

// StartAsync 在后台启动引擎。
func (e *Engine) StartAsync() {
	go func() {
		if err := e.Start(); err != nil {
			log.Printf("adblock: engine stopped: %v", err)
		}
	}()
}

// Stop 停止引擎。
func (e *Engine) Stop() {
	if e.subMgr != nil {
		e.subMgr.Stop()
	}
	if e.proxy != nil {
		e.proxy.Close()
	}
}

// ProxyAddr 返回本地代理监听地址，用于 netstack 路由配置。
func (e *Engine) ProxyAddr() string {
	return e.proxy.Addr()
}

// ShouldIntercept 判断目标域名是否需要 MITM 解密。
func (e *Engine) ShouldIntercept(hostname string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.rules.ShouldMITM(hostname)
}

// TLSConfigForHost 为指定域名生成临时 TLS 证书。
func (e *Engine) TLSConfigForHost(hostname string) *tls.Config {
	cert, err := e.ca.CertForHost(hostname)
	if err != nil {
		log.Printf("adblock: failed to generate cert for %s: %v", hostname, err)
		return nil
	}
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
	}
}

// CACertPEM 返回 CA 证书 PEM 编码，用于导出给用户安装信任。
func (e *Engine) CACertPEM() []byte {
	return e.ca.CACertPEM()
}

// RuleCount 返回已加载的规则数量。
func (e *Engine) RuleCount() int {
	e.rules.mu.RLock()
	defer e.rules.mu.RUnlock()
	return len(e.rules.rules)
}

// LoadRules 从 reader 加载规则。
func (e *Engine) LoadRules(r io.Reader) error {
	return e.rules.Load(r)
}

// LoadScript 加载 JS 脚本。
func (e *Engine) LoadScript(name, content string) error {
	return e.scripts.LoadScript(name, content)
}

// SetEnabled 开关广告拦截。
func (e *Engine) SetEnabled(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = v
}

// IsEnabled 返回广告拦截是否启用。
func (e *Engine) IsEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_netstack

package main

import (
	"context"
	"encoding/json"
	"expvar"
	"net"
	"net/netip"
	"os"
	"path/filepath"

	"tailscale.com/adblock"
	"tailscale.com/tsd"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/netstack"
)

func init() {
	hookNewNetstack.Set(newNetstack)
}

func newNetstack(logf logger.Logf, sys *tsd.System, onlyNetstack bool) (tsd.NetstackImpl, error) {
	ns, err := netstack.Create(logf,
		sys.Tun.Get(),
		sys.Engine.Get(),
		sys.MagicSock.Get(),
		sys.Dialer.Get(),
		sys.DNSManager.Get(),
		sys.ProxyMapper(),
	)
	if err != nil {
		return nil, err
	}
	// Only register debug info if we have a debug mux
	if debugMux != nil {
		expvar.Publish("netstack", ns.ExpVar())
	}

	sys.Set(ns)
	ns.ProcessLocalIPs = onlyNetstack
	ns.ProcessSubnets = onlyNetstack || handleSubnetsInNetstack()

	// --- adblock 引擎集成 ---
	if ab := initAdblock(logf); ab != nil {
		bridge := adblock.NewNetBridge(ab)
		ns.GetTCPHandlerForFlow = bridge.TCPHandlerForFlow()
		logf("adblock: engine attached to netstack, %d rules loaded", ab.RuleCount())
	}

	dialer := sys.Dialer.Get() // must be set by caller already

	if onlyNetstack {
		e := sys.Engine.Get()
		dialer.UseNetstackForIP = func(ip netip.Addr) bool {
			_, ok := e.PeerForIP(ip)
			return ok
		}
		dialer.NetstackDialTCP = func(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
			// Note: don't just return ns.DialContextTCP or we'll return
			// *gonet.TCPConn(nil) instead of a nil interface which trips up
			// callers.
			tcpConn, err := ns.DialContextTCP(ctx, dst)
			if err != nil {
				return nil, err
			}
			return tcpConn, nil
		}
		dialer.NetstackDialUDP = func(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
			// Note: don't just return ns.DialContextUDP or we'll return
			// *gonet.UDPConn(nil) instead of a nil interface which trips up
			// callers.
			udpConn, err := ns.DialContextUDP(ctx, dst)
			if err != nil {
				return nil, err
			}
			return udpConn, nil
		}
	}

	return ns, nil
}

// initAdblock 初始化广告拦截引擎。
// 读取配置文件 /var/lib/tailscale/adblock.json 或环境变量 TS_ADBLOCK_ENABLED。
// 返回 nil 表示不启用。
func initAdblock(logf logger.Logf) *adblock.Engine {
	// 检查环境变量快速开关
	if os.Getenv("TS_ADBLOCK_ENABLED") == "0" {
		return nil
	}

	// 默认路径
	stateDir := "/var/lib/tailscale"
	if d := os.Getenv("TS_STATE_DIR"); d != "" {
		stateDir = d
	}

	adblockDir := filepath.Join(stateDir, "adblock")
	configFile := filepath.Join(adblockDir, "config.json")

	// 如果没有配置文件且没有环境变量强制启用，不启用
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		if os.Getenv("TS_ADBLOCK_ENABLED") != "1" {
			return nil
		}
	}

	// 确保 adblock 目录存在
	os.MkdirAll(adblockDir, 0700)
	os.MkdirAll(filepath.Join(adblockDir, "rules"), 0700)

	cfg := adblock.Config{
		Enabled:        true,
		CAKeyPath:      filepath.Join(adblockDir, "ca.key"),
		CACertPath:     filepath.Join(adblockDir, "ca.pem"),
		RuleDir:        filepath.Join(adblockDir, "rules"),
		UpdateInterval: 3600,
	}

	// 尝试读取配置文件覆盖默认值
	if data, err := os.ReadFile(configFile); err == nil {
		if parsed, err := parseAdblockConfig(data); err == nil {
			cfg = *parsed
		} else {
			logf("adblock: parse config: %v, using defaults", err)
		}
	}

	engine, err := adblock.New(cfg)
	if err != nil {
		logf("adblock: init failed: %v", err)
		return nil
	}

	// 加载本地规则文件
	ruleDir := cfg.RuleDir
	if entries, err := os.ReadDir(ruleDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			ext := filepath.Ext(name)
			if ext == ".conf" || ext == ".sgmodule" || ext == ".list" {
				path := filepath.Join(ruleDir, name)
				f, err := os.Open(path)
				if err != nil {
					logf("adblock: open rule %s: %v", name, err)
					continue
				}
				if err := engine.LoadRules(f); err != nil {
					logf("adblock: parse rule %s: %v", name, err)
				}
				f.Close()
				logf("adblock: loaded rule file %s", name)
			}
			if ext == ".js" {
				path := filepath.Join(ruleDir, name)
				content, err := os.ReadFile(path)
				if err == nil {
					engine.LoadScript(name, string(content))
					logf("adblock: loaded script %s", name)
				}
			}
		}
	}

	// 异步启动规则订阅更新
	engine.StartAsync()

	logf("adblock: engine started, CA cert at %s", cfg.CACertPath)
	return engine
}

// parseAdblockConfig 解析 adblock 配置文件。
func parseAdblockConfig(data []byte) (*adblock.Config, error) {
	// 简单的 JSON 解析
	// 配置文件格式:
	// {
	//   "enabled": true,
	//   "ruleUrls": ["https://..."],
	//   "ruleDir": "/var/lib/tailscale/adblock/rules",
	//   "updateInterval": 3600,
	//   "caKeyPath": "/var/lib/tailscale/adblock/ca.key",
	//   "caCertPath": "/var/lib/tailscale/adblock/ca.pem"
	// }
	type jsonConfig struct {
		Enabled        bool     `json:"enabled"`
		RuleURLs       []string `json:"ruleUrls"`
		RuleDir        string   `json:"ruleDir"`
		UpdateInterval int      `json:"updateInterval"`
		CAKeyPath      string   `json:"caKeyPath"`
		CACertPath     string   `json:"caCertPath"`
	}

	var jc jsonConfig
	if err := json.Unmarshal(data, &jc); err != nil {
		return nil, err
	}

	return &adblock.Config{
		Enabled:        jc.Enabled,
		RuleURLs:       jc.RuleURLs,
		RuleDir:        jc.RuleDir,
		UpdateInterval: jc.UpdateInterval,
		CAKeyPath:      jc.CAKeyPath,
		CACertPath:     jc.CACertPath,
	}, nil
}

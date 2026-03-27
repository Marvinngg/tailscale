package adblock

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/adblock/rules"
)

// SubscriptionManager 管理规则订阅（远程 URL 定期拉取更新）。
type SubscriptionManager struct {
	urls     []string
	ruleDir  string
	interval time.Duration
	ruleSet  *RuleSet

	mu     sync.Mutex
	stopCh chan struct{}
}

// NewSubscriptionManager 创建规则订阅管理器。
func NewSubscriptionManager(urls []string, ruleDir string, intervalSec int, ruleSet *RuleSet) *SubscriptionManager {
	if intervalSec <= 0 {
		intervalSec = 3600
	}
	return &SubscriptionManager{
		urls:     urls,
		ruleDir:  ruleDir,
		interval: time.Duration(intervalSec) * time.Second,
		ruleSet:  ruleSet,
	}
}

// Start 启动订阅管理器：首次加载 + 定期更新。
func (sm *SubscriptionManager) Start() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.stopCh = make(chan struct{})

	// 首次加载：先从本地缓存加载，再异步更新远程
	if err := sm.loadLocal(); err != nil {
		log.Printf("adblock/subscription: local load: %v", err)
	}

	go sm.updateLoop()
	return nil
}

// Stop 停止订阅管理器。
func (sm *SubscriptionManager) Stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.stopCh != nil {
		close(sm.stopCh)
		sm.stopCh = nil
	}
}

func (sm *SubscriptionManager) loadLocal() error {
	if sm.ruleDir == "" {
		return nil
	}

	entries, err := os.ReadDir(sm.ruleDir)
	if err != nil {
		return err
	}

	var allRules []rules.Rule
	hostnames := rules.NewHostnameSet()

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".conf") && !strings.HasSuffix(entry.Name(), ".sgmodule")) {
			continue
		}

		f, err := os.Open(filepath.Join(sm.ruleDir, entry.Name()))
		if err != nil {
			log.Printf("adblock/subscription: open %s: %v", entry.Name(), err)
			continue
		}

		result, err := rules.Parse(f)
		f.Close()
		if err != nil {
			log.Printf("adblock/subscription: parse %s: %v", entry.Name(), err)
			continue
		}

		allRules = append(allRules, result.Rules...)
		// 合并 hostnames
		// TODO: HostnameSet merge
		_ = result.Hostnames
	}

	if len(allRules) > 0 {
		sm.ruleSet.Replace(allRules, hostnames)
		log.Printf("adblock/subscription: loaded %d rules from local cache", len(allRules))
	}

	return nil
}

func (sm *SubscriptionManager) updateLoop() {
	// 首次延迟 10 秒再更新远程
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-sm.stopCh:
			return
		case <-timer.C:
			sm.fetchAll()
			timer.Reset(sm.interval)
		}
	}
}

func (sm *SubscriptionManager) fetchAll() {
	var allRules []rules.Rule
	hostnames := rules.NewHostnameSet()

	for _, url := range sm.urls {
		rr, hn, err := sm.fetchOne(url)
		if err != nil {
			log.Printf("adblock/subscription: fetch %s: %v", url, err)
			continue
		}
		allRules = append(allRules, rr...)
		_ = hn // TODO: merge hostnames
	}

	if len(allRules) > 0 {
		sm.ruleSet.Replace(allRules, hostnames)
		log.Printf("adblock/subscription: updated %d rules from %d sources", len(allRules), len(sm.urls))
	}
}

func (sm *SubscriptionManager) fetchOne(url string) ([]rules.Rule, *rules.HostnameSet, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 限制读取大小（10MB）
	body := io.LimitReader(resp.Body, 10<<20)
	result, err := rules.Parse(body)
	if err != nil {
		return nil, nil, err
	}

	// 缓存到本地
	if sm.ruleDir != "" {
		sm.cacheToLocal(url, resp)
	}

	return result.Rules, result.Hostnames, nil
}

func (sm *SubscriptionManager) cacheToLocal(url string, resp *http.Response) {
	// 用 URL hash 作为文件名缓存
	// TODO: 实现本地缓存
}

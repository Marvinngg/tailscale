package adblock

import (
	"io"
	"net/http"
	"sync"

	"tailscale.com/adblock/rules"
)

// RuleSet 管理所有已加载的规则。线程安全。
type RuleSet struct {
	mu        sync.RWMutex
	rules     []rules.Rule
	hostnames *rules.HostnameSet
}

// NewRuleSet 创建空规则集。
func NewRuleSet() *RuleSet {
	return &RuleSet{
		hostnames: rules.NewHostnameSet(),
	}
}

// Load 从 reader 加载规则（追加到现有规则）。
func (rs *RuleSet) Load(r io.Reader) error {
	result, err := rules.Parse(r)
	if err != nil {
		return err
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()

	rs.rules = append(rs.rules, result.Rules...)
	rs.hostnames.Merge(result.Hostnames)
	return nil
}

// Replace 替换全部规则。用于规则更新。
func (rs *RuleSet) Replace(allRules []rules.Rule, hostnames *rules.HostnameSet) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.rules = allRules
	rs.hostnames = hostnames
}

// ShouldMITM 判断域名是否需要 MITM 解密。
func (rs *RuleSet) ShouldMITM(hostname string) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.hostnames.Match(hostname)
}

// Match 对 URL 匹配规则，返回第一个命中的规则。
func (rs *RuleSet) Match(url string) *rules.Rule {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	for i := range rs.rules {
		if rs.rules[i].Pattern.MatchString(url) {
			return &rs.rules[i]
		}
	}
	return nil
}

// Execute 执行规则动作，写入 HTTP 响应。
// 对于 script 类型规则，需要先获取上游响应再修改。
func (rs *RuleSet) Execute(rule *rules.Rule, w http.ResponseWriter) bool {
	switch rule.Action {
	case rules.ActionReject:
		w.WriteHeader(http.StatusForbidden)
		return true
	case rules.ActionReject200:
		w.WriteHeader(http.StatusOK)
		return true
	case rules.ActionRejectDict:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
		return true
	case rules.ActionRejectArray:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
		return true
	case rules.ActionRejectImg:
		// 1x1 透明 GIF
		w.Header().Set("Content-Type", "image/gif")
		w.WriteHeader(http.StatusOK)
		w.Write(transparentGIF)
		return true
	case rules.ActionMock:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rule.StatusCode)
		if rule.ActionData != "" {
			w.Write([]byte(rule.ActionData))
		}
		return true
	case rules.ActionScriptResponse, rules.ActionScriptRequest:
		// Script 类型不在此处理，需要上游响应后由 ScriptEngine 处理
		return false
	}
	return false
}

// 1x1 透明 GIF
var transparentGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00,
	0x01, 0x00, 0x80, 0x00, 0x00, 0xff, 0xff, 0xff,
	0x00, 0x00, 0x00, 0x21, 0xf9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44,
	0x01, 0x00, 0x3b,
}

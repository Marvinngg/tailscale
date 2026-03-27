// Package rules 解析 Surge / QuantumultX 格式的广告拦截规则。
//
// 支持的规则格式：
//
//	Surge URL Rewrite:
//	  ^https://example\.com/ad url reject-200
//
//	Surge Map Local:
//	  ^https://example\.com/api data-type=text data="{}" status-code=200
//
//	Surge Script:
//	  script-name = type=http-response, pattern=^https://example\.com, requires-body=1, script-path=xxx.js
//
//	QuantumultX Rewrite:
//	  ^https://example\.com/ad url reject-200
//	  ^https://example\.com/api url script-response-body xxx.js
//
//	MITM hostname 声明:
//	  hostname = example.com, *.api.example.com
package rules

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Action 规则动作类型。
type Action int

const (
	ActionReject        Action = iota // 拒绝连接
	ActionReject200                    // 返回 HTTP 200 空响应
	ActionRejectDict                   // 返回 {}
	ActionRejectArray                  // 返回 []
	ActionRejectImg                    // 返回 1px 透明 GIF
	ActionMock                         // 返回预设数据
	ActionScriptResponse               // JS 脚本修改响应
	ActionScriptRequest                // JS 脚本修改请求
)

// Rule 一条拦截规则。
type Rule struct {
	Pattern    *regexp.Regexp // URL 正则匹配
	RawPattern string        // 原始正则字符串
	Action     Action        // 动作类型
	ActionData string        // Mock 数据或脚本路径
	StatusCode int           // Mock 响应状态码
	DataType   string        // Mock 数据类型 (text/tiny-gif/base64)
}

// HostnameSet MITM 域名集合。只有在此集合中声明的域名才会被 TLS 解密。
type HostnameSet struct {
	exact    map[string]bool // 精确匹配: example.com
	wildcard []string        // 通配符匹配: *.example.com
}

// NewHostnameSet 创建空的域名集合。
func NewHostnameSet() *HostnameSet {
	return &HostnameSet{
		exact: make(map[string]bool),
	}
}

// Add 添加域名（支持通配符 *. 前缀）。
func (h *HostnameSet) Add(hostname string) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return
	}
	if strings.HasPrefix(hostname, "*.") {
		h.wildcard = append(h.wildcard, hostname[1:]) // 保留 ".example.com"
	} else if strings.HasPrefix(hostname, "*") {
		h.wildcard = append(h.wildcard, hostname[1:])
	} else {
		h.exact[hostname] = true
	}
}

// Merge 将另一个 HostnameSet 合并到当前集合。
func (h *HostnameSet) Merge(other *HostnameSet) {
	for k := range other.exact {
		h.exact[k] = true
	}
	h.wildcard = append(h.wildcard, other.wildcard...)
}

// Match 判断域名是否在集合中。
func (h *HostnameSet) Match(hostname string) bool {
	if h.exact[hostname] {
		return true
	}
	for _, suffix := range h.wildcard {
		if strings.HasSuffix(hostname, suffix) {
			return true
		}
	}
	return false
}

// ParseResult 解析结果。
type ParseResult struct {
	Rules     []Rule
	Hostnames *HostnameSet
}

// Parse 解析规则文件内容。自动检测 Surge / QX 格式。
func Parse(r io.Reader) (*ParseResult, error) {
	result := &ParseResult{
		Hostnames: NewHostnameSet(),
	}

	scanner := bufio.NewScanner(r)
	var section string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") {
			continue
		}

		// Surge section headers
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}

		// MITM hostname 声明
		if strings.HasPrefix(line, "hostname") && strings.Contains(line, "=") {
			parseHostnames(line, result.Hostnames)
			continue
		}

		// 根据 section 或行格式解析规则
		switch section {
		case "url rewrite", "rewrite_local":
			if rule, err := parseRewriteRule(line); err == nil {
				result.Rules = append(result.Rules, rule)
			}
		case "map local":
			if rule, err := parseMapLocalRule(line); err == nil {
				result.Rules = append(result.Rules, rule)
			}
		case "script":
			if rule, err := parseSurgeScript(line); err == nil {
				result.Rules = append(result.Rules, rule)
			}
		default:
			// QX 格式没有 section header，直接按行解析
			if rule, err := parseRewriteRule(line); err == nil {
				result.Rules = append(result.Rules, rule)
			}
		}
	}

	return result, scanner.Err()
}

func parseHostnames(line string, set *HostnameSet) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return
	}
	// 处理 %APPEND% 和 %INSERT% 指令
	hostStr := strings.TrimSpace(parts[1])
	hostStr = strings.ReplaceAll(hostStr, "%APPEND%", "")
	hostStr = strings.ReplaceAll(hostStr, "%INSERT%", "")

	for _, h := range strings.Split(hostStr, ",") {
		set.Add(strings.TrimSpace(h))
	}
}

func parseRewriteRule(line string) (Rule, error) {
	// 格式: ^https://pattern url action [script-path]
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return Rule{}, fmt.Errorf("invalid rewrite rule: %s", line)
	}

	pattern := parts[0]
	// parts[1] 应该是 "url"
	actionStr := parts[2]

	re, err := regexp.Compile(pattern)
	if err != nil {
		return Rule{}, fmt.Errorf("invalid regex %q: %w", pattern, err)
	}

	rule := Rule{
		Pattern:    re,
		RawPattern: pattern,
		StatusCode: 200,
	}

	switch actionStr {
	case "reject":
		rule.Action = ActionReject
	case "reject-200":
		rule.Action = ActionReject200
	case "reject-dict":
		rule.Action = ActionRejectDict
	case "reject-array":
		rule.Action = ActionRejectArray
	case "reject-img":
		rule.Action = ActionRejectImg
	case "script-response-body":
		rule.Action = ActionScriptResponse
		if len(parts) >= 4 {
			rule.ActionData = parts[3]
		}
	case "script-request-header", "script-request-body":
		rule.Action = ActionScriptRequest
		if len(parts) >= 4 {
			rule.ActionData = parts[3]
		}
	default:
		return Rule{}, fmt.Errorf("unknown action: %s", actionStr)
	}

	return rule, nil
}

func parseMapLocalRule(line string) (Rule, error) {
	// Surge Map Local 格式:
	// ^https://pattern data-type=text data="{}" status-code=200
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return Rule{}, fmt.Errorf("invalid map local rule: %s", line)
	}

	re, err := regexp.Compile(parts[0])
	if err != nil {
		return Rule{}, fmt.Errorf("invalid regex: %w", err)
	}

	rule := Rule{
		Pattern:    re,
		RawPattern: parts[0],
		Action:     ActionMock,
		StatusCode: 200,
		DataType:   "text",
	}

	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "data-type=") {
			rule.DataType = strings.TrimPrefix(part, "data-type=")
		} else if strings.HasPrefix(part, "data=") {
			rule.ActionData = strings.Trim(strings.TrimPrefix(part, "data="), "\"")
		} else if strings.HasPrefix(part, "status-code=") {
			fmt.Sscanf(strings.TrimPrefix(part, "status-code="), "%d", &rule.StatusCode)
		}
	}

	return rule, nil
}

func parseSurgeScript(line string) (Rule, error) {
	// Surge Script 格式:
	// name = type=http-response, pattern=^https://..., requires-body=1, script-path=xxx.js
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return Rule{}, fmt.Errorf("invalid script rule: %s", line)
	}

	props := make(map[string]string)
	for _, item := range strings.Split(parts[1], ",") {
		kv := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(kv) == 2 {
			props[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}

	patternStr, ok := props["pattern"]
	if !ok {
		return Rule{}, fmt.Errorf("script rule missing pattern")
	}

	re, err := regexp.Compile(patternStr)
	if err != nil {
		return Rule{}, fmt.Errorf("invalid regex: %w", err)
	}

	rule := Rule{
		Pattern:    re,
		RawPattern: patternStr,
		ActionData: props["script-path"],
	}

	switch props["type"] {
	case "http-response":
		rule.Action = ActionScriptResponse
	case "http-request":
		rule.Action = ActionScriptRequest
	default:
		rule.Action = ActionScriptResponse
	}

	return rule, nil
}

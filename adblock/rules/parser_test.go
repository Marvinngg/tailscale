package rules

import (
	"strings"
	"testing"
)

// 使用真实的墨鱼规则片段测试解析器。

func TestParseQXRewrite(t *testing.T) {
	// QuantumultX 格式（墨鱼去开屏广告风格）
	input := `
# 墨鱼去开屏广告 2.0
# https://github.com/ddgksf2013

# 微博开屏广告
^https://boot\.biz\.weibo\.com/v\d/ad/realtime url reject-200
^https://sdkapp\.uve\.weibo\.com/interface/sdk/ url reject-200

# 知乎开屏广告
^https://api\.zhihu\.com/commercial_api/launch_v2 url reject-dict
^https://api\.zhihu\.com/ad-style-service url reject-dict

# 高德地图开屏
^https://m5\.amap\.com/ws/valueadded/alimama/splash_screen url reject-200

# 微博时间线广告（需要 JS 脚本精确去除）
^https://api\.weibo\.com/2/statuses/unread_friends_timeline url script-response-body weibo_timeline.js

# MITM
hostname = boot.biz.weibo.com, sdkapp.uve.weibo.com, api.zhihu.com, m5.amap.com, api.weibo.com
`

	result, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(result.Rules) != 6 {
		t.Fatalf("expected 6 rules, got %d", len(result.Rules))
	}

	// 验证动作类型
	tests := []struct {
		idx    int
		action Action
	}{
		{0, ActionReject200},  // 微博开屏
		{1, ActionReject200},  // 微博 SDK
		{2, ActionRejectDict}, // 知乎开屏
		{3, ActionRejectDict}, // 知乎广告样式
		{4, ActionReject200},  // 高德开屏
		{5, ActionScriptResponse}, // 微博时间线
	}

	for _, tt := range tests {
		if result.Rules[tt.idx].Action != tt.action {
			t.Errorf("rule[%d] action = %v, want %v", tt.idx, result.Rules[tt.idx].Action, tt.action)
		}
	}

	// 验证脚本路径
	if result.Rules[5].ActionData != "weibo_timeline.js" {
		t.Errorf("script rule data = %q, want %q", result.Rules[5].ActionData, "weibo_timeline.js")
	}

	// 验证 MITM hostname
	hostnameTests := []struct {
		host  string
		match bool
	}{
		{"boot.biz.weibo.com", true},
		{"api.zhihu.com", true},
		{"api.weibo.com", true},
		{"m5.amap.com", true},
		{"www.google.com", false},
		{"baidu.com", false},
	}

	for _, tt := range hostnameTests {
		if got := result.Hostnames.Match(tt.host); got != tt.match {
			t.Errorf("hostname %q: Match() = %v, want %v", tt.host, got, tt.match)
		}
	}
}

func TestParseHostnameWildcard(t *testing.T) {
	input := `hostname = *.weibo.com, *.zhihu.com, api.bilibili.com`

	result, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		host  string
		match bool
	}{
		{"api.weibo.com", true},
		{"boot.biz.weibo.com", true},
		{"weibo.com", false}, // 通配符 *.weibo.com 不匹配 weibo.com 本身
		{"api.zhihu.com", true},
		{"api.bilibili.com", true},
		{"www.bilibili.com", false},
	}

	for _, tt := range tests {
		if got := result.Hostnames.Match(tt.host); got != tt.match {
			t.Errorf("hostname %q: Match() = %v, want %v", tt.host, got, tt.match)
		}
	}
}

func TestParseSurgeModule(t *testing.T) {
	// Surge .sgmodule 格式
	input := `
[URL Rewrite]
^https://api\.example\.com/splash url reject-200
^https://api\.example\.com/banner url reject-img

[Map Local]
^https://api\.example\.com/ad/config data-type=text data="{}" status-code=200

[Script]
去广告 = type=http-response, pattern=^https://api\.example\.com/feed, requires-body=1, script-path=remove_ad.js

[MITM]
hostname = %APPEND% api.example.com
`

	result, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Rules) != 4 {
		t.Fatalf("expected 4 rules, got %d", len(result.Rules))
	}

	// URL Rewrite rules
	if result.Rules[0].Action != ActionReject200 {
		t.Errorf("rule[0] action = %v, want reject-200", result.Rules[0].Action)
	}
	if result.Rules[1].Action != ActionRejectImg {
		t.Errorf("rule[1] action = %v, want reject-img", result.Rules[1].Action)
	}

	// Map Local rule
	if result.Rules[2].Action != ActionMock {
		t.Errorf("rule[2] action = %v, want mock", result.Rules[2].Action)
	}
	if result.Rules[2].ActionData != "{}" {
		t.Errorf("rule[2] data = %q, want %q", result.Rules[2].ActionData, "{}")
	}

	// Script rule
	if result.Rules[3].Action != ActionScriptResponse {
		t.Errorf("rule[3] action = %v, want script-response", result.Rules[3].Action)
	}
	if result.Rules[3].ActionData != "remove_ad.js" {
		t.Errorf("rule[3] script = %q, want %q", result.Rules[3].ActionData, "remove_ad.js")
	}

	// hostname（含 %APPEND% 指令，应被正确处理）
	if !result.Hostnames.Match("api.example.com") {
		t.Error("hostname api.example.com should match")
	}
}

func TestRuleMatching(t *testing.T) {
	input := `
^https://boot\.biz\.weibo\.com/ url reject-200
^https://api\.zhihu\.com/commercial_api/launch_v2 url reject-dict
`

	result, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	// 测试 URL 匹配
	tests := []struct {
		url   string
		match bool
	}{
		{"https://boot.biz.weibo.com/v1/ad/realtime?os=ios", true},
		{"https://api.zhihu.com/commercial_api/launch_v2?app=zhihu", true},
		{"https://api.zhihu.com/normal/api", false},
		{"https://www.google.com", false},
	}

	for _, tt := range tests {
		matched := false
		for _, rule := range result.Rules {
			if rule.Pattern.MatchString(tt.url) {
				matched = true
				break
			}
		}
		if matched != tt.match {
			t.Errorf("url %q: matched = %v, want %v", tt.url, matched, tt.match)
		}
	}
}

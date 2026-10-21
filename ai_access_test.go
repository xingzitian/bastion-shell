package main

// 桌面版「AI 访问」页的数据。
//
// 桌面版是**单令牌**（全权），所以这一页的重点是"谁绑在哪条会话上"，
// 以及**如实说明**：多令牌/权限分档只有扩展侧有。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaskToken(t *testing.T) {
	if got := maskToken("abcdefghijklmnop"); got != "abcd…mnop" {
		t.Fatalf("掩码不对：%q", got)
	}
	if got := maskToken("short"); got != "••••" {
		t.Fatalf("太短的令牌不该露头：%q", got)
	}
}

func TestBuildAiAccessViewShowsMainTokenAndBindings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	t.Setenv("BASTIONSHELL_CONFIG_DIR", dir) // mcp.json 也落临时目录，别碰真实配置

	doc := loadBindings()
	// 主令牌绑一条、扩展侧客户端绑另一条
	if _, _, e := bindSession(doc, "default", sessionRef{SID: "s-1", Name: "#3 deploy@h1"}, "A"); e != "" {
		t.Fatalf("绑定失败：%s", e)
	}
	if _, _, e := bindSession(doc, "claude", sessionRef{SID: "s-2", Name: "#1 deploy@h2"}, ""); e != "" {
		t.Fatalf("绑定失败：%s", e)
	}
	doc.put("claude", clientBindings{
		Label:     "claude",
		UpdatedAt: "2026-10-21T00:00:00Z",
		Sessions: map[string]bindingEntry{
			"B": {SessionID: "s-2", Name: "#1 deploy@h2", Since: "2026-10-21T00:00:00Z"},
		},
	})
	saveBindings(doc)

	v := buildAiAccessView()
	if v.Endpoint == "" {
		t.Fatal("端点不该为空")
	}
	if v.TokenMasked == "" || v.TokenMasked == v.Token {
		t.Fatal("UI 上应该只给掩码")
	}
	if !v.MainOnly {
		t.Log("注意：这个用例里还有 claude 客户端，MainOnly 应为 false")
	}
	// 主令牌必须显示，且是 full
	main := v.Clients[0]
	if main.ClientID != "default" || main.Scope != "full" || !main.IsMain {
		t.Fatalf("第一个应该是主令牌/全权：%+v", main)
	}
	if main.ScopeLabel != "全权" || len(main.Can) == 0 {
		t.Fatalf("主令牌要带上人话的权限说明：%+v", main)
	}
	// 扩展侧客户端：标 external，不冒充权限
	var claude *aiClientView
	for i := range v.Clients {
		if v.Clients[i].ClientID == "claude" {
			claude = &v.Clients[i]
		}
	}
	if claude == nil || !claude.External {
		t.Fatalf("扩展侧客户端要标 external：%+v", claude)
	}
	if claude.Can != nil {
		t.Fatal("扩展侧客户端的权限不该由桌面版编造")
	}
	// note 必须说清"桌面版是单令牌"
	if v.Note == "" || !contains(v.Note, "单令牌") {
		t.Fatalf("说明要写清桌面版是单令牌：%q", v.Note)
	}
	// 绑定要落到文件路径上（UI 显示用）
	if !filepath.IsAbs(v.BindingsFile) {
		t.Fatalf("绑定文件路径应该是绝对路径：%q", v.BindingsFile)
	}
	if _, err := os.Stat(v.BindingsFile); err != nil {
		t.Fatalf("绑定文件应该真的存在：%v", err)
	}
}

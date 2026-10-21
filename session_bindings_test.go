package main

// 桌面版的会话解析/绑定（P0a）—— 与扩展侧同一份契约的 Go 实现。
//
// 盯的还是那件事：**命令绝不能落到错的机器上**。歧义必须拒绝、写操作没绑定必须拒绝，
// 绝不回退到"最近活动的那条"。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func refs() []sessionRef {
	return []sessionRef{
		{SID: "s-7f3a", Name: "deploy@10.0.0.1", State: "shell"},
		{SID: "s-9c21", Name: "deploy@10.0.0.2", State: "shell"},
		{SID: "s-1111", Name: "deploy@10.0.0.10", State: "menu"},
	}
}

func withBindingsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	return dir
}

func emptyDoc() *bindingsDoc { return &bindingsDoc{Version: 1, Clients: map[string]clientBindings{}} }

// ───────────── 解析 ─────────────

func TestResolveDesktopByIDNameAlias(t *testing.T) {
	doc := emptyDoc()
	r, msg := resolveSessionRef(refs(), "s-7f3a", "write", "default", doc, nil, false)
	if msg != "" || r.SID != "s-7f3a" {
		t.Fatalf("按 id 应该命中：%v %q", r, msg)
	}
	r, msg = resolveSessionRef(refs(), "deploy@10.0.0.2", "write", "default", doc, nil, false)
	if msg != "" || r.SID != "s-9c21" {
		t.Fatalf("按完整名应该命中：%v %q", r, msg)
	}
	// 绑定一个别名再用别名解析
	if _, _, e := bindSession(doc, "default", refs()[0], "A"); e != "" {
		t.Fatalf("绑定失败：%s", e)
	}
	r, msg = resolveSessionRef(refs(), "a", "write", "default", doc, nil, false)
	if msg != "" || r.SID != "s-7f3a" {
		t.Fatalf("按别名（大小写不敏感）应该命中：%v %q", r, msg)
	}
}

func TestResolveDesktopAmbiguousFragmentRefused(t *testing.T) {
	// 10.0.0.1 是 10.0.0.10 的前缀：以前 strings.Contains 会取第一条 → 打到错的机器
	_, msg := resolveSessionRef(refs(), "10.0.0.1", "write", "default", emptyDoc(), nil, false)
	if msg == "" {
		t.Fatal("★ 歧义必须拒绝")
	}
	if !contains(msg, "匹配到 2 条会话") || !contains(msg, "s-7f3a") || !contains(msg, "s-1111") {
		t.Fatalf("拒绝文案要列出候选：%q", msg)
	}
}

func TestResolveDesktopWriteWithoutBindingRefused(t *testing.T) {
	_, msg := resolveSessionRef(refs(), "", "write", "default", emptyDoc(), &refs()[0], false)
	if msg == "" || !contains(msg, "还没有绑定任何会话") {
		t.Fatalf("写操作没绑定必须拒绝（即使有 fallback）：%q", msg)
	}
}

func TestResolveDesktopMultiBindingRefused(t *testing.T) {
	doc := emptyDoc()
	_, _, _ = bindSession(doc, "default", refs()[0], "A")
	_, _, _ = bindSession(doc, "default", refs()[1], "B")
	_, msg := resolveSessionRef(refs(), "", "write", "default", doc, nil, false)
	if !contains(msg, "绑定了 2 条会话") || !contains(msg, "不会替你猜") {
		t.Fatalf("多绑定必须要求指定：%q", msg)
	}
}

func TestResolveDesktopReadFallsBack(t *testing.T) {
	fb := refs()[1]
	r, msg := resolveSessionRef(refs(), "", "read", "default", emptyDoc(), &fb, false)
	if msg != "" || r.SID != "s-9c21" {
		t.Fatalf("读操作没有绑定时允许回退：%v %q", r, msg)
	}
}

// ───────────── 绑定 ─────────────

func TestBindDesktopAliasesAndIdempotent(t *testing.T) {
	doc := emptyDoc()
	a, existing, msg := bindSession(doc, "default", refs()[0], "")
	if msg != "" || a != "A" || existing {
		t.Fatalf("第一个应该自动拿 A：%q %v %q", a, existing, msg)
	}
	a2, existing2, _ := bindSession(doc, "default", refs()[0], "")
	if a2 != "A" || !existing2 {
		t.Fatalf("重复绑定应该幂等返回 A：%q %v", a2, existing2)
	}
	b, _, _ := bindSession(doc, "default", refs()[1], "")
	if b != "B" {
		t.Fatalf("第二个应该是 B：%q", b)
	}
	if _, _, e := bindSession(doc, "default", refs()[2], "A"); e == "" {
		t.Fatal("别名冲突必须拒绝")
	}
}

func TestBindingsStoreRoundTripAndOwners(t *testing.T) {
	dir := withBindingsDir(t)
	doc := loadBindings()
	if _, _, e := bindSession(doc, "default", refs()[0], ""); e != "" {
		t.Fatalf("绑定失败：%s", e)
	}
	saveBindings(doc)
	if _, err := os.Stat(filepath.Join(dir, bindingsFile)); err != nil {
		t.Fatalf("绑定文件应该落在共享目录：%v", err)
	}
	again := loadBindings()
	if aliasOfSession(again.client("default"), "s-7f3a") != "A" {
		t.Fatalf("读回来应该还有 A：%+v", again.Clients)
	}
	if own := again.owners("s-7f3a"); len(own) != 1 || own[0] != "default" {
		t.Fatalf("owners 不对：%v", own)
	}
}

func TestCleanupBindingsDesktop(t *testing.T) {
	doc := emptyDoc()
	_, _, _ = bindSession(doc, "default", refs()[0], "A")
	_, _, _ = bindSession(doc, "default", refs()[1], "B")
	// B 设成 13 小时前 → 超时
	cb := doc.client("default")
	b := cb.Sessions["B"]
	b.Since = time.Now().Add(-13 * time.Hour).Format(time.RFC3339)
	cb.Sessions["B"] = b
	doc.put("default", cb)
	changed := cleanupBindings(doc, map[string]bool{"s-7f3a": true}) // s-9c21 已经没了 + B 超时
	if !changed {
		t.Fatal("应该发生清理")
	}
	if len(doc.client("default").Sessions) != 1 {
		t.Fatalf("应该只剩一条：%+v", doc.client("default").Sessions)
	}
}

func TestExpectCursorProblemDesktop(t *testing.T) {
	if expectCursorProblem(nil, 5) != "" {
		t.Fatal("没传就不校验")
	}
	if expectCursorProblem(ptrInt64(5), 5) != "" {
		t.Fatal("一致应放行")
	}
	if msg := expectCursorProblem(ptrInt64(4), 5); !contains(msg, "cursor 4 → 5") {
		t.Fatalf("不一致要拒绝并说明：%q", msg)
	}
}

func ptrInt64(v int64) *int64 { return &v }

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}

func TestAliasNameAndCap32(t *testing.T) {
	want := map[int]string{0: "A", 7: "H", 25: "Z", 26: "AA", 27: "AB", 31: "AF"}
	for i, name := range want {
		if got := aliasName(i); got != name {
			t.Fatalf("aliasName(%d) = %q，应为 %q", i, got, name)
		}
	}
	doc := emptyDoc()
	var seen []string
	for i := 0; i < 32; i++ {
		a, _, msg := bindSession(doc, "default", sessionRef{SID: fmt.Sprintf("s-%d", i), Name: fmt.Sprintf("#%d h", i)}, "")
		if msg != "" {
			t.Fatalf("第 %d 条应该绑定成功：%s", i+1, msg)
		}
		seen = append(seen, a)
	}
	if seen[8] != "I" {
		t.Fatalf("第 9 条应该是 I，得到 %q（原来只有 A..H）", seen[8])
	}
	if seen[26] != "AA" || seen[31] != "AF" {
		t.Fatalf("26/31 应该是 AA/AF，得到 %q/%q", seen[26], seen[31])
	}
	if _, _, msg := bindSession(doc, "default", sessionRef{SID: "s-32", Name: "#32 h"}, ""); msg == "" || !contains(msg, "32") {
		t.Fatalf("第 33 条必须被拒并说明上限：%q", msg)
	}
}

package main

// 凭据清单（桌面侧写入）：与扩展共用的 credentials.json。
// 盯的是：只写元数据、同键去重、删除只删那一条、坏文件不影响保存。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withSharedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	return dir
}

func readCredIndexRaw(t *testing.T) (f credIndexFile) {
	t.Helper()
	raw, err := os.ReadFile(credentialIndexPath())
	if err != nil {
		t.Fatalf("读清单失败：%v", err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("清单不是合法 JSON：%v（%s）", err, string(raw))
	}
	return f
}

func TestCredIndexRecordAndDrop(t *testing.T) {
	withSharedDir(t)
	recordCredIndex("中转长期机器", "password", "desktop")
	recordCredIndex("中转长期机器", "password", "desktop") // 重复记只留一条
	f := readCredIndexRaw(t)
	if len(f.Entries) != 1 {
		t.Fatalf("同键同种类同属主应只留一条，实际 %d 条：%+v", len(f.Entries), f.Entries)
	}
	if f.Entries[0].Kind != "password" || f.Entries[0].Owner != "desktop" {
		t.Fatalf("字段写错了：%+v", f.Entries[0])
	}
	if f.Entries[0].UpdatedAt == "" {
		t.Fatal("updatedAt 不能为空（面板要显示「什么时候存的」）")
	}

	dropCredIndex("中转长期机器", "password", "desktop")
	if got := readCredIndexRaw(t).Entries; len(got) != 0 {
		t.Fatalf("删除后应为空，实际 %+v", got)
	}
}

func TestCredIndexKeepsOtherOwnersAndKinds(t *testing.T) {
	withSharedDir(t)
	// 扩展写的那条，桌面不该动
	idx := credIndexFile{Version: 1, Entries: []credIndexEntry{
		{Key: "p1", Kind: "password", Owner: "vscode", UpdatedAt: "2026-11-27T01:00:00Z"},
	}}
	raw, _ := json.Marshal(idx)
	if err := os.MkdirAll(filepath.Dir(credentialIndexPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialIndexPath(), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	recordCredIndex("p1", "passphrase", "desktop")
	dropCredIndex("p1", "password", "desktop") // 桌面没有这条 → 不该删掉扩展那条

	got := readCredIndexRaw(t).Entries
	if len(got) != 2 {
		t.Fatalf("应保留 2 条（vscode/password + desktop/passphrase），实际 %+v", got)
	}
	var hasVscodePw bool
	for _, e := range got {
		if e.Owner == "vscode" && e.Kind == "password" {
			hasVscodePw = true
		}
	}
	if !hasVscodePw {
		t.Fatalf("扩展那条被误删了：%+v", got)
	}
}

func TestCredIndexOnlyMetadata(t *testing.T) {
	withSharedDir(t)
	recordCredIndex("secret-profile", "password", "desktop")
	raw, err := os.ReadFile(credentialIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, bad := range []string{"\"value\"", "\"secret\"", "\"cipher\"", "\"plain\""} {
		if strings.Contains(text, bad) {
			t.Fatalf("清单里不该出现 %s：%s", bad, text)
		}
	}
}

func TestCredIndexBadFileDoesNotBreak(t *testing.T) {
	dir := withSharedDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, credentialIndexFile), []byte("{ 坏掉的 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 坏文件 → 读成空表
	if got := loadCredIndexLocked(); len(got) != 0 {
		t.Fatalf("坏文件应读成空表，实际 %+v", got)
	}
	// 而且不该阻止继续记录
	recordCredIndex("ok", "password", "desktop")
	if got := readCredIndexRaw(t).Entries; len(got) != 1 || got[0].Key != "ok" {
		t.Fatalf("坏文件之后应能重新写，实际 %+v", got)
	}
}

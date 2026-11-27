package main

// 桌面侧凭据：① /api/credentials 的数据组装；② 档案改名/删除时 DPAPI 条目的去向。
//
// 与扩展侧同一套规则（src/core/profileSecretMigration.ts）：
//   - 改名（同一台机器的新名字还在）→ 密码搬过去；
//   - 删除（没有接手者）→ 密码清掉，不留孤儿；
//   - 分不清（同身份多条）→ 不搬也不删，留成可见孤儿。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// 测试用的隔离目录：档案/清单走共享目录，DPAPI 走 UserConfigDir（Linux 下是 XDG_CONFIG_HOME）
func isolate(t *testing.T) {
	t.Helper()
	shared := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", shared)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("BASTIONSHELL_LEGACY_PROFILES", filepath.Join(t.TempDir(), "none.json"))
}

func newTestProfileStore(t *testing.T) *profileStore {
	t.Helper()
	st := newProfileStore()
	st.load()
	return st
}

func TestBuildCredentialsViewRowsAndOrphans(t *testing.T) {
	entries := []credIndexEntry{
		{Key: "xzt", Kind: "password", Owner: "vscode", UpdatedAt: "2026-11-27T01:00:00Z"},
		{Key: "xzt", Kind: "passphrase", Owner: "vscode", UpdatedAt: "2026-11-27T02:00:00Z"},
		{Key: "中转2", Kind: "password", Owner: "vscode", UpdatedAt: "2026-11-20T00:00:00Z"},
	}
	profiles := []Profile{{Name: "xzt", Host: "1.2.3.4", Port: 22, Username: "root", AuthMethod: "key"}}
	view := buildCredentialsView(profiles, entries, nil)

	if len(view.Rows) != 1 {
		t.Fatalf("应有一行档案，实际 %+v", view.Rows)
	}
	r := view.Rows[0]
	if r.Target != "root@1.2.3.4:22" {
		t.Fatalf("target 拼错了：%q", r.Target)
	}
	if !r.Password.Has || len(r.Password.Owners) != 1 || r.Password.Owners[0] != "vscode" {
		t.Fatalf("密码状态不对：%+v", r.Password)
	}
	if !r.Passphrase.Has {
		t.Fatalf("口令状态不对：%+v", r.Passphrase)
	}
	if len(view.Orphans) != 1 || view.Orphans[0].Key != "中转2" {
		t.Fatalf("孤儿没认出来：%+v", view.Orphans)
	}
	if view.IndexFile == "" {
		t.Fatal("要给界面一个清单路径")
	}
}

func TestBuildCredentialsViewDpapiCountsAsDesktop(t *testing.T) {
	profiles := []Profile{{Name: "p", Host: "h", Port: 22, Username: "u", AuthMethod: "password"}}
	view := buildCredentialsView(profiles, nil, map[string]string{"p": ""})
	if !view.Rows[0].Password.Has {
		t.Fatal("DPAPI 里有条目 → 必须算已存（清单可能还没写：老数据）")
	}
	if len(view.Rows[0].Password.Owners) != 1 || view.Rows[0].Password.Owners[0] != "desktop" {
		t.Fatalf("属主应为 desktop：%+v", view.Rows[0].Password)
	}
}

func TestRemoveDeletesSecretWhenNoSuccessor(t *testing.T) {
	isolate(t)
	store := newTestProfileStore(t)
	if err := store.upsert(Profile{Name: "old", Host: "1.1.1.1", Port: 22, Username: "root", AuthMethod: "password"}); err != nil {
		t.Fatal(err)
	}
	sec := newSecretStore()
	sec.load()
	if err := sec.set("old", "pw-123"); err != nil {
		t.Fatal(err)
	}

	if err := store.remove("old"); err != nil {
		t.Fatal(err)
	}

	after := newSecretStore()
	after.load()
	if _, ok := after.get("old"); ok {
		t.Fatal("档案删了，密码应该一起清掉（否则就是孤儿）")
	}
}

func TestRemoveMigratesSecretToRenamedProfile(t *testing.T) {
	isolate(t)
	store := newTestProfileStore(t)
	// 同一台机器：旧名字 old、新名字 new（host/port/user 完全一致）
	if err := store.upsert(Profile{Name: "old", Host: "1.1.1.1", Port: 22, Username: "root", AuthMethod: "password"}); err != nil {
		t.Fatal(err)
	}
	if err := store.upsert(Profile{Name: "new", Host: "1.1.1.1", Port: 22, Username: "root", AuthMethod: "password"}); err != nil {
		t.Fatal(err)
	}
	sec := newSecretStore()
	sec.load()
	if err := sec.set("old", "pw-123"); err != nil {
		t.Fatal(err)
	}

	if err := store.remove("old"); err != nil {
		t.Fatal(err)
	}

	after := newSecretStore()
	after.load()
	if _, ok := after.get("old"); ok {
		t.Fatal("旧名字那份应该删掉（搬走之后）")
	}
	pw, ok := after.get("new")
	if !ok || pw != "pw-123" {
		t.Fatalf("改名后密码应挂到新名字下，实际 ok=%v pw=%q", ok, pw)
	}
	// 清单也要跟着走：新名字有 desktop 条目，旧名字没有了
	var keys []string
	for _, e := range loadCredIndexLocked() {
		if e.Owner == "desktop" && e.Kind == "password" {
			keys = append(keys, e.Key)
		}
	}
	if len(keys) != 1 || keys[0] != "new" {
		t.Fatalf("清单里 desktop 条目应只有 new，实际 %v", keys)
	}
}

func TestRemoveKeepsSecretWhenAmbiguous(t *testing.T) {
	isolate(t)
	store := newTestProfileStore(t)
	for _, n := range []string{"old", "a", "b"} {
		if err := store.upsert(Profile{Name: n, Host: "1.1.1.1", Port: 22, Username: "root", AuthMethod: "password"}); err != nil {
			t.Fatal(err)
		}
	}
	sec := newSecretStore()
	sec.load()
	if err := sec.set("old", "pw-123"); err != nil {
		t.Fatal(err)
	}
	if err := store.remove("old"); err != nil {
		t.Fatal(err)
	}
	after := newSecretStore()
	after.load()
	if _, ok := after.get("old"); !ok {
		t.Fatal("分不清时不能删（删了找不回来）—— 应留成孤儿让界面处理")
	}
	if _, ok := after.get("a"); ok {
		t.Fatal("分不清时也不能随便搬给 a")
	}
}

func TestCredentialsRouteServesJSON(t *testing.T) {
	isolate(t)
	store := newTestProfileStore(t)
	if err := store.upsert(Profile{Name: "xzt", Host: "1.2.3.4", Port: 22, Username: "root", AuthMethod: "key"}); err != nil {
		t.Fatal(err)
	}
	recordCredIndex("xzt", "password", "desktop")
	mux := http.NewServeMux()
	registerCredentialRoutes(mux, store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/credentials", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 %d", rec.Code)
	}
	var view credentialsView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	if len(view.Rows) != 1 || view.Rows[0].Name != "xzt" {
		t.Fatalf("行不对：%+v", view.Rows)
	}
	if !view.Rows[0].Password.Has {
		t.Fatal("清单里有 desktop 条目 → 应显示已存")
	}
	// 关键：响应里不能出现明文/密文
	body := rec.Body.String()
	for _, bad := range []string{"pw-", "\"value\"", "\"secret\""} {
		if strings.Contains(body, bad) {
			t.Fatalf("响应里不该有 %q：%s", bad, body)
		}
	}
}


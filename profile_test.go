package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 共享档案（两个客户端读同一份）的测试。
//
// 这一组盯的是**数据安全**，不是功能好不好看：
//   - 共享文件里**不能有密码**（两边的密码各存各的系统凭据库）；
//   - 写回时**不能丢别人写的字段**（扩展侧以后加字段是迟早的事，拿本方结构体重写就是数据丢失）；
//   - 迁移老档案时**不动老文件**、只补不覆盖。

func withProfilesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	t.Setenv("BASTIONSHELL_LEGACY_PROFILES", "") // 默认关掉迁移，需要的用例自己设
	return dir
}

func readProfilesFile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, profileSharedFile))
	if err != nil {
		t.Fatalf("读共享档案失败：%v", err)
	}
	return string(data)
}

// ───────────────────────── 基本读写 ─────────────────────────

func TestProfileStoreRoundTrip(t *testing.T) {
	_ = withProfilesDir(t)
	store := newProfileStore()
	store.load()
	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("空目录应该读出 0 个档案：%+v", got)
	}

	if err := store.upsert(Profile{
		Name: "生产堡垒机", Host: "10.0.0.10", Port: 22,
		Username: "deploy", AuthMethod: "password", Mode: "bastion",
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if err := store.upsert(Profile{
		Name: "直连测试机", Host: "10.0.0.11", Port: 2222,
		Username: "root", AuthMethod: "key", PrivateKeyPath: "/home/me/.ssh/id_ed25519", Mode: "direct",
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	// 换一个 store 实例重读：必须是同两份（真的落盘了）
	again := newProfileStore()
	again.load()
	list := again.snapshot()
	if len(list) != 2 {
		t.Fatalf("应该读出 2 个档案：%+v", list)
	}
	byName := map[string]Profile{}
	for _, p := range list {
		byName[p.Name] = p
	}
	p := byName["直连测试机"]
	if p.Host != "10.0.0.11" || p.Port != 2222 || p.Username != "root" || p.AuthMethod != "key" ||
		p.PrivateKeyPath != "/home/me/.ssh/id_ed25519" || p.Mode != "direct" {
		t.Fatalf("读回来的字段不对：%+v", p)
	}
	if p.ID != p.Name {
		t.Fatalf("给前端的 ID 应该等于档案名：%q vs %q", p.ID, p.Name)
	}
}

func TestProfileStoreUpsertUpdatesInPlace(t *testing.T) {
	dir := withProfilesDir(t)
	store := newProfileStore()
	store.load()
	_ = store.upsert(Profile{Name: "a", Host: "10.0.0.1", Port: 22, Username: "u1"})
	_ = store.upsert(Profile{Name: "a", Host: "10.0.0.2", Port: 22, Username: "u2"})

	store2 := newProfileStore()
	store2.load()
	list := store2.snapshot()
	if len(list) != 1 {
		t.Fatalf("同名应该更新而不是新增：%+v", list)
	}
	if list[0].Host != "10.0.0.2" || list[0].Username != "u2" {
		t.Fatalf("更新没生效：%+v", list[0])
	}
	_ = dir
}

func TestProfileStoreRemove(t *testing.T) {
	withProfilesDir(t)
	store := newProfileStore()
	store.load()
	_ = store.upsert(Profile{Name: "a", Host: "1", Port: 22, Username: "u"})
	_ = store.upsert(Profile{Name: "b", Host: "2", Port: 22, Username: "u"})
	if err := store.remove("a"); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if list := store.snapshot(); len(list) != 1 || list[0].Name != "b" {
		t.Fatalf("删除后应该只剩 b：%+v", list)
	}
}

// ───────────────────────── 不丢字段 ─────────────────────────

func TestProfileStorePreservesUnknownFields(t *testing.T) {
	dir := withProfilesDir(t)
	// 模拟"扩展侧写进来的、桌面版不认识的字段"（$schema 之类以后很可能加）
	original := profilesHeader + "\n" + `[
  {
    "name": "生产堡垒机",
    "host": "10.0.0.10",
    "port": 22,
    "username": "deploy",
    "authMethod": "password",
    "mode": "bastion",
    "preCommand": "cd /opt/app",
    "someFutureField": { "nested": [1, 2, 3] }
  }
]
`
	if err := os.WriteFile(filepath.Join(dir, profileSharedFile), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newProfileStore()
	store.load()
	// 桌面版只改自己认识的字段（比如改端口）
	if err := store.upsert(Profile{
		Name: "生产堡垒机", Host: "10.0.0.10", Port: 2200,
		Username: "deploy", AuthMethod: "password", Mode: "bastion",
	}); err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	text := readProfilesFile(t, dir)
	for _, want := range []string{"preCommand", "cd /opt/app", "someFutureField", "nested"} {
		if !strings.Contains(text, want) {
			t.Errorf("写回时把别人写的字段丢了：%q\n%s", want, text)
		}
	}
	if !strings.Contains(text, "2200") {
		t.Errorf("该改的字段没改：\n%s", text)
	}
	// 头注释也要在
	if !strings.Contains(text, "共享这一份") {
		t.Errorf("文件头说明丢了：\n%s", text)
	}
}

// ───────────────────────── 凭据 ─────────────────────────

func TestProfileStoreNeverWritesPassword(t *testing.T) {
	dir := withProfilesDir(t)
	store := newProfileStore()
	store.load()
	// 即使调用方（比如前端老代码）传了密码字段，也不该落进共享文件
	_ = store.upsert(Profile{
		Name: "p", Host: "h", Port: 22, Username: "u", AuthMethod: "password",
	})
	text := readProfilesFile(t, dir)
	for _, bad := range []string{"password\"", "P@ssw0rd", "passphrase"} {
		if strings.Contains(text, bad) && bad != "password\"" {
			t.Errorf("共享文件里出现了凭据相关字段 %q：\n%s", bad, text)
		}
	}
	// authMethod=password 是合法字段，但绝不能有 password/passphrase 这种"值"字段
	var arr []map[string]any
	if err := parseJSONC(text, &arr); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	for _, e := range arr {
		for k := range e {
			if k == "password" || k == "passphrase" || k == "secret" {
				t.Fatalf("共享文件里不该有 %q 字段：%+v", k, e)
			}
		}
	}
}

// ───────────────────────── 迁移老档案 ─────────────────────────

func TestProfileStoreMigratesLegacyDesktopFile(t *testing.T) {
	dir := withProfilesDir(t)
	legacy := filepath.Join(dir, "legacy-profiles.json")
	legacyBody := `{
  "profiles": [
    { "name": "老档案A", "host": "10.1.1.1", "port": 22, "user": "olduser", "password": "PLAINTEXT-SECRET", "key": "" },
    { "name": "老档案B", "host": "10.1.1.2", "port": 2222, "user": "keyuser", "password": "", "key": "C:\\keys\\id_rsa" }
  ]
}`
	if err := os.WriteFile(legacy, []byte(legacyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASTIONSHELL_LEGACY_PROFILES", legacy)

	store := newProfileStore()
	store.load()
	list := store.snapshot()
	if len(list) != 2 {
		t.Fatalf("应该迁进 2 个档案：%+v", list)
	}
	byName := map[string]Profile{}
	for _, p := range list {
		byName[p.Name] = p
	}
	if p := byName["老档案A"]; p.Username != "olduser" || p.AuthMethod != "password" {
		t.Fatalf("迁移映射不对（user→username / authMethod）：%+v", p)
	}
	if p := byName["老档案B"]; p.Username != "keyuser" || p.AuthMethod != "key" || p.PrivateKeyPath != `C:\keys\id_rsa` {
		t.Fatalf("迁移映射不对（key→privateKeyPath）：%+v", p)
	}

	text := readProfilesFile(t, dir)
	if strings.Contains(text, "PLAINTEXT-SECRET") {
		t.Fatalf("⚠️ 老文件里的明文密码被迁进共享文件了：\n%s", text)
	}
	// 老文件必须原样还在（我们不删用户的东西）
	if got, err := os.ReadFile(legacy); err != nil || string(got) != legacyBody {
		t.Fatalf("迁移不该动老文件：%v / %s", err, got)
	}
}

func TestProfileStoreMigrationDoesNotOverwriteShared(t *testing.T) {
	dir := withProfilesDir(t)
	// 共享文件里已经有同名档案（用户在扩展里建的），迁移不能覆盖它
	shared := profilesHeader + "\n" + `[
  { "name": "同名", "host": "9.9.9.9", "port": 22, "username": "from-extension", "authMethod": "password", "mode": "bastion" }
]
`
	if err := os.WriteFile(filepath.Join(dir, profileSharedFile), []byte(shared), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacy, []byte(`{"profiles":[{"name":"同名","host":"1.1.1.1","port":22,"user":"from-desktop"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASTIONSHELL_LEGACY_PROFILES", legacy)

	store := newProfileStore()
	store.load()
	list := store.snapshot()
	if len(list) != 1 {
		t.Fatalf("同名不该变成两条：%+v", list)
	}
	if list[0].Username != "from-extension" {
		t.Fatalf("共享文件里那份才是真源，不该被老文件覆盖：%+v", list[0])
	}
}

func TestProfileStoreMigrationDisabledByEmptyOverride(t *testing.T) {
	dir := withProfilesDir(t)
	legacy := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacy, []byte(`{"profiles":[{"name":"x","host":"1","port":22,"user":"u"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// 没设 BASTIONSHELL_LEGACY_PROFILES 时不该去读真实的用户档案；
	// 这里显式设成空串 = 关闭迁移
	store := newProfileStore()
	store.load()
	if list := store.snapshot(); len(list) != 0 {
		t.Fatalf("迁移关闭时不该有档案：%+v", list)
	}
}

// ───────────────────────── 细节 ─────────────────────────

func TestProfileDefaultsAndNormalization(t *testing.T) {
	dir := withProfilesDir(t)
	body := profilesHeader + "\n" + `[
  { "name": "没写端口" },
  { "name": "写了私钥但没写方式", "privateKeyPath": "/k/id_rsa" },
  { "name": "直接模式", "mode": "direct" }
]
`
	if err := os.WriteFile(filepath.Join(dir, profileSharedFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newProfileStore()
	store.load()
	list := store.snapshot()
	if len(list) != 3 {
		t.Fatalf("应该 3 条：%+v", list)
	}
	if list[0].Port != 22 {
		t.Fatalf("没写端口应该默认 22：%+v", list[0])
	}
	if list[0].Mode != "bastion" {
		t.Fatalf("没写模式应该默认 bastion：%+v", list[0])
	}
	if list[1].AuthMethod != "password" {
		t.Fatalf("没写认证方式应该默认 password：%+v", list[1])
	}
	if list[2].Mode != "direct" {
		t.Fatalf("写了 direct 就要保留：%+v", list[2])
	}
}

func TestProfileSwitchFromKeyToPasswordClearsPath(t *testing.T) {
	withProfilesDir(t)
	store := newProfileStore()
	store.load()
	_ = store.upsert(Profile{Name: "p", Host: "h", Port: 22, Username: "u", AuthMethod: "key", PrivateKeyPath: "/k"})
	_ = store.upsert(Profile{Name: "p", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	store2 := newProfileStore()
	store2.load()
	if got := store2.snapshot()[0]; got.PrivateKeyPath != "" || got.AuthMethod != "password" {
		t.Fatalf("改回密码认证时应该清掉私钥路径（否则留一个不生效的路径）：%+v", got)
	}
}

func TestProfileFilePermissions(t *testing.T) {
	dir := withProfilesDir(t)
	store := newProfileStore()
	store.load()
	_ = store.upsert(Profile{Name: "p", Host: "h", Port: 22, Username: "u"})
	info, err := os.Stat(filepath.Join(dir, profileSharedFile))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("共享档案权限应该是 0600，得到 %o", perm)
		}
	}
}

func TestProfileRejectsEmptyName(t *testing.T) {
	withProfilesDir(t)
	store := newProfileStore()
	store.load()
	if err := store.upsert(Profile{Name: "   ", Host: "h"}); err == nil {
		t.Fatal("空档案名必须拒绝（它会变成文件里一条无名记录）")
	}
}

// 界面 → REST → 共享文件 的整合测试。
//
// 桌面版界面（shim）现在调的就是这几条路由；字段名必须和共享文件对得上，
// 而且界面自己那些**只对界面有意义**的字段（hasStoredPassword / id）**不能写进共享文件**。
func TestProfileRoutesSharedFile(t *testing.T) {
	dir := withProfilesDir(t)
	mux := http.NewServeMux()
	registerProfileRoutes(mux, newProfileStore())

	// shim 发过来的形状（含桌面端私有字段）
	body := `{"id":"生产堡垒机","name":"生产堡垒机","host":"10.0.0.10","port":22,` +
		`"username":"deploy","authMethod":"password","mode":"bastion","hasStoredPassword":true}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/profiles", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST 应该成功，得到 %d：%s", rec.Code, rec.Body.String())
	}

	// GET 要能读回来，并带上界面要的 id
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/profiles", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 应该成功，得到 %d", rec.Code)
	}
	var list []Profile
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析 GET 结果失败：%v", err)
	}
	if len(list) != 1 || list[0].Name != "生产堡垒机" || list[0].ID != "生产堡垒机" || list[0].Username != "deploy" {
		t.Fatalf("GET 结果不对：%+v", list)
	}

	// 共享文件里**不该有**界面私有字段
	text := readProfilesFile(t, dir)
	if strings.Contains(text, "hasStoredPassword") {
		t.Fatalf("界面私有字段被写进了共享文件（扩展侧会看到莫名其妙的东西）：\n%s", text)
	}

	// DELETE 用 ?name= 删
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/profiles?name="+url.QueryEscape("生产堡垒机"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE 应该成功，得到 %d", rec.Code)
	}
	reloaded := newProfileStore()
	reloaded.load()
	if left := reloaded.snapshot(); len(left) != 0 {
		t.Fatalf("删除后共享文件里不该还有档案：%+v", left)
	}
}

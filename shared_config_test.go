package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 共享配置（转发规则 / 快捷命令）的测试。
//
// 和连接档案同一套纪律，所以这里盯的也是同一件事：**别丢对方写的字段**。
// 桌面版的结构体里没有 `label`/`profileId`（转发）和 `description`/`sendEnter`/`file`（快捷命令），
// 而扩展侧有 —— 拿结构体整体重写就会把这些字段抹掉。

func withSharedConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	t.Setenv("BASTIONSHELL_LEGACY_FORWARDS", "")
	return dir
}

func TestForwardStoreSharedFileRoundTrip(t *testing.T) {
	dir := withSharedConfigDir(t)
	store := newForwardStore()
	store.load()
	if list := store.snapshot(); len(list) != 0 {
		t.Fatalf("空目录应该 0 条：%+v", list)
	}
	if err := store.upsert(forwardRule{ID: "r1", LocalPort: 15432, RemoteHost: "10.0.0.5", RemotePort: 5432}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if err := store.upsert(forwardRule{ID: "r2", LocalHost: "0.0.0.0", LocalPort: 18080, RemoteHost: "10.0.0.6", RemotePort: 80}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	again := newForwardStore()
	again.load()
	list := again.snapshot()
	if len(list) != 2 {
		t.Fatalf("应该 2 条：%+v", list)
	}
	if list[0].LocalHost != "127.0.0.1" {
		t.Fatalf("没写 localHost 应该默认 127.0.0.1：%+v", list[0])
	}
	if list[1].LocalHost != "0.0.0.0" || list[1].LocalPort != 18080 {
		t.Fatalf("第二条不对：%+v", list[1])
	}
	if _, err := os.Stat(filepath.Join(dir, forwardRulesSharedFile)); err != nil {
		t.Fatalf("共享转发规则文件没落盘：%v", err)
	}
}

func TestForwardStorePreservesExtensionFields(t *testing.T) {
	dir := withSharedConfigDir(t)
	// 扩展侧写出来的样子：有 label / profileId
	body := forwardRulesHeader + "\n" + `[
  {
    "id": "r1",
    "profileId": "生产堡垒机",
    "label": "本地 15432 → 库",
    "localHost": "127.0.0.1",
    "localPort": 15432,
    "remoteHost": "10.0.0.5",
    "remotePort": 5432,
    "someFutureField": true
  }
]
`
	if err := os.WriteFile(filepath.Join(dir, forwardRulesSharedFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newForwardStore()
	store.load()
	// 桌面版只改自己认识的字段
	if err := store.upsert(forwardRule{ID: "r1", LocalPort: 15433, RemoteHost: "10.0.0.5", RemotePort: 5432}); err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	text, _ := os.ReadFile(filepath.Join(dir, forwardRulesSharedFile))
	for _, want := range []string{"profileId", "生产堡垒机", "label", "本地 15432 → 库", "someFutureField", "15433"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("写回丢了字段或没改到：%q\n%s", want, text)
		}
	}
}

func TestForwardStoreMigratesLegacy(t *testing.T) {
	dir := withSharedConfigDir(t)
	legacy := filepath.Join(dir, "legacy-forwards.json")
	legacyBody := `{"forwards":[{"id":"old1","localHost":"127.0.0.1","localPort":13306,"remoteHost":"10.1.1.1","remotePort":3306}]}`
	if err := os.WriteFile(legacy, []byte(legacyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASTIONSHELL_LEGACY_FORWARDS", legacy)

	store := newForwardStore()
	store.load()
	list := store.snapshot()
	if len(list) != 1 || list[0].ID != "old1" || list[0].LocalPort != 13306 {
		t.Fatalf("迁移结果不对：%+v", list)
	}
	// label 要自动补一个（扩展侧界面按 label 显示）
	text, _ := os.ReadFile(filepath.Join(dir, forwardRulesSharedFile))
	if !strings.Contains(string(text), "label") {
		t.Fatalf("迁移时该补 label：\n%s", text)
	}
	// 老文件不动
	if got, err := os.ReadFile(legacy); err != nil || string(got) != legacyBody {
		t.Fatalf("迁移不该动老文件：%v", err)
	}
}

func TestQuickCommandStoreSharedFileAndPreservesFields(t *testing.T) {
	dir := withSharedConfigDir(t)
	// 扩展侧写的：command 是数组、还有 description/sendEnter
	body := quickCommandsHeader + "\n" + `[
  { "id": "q1", "label": "看磁盘", "command": ["df -h", "du -sh /var"], "description": "排查磁盘", "sendEnter": true }
]
`
	if err := os.WriteFile(filepath.Join(dir, quickCommandsSharedFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newQuickCommandStore()
	store.load()
	list := store.snapshot()
	if len(list) != 1 || list[0].Label != "看磁盘" {
		t.Fatalf("读共享快捷命令失败：%+v", list)
	}
	// 数组型 command：展示取首行，但**文件里不能被改掉**
	if list[0].Command != "df -h" {
		t.Fatalf("数组型 command 展示应该取首行：%+v", list[0])
	}
	if err := store.upsert(quickCommand{ID: "q2", Label: "看内存", Command: "free -m"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	text, _ := os.ReadFile(filepath.Join(dir, quickCommandsSharedFile))
	for _, want := range []string{"description", "sendEnter", "du -sh /var", "看内存"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("写回丢了字段：%q\n%s", want, text)
		}
	}
}

func TestQuickCommandRoutes(t *testing.T) {
	dir := withSharedConfigDir(t)
	mux := http.NewServeMux()
	registerQuickCommandRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/quick-commands",
		strings.NewReader(`{"id":"q1","label":"uptime","command":"uptime"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST 应该成功：%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/quick-commands", nil))
	var list []quickCommand
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("GET 结果不对：%v / %s", err, rec.Body.String())
	}
	if list[0].Label != "uptime" {
		t.Fatalf("字段没对上：%+v", list[0])
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/quick-commands?id=q1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE 应该成功：%d", rec.Code)
	}
	// 落的是共享文件
	if _, err := os.Stat(filepath.Join(dir, quickCommandsSharedFile)); err != nil {
		t.Fatalf("共享快捷命令文件应在共享目录里：%v", err)
	}
}

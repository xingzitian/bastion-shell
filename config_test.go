package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// routeBody 只挂 mcp-info 这一条路由，单独测它的返回（不启动整个后端）
func routeBody(t *testing.T, path string) string {
	t.Helper()
	mux := http.NewServeMux()
	registerMcpInfoRoute(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

// 这一组测试盯的是「配置/版本这类会悄悄跑偏的东西」：
// 版本号三处不一致、token 落到错的目录、把 token 泄露给不鉴权的后端 ——
// 都属于不会报错、但真出事的那种。

func TestVersionMatchesWailsJSON(t *testing.T) {
	// version 会被写进 MCP 的 serverInfo 给 AI 看；对不上就会让
	// 「你用的是哪个版本」变成猜谜。三处必须一致。
	raw, err := os.ReadFile("wails.json")
	if err != nil {
		t.Fatalf("读 wails.json 失败：%v", err)
	}
	var cfg struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"Info"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("解析 wails.json 失败：%v", err)
	}
	if cfg.Info.ProductVersion != version {
		t.Errorf("wails.json 的 productVersion=%q，version.go 里是 %q —— 改一处记得改另一处",
			cfg.Info.ProductVersion, version)
	}

	raw, err = os.ReadFile(filepath.Join("frontend", "package.json"))
	if err != nil {
		t.Fatalf("读 frontend/package.json 失败：%v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("解析 frontend/package.json 失败：%v", err)
	}
	if pkg.Version != version {
		t.Errorf("frontend/package.json 的 version=%q，version.go 里是 %q", pkg.Version, version)
	}
}

func TestConfigDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_CONFIG_DIR", dir)
	if got := bastionConfigDir(); got != dir {
		t.Fatalf("BASTIONSHELL_CONFIG_DIR 没生效：%q vs %q", got, dir)
	}
	if got := mcpConfigPath(); got != filepath.Join(dir, "mcp.json") {
		t.Fatalf("mcp.json 路径不对：%q", got)
	}
}

func TestLoadOrCreateMcpConfigKeepsToken(t *testing.T) {
	t.Setenv("BASTIONSHELL_CONFIG_DIR", t.TempDir())

	first := loadOrCreateMcpConfig()
	if len(first.Token) != 64 {
		t.Fatalf("应该生成 64 字符 token，得到 %q", first.Token)
	}
	if first.Port != defaultMcpPort {
		t.Fatalf("默认端口应该是 %d，得到 %d", defaultMcpPort, first.Port)
	}
	// 文件必须存在且权限收紧（token 等价于「能在这些服务器上执行任意命令」）。
	// ⚠️ Windows 上 Go 不映射 POSIX 权限位（写 0600 读回来是 0666），
	// 那边的保护来自 %AppData% 本身的用户隔离（以及 NTFS ACL），所以只在这之外的平台断言。
	info, err := os.Stat(mcpConfigPath())
	if err != nil {
		t.Fatalf("mcp.json 没落盘：%v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mcp.json 权限应该是 0600，得到 %o", perm)
		}
	}

	// 第二次读必须**沿用**同一个 token：每次启动换 token 会让配好的客户端全部失效
	second := loadOrCreateMcpConfig()
	if second.Token != first.Token {
		t.Fatalf("token 不该变：%q → %q", first.Token, second.Token)
	}
}

func TestLoadOrCreateMcpConfigRecoversFromGarbage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte("{坏了"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadOrCreateMcpConfig()
	if len(cfg.Token) != 64 {
		t.Fatalf("配置坏了应该重新生成 token，得到 %q", cfg.Token)
	}
}

func TestMcpDisabledEnvSwitch(t *testing.T) {
	t.Setenv("BASTIONSHELL_MCP", "0")
	if !mcpDisabled() {
		t.Fatal("BASTIONSHELL_MCP=0 应该关掉端点")
	}
	t.Setenv("BASTIONSHELL_MCP", "1")
	if mcpDisabled() {
		t.Fatal("BASTIONSHELL_MCP=1 不该关掉端点")
	}
	t.Setenv("BASTIONSHELL_MCP", "")
	if mcpDisabled() {
		t.Fatal("默认（未设置）应该是开启")
	}
}

func TestMcpInfoRouteNeverLeaksToken(t *testing.T) {
	// 内部后端（18090）是 CORS `*` 且完全不鉴权的 —— 把能执行任意命令的
	// token 交给任意网页就是引狼入室。这条测试盯着这件事。
	t.Setenv("BASTIONSHELL_CONFIG_DIR", t.TempDir())
	cfg := loadOrCreateMcpConfig()
	token := cfg.Token

	body := routeBody(t, "/api/mcp-info")
	if strings.Contains(body, token) {
		t.Fatal("/api/mcp-info 泄露了 MCP token")
	}
	if !strings.Contains(body, "configPath") {
		t.Fatal("/api/mcp-info 应告诉用户 token 在哪个文件里")
	}
	if !strings.Contains(body, "toolCount") {
		t.Fatal("/api/mcp-info 应报告工具个数")
	}
}

func TestPortInUseDetection(t *testing.T) {
	// 起一个真实监听，portInUse 必须能探到；探不到的端口不该误报
	srv, err := startMcpHTTP(newMcpServer(&fakeAPI{}), "t", "127.0.0.1", 0, nil)
	if err != nil {
		t.Fatalf("起端点失败：%v", err)
	}
	defer srv.Close()
	if !portInUse("127.0.0.1", srv.port) {
		t.Fatalf("端口 %d 明明有人听，portInUse 却报没占", srv.port)
	}
	if portInUse("127.0.0.1", 0) {
		t.Fatal("端口 0 不该被判为占用")
	}
}

func TestStartMcpHTTPFallsBackWhenPortBusy(t *testing.T) {
	// 固定端口被占时必须退到随机端口，并在句柄上标出来 ——
	// 两个实例（桌面版 + VS Code 扩展）同时开着是常态。
	first, err := startMcpHTTP(newMcpServer(&fakeAPI{}), "t1", "127.0.0.1", 0, nil)
	if err != nil {
		t.Fatalf("第一个端点起不来：%v", err)
	}
	defer first.Close()

	second, err := startMcpHTTP(newMcpServer(&fakeAPI{}), "t2", "127.0.0.1", first.port, nil)
	if err != nil {
		t.Fatalf("第二个端点应该退到随机端口，而不是失败：%v", err)
	}
	defer second.Close()
	if !second.portFallback {
		t.Fatal("退到随机端口时 portFallback 必须是 true（UI/日志要能说清）")
	}
	if second.port == first.port {
		t.Fatalf("两个端点不该同端口：%d", second.port)
	}
}

func TestMcpEndpointInfoWhenNotStarted(t *testing.T) {
	stopMcpServer()
	if _, _, _, ok := mcpEndpointInfo(); ok {
		t.Fatal("端点没启动时 mcpEndpointInfo 应该报 ok=false")
	}
}

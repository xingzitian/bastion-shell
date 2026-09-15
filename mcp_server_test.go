package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MCP 传输层的测试：用**真实 HTTP 端点 + 真实 JSON-RPC**（httptest），
// 只把会话能力换成假实现。这样协议层（鉴权 / 状态码 / 错误码 / 通知）全部被测到，
// 又不需要开窗口、不需要真会话。
//
// 协议行为是照 VS Code 扩展的 mcpServer.ts 对齐的 —— 那边已用官方 MCP SDK
// 做过互操作验证（13/13），这里的用例就是那些行为的回归网。

type fakeAPI struct {
	lastCommand    string
	lastTerminal   string
	lastAction     string
	lastProfile    string
	lastLocalPath  string
	lastRemotePath string
	err            error
}

func (f *fakeAPI) ListSessions() (string, error) { return "会话列表", f.err }
func (f *fakeAPI) Health() (string, error)       { return "自检正常", f.err }
func (f *fakeAPI) ListProfiles() (string, error) { return "档案列表", f.err }
func (f *fakeAPI) Tail(terminal string, lines int) (string, error) {
	f.lastTerminal = terminal
	return "屏幕内容", f.err
}
func (f *fakeAPI) Exec(command, terminal string) (string, error) {
	f.lastCommand, f.lastTerminal = command, terminal
	return "命令输出", f.err
}
func (f *fakeAPI) Habits(action, profile, habit, privilege string) (string, error) {
	f.lastAction, f.lastProfile = action, profile
	return "习惯内容", f.err
}
func (f *fakeAPI) Push(localPath, remoteDir, terminal string) (string, error) {
	f.lastLocalPath, f.lastTerminal = localPath, terminal
	return "上传结果", f.err
}
func (f *fakeAPI) Pull(remotePath, localDir, terminal string) (string, error) {
	f.lastRemotePath, f.lastTerminal = remotePath, terminal
	return "下载结果", f.err
}

const testToken = "0123456789abcdef0123456789abcdef"

func newTestMcpServer(t *testing.T) (*httptest.Server, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{}
	srv := httptest.NewServer(mcpHTTPHandler(newMcpServer(api), testToken, nil))
	t.Cleanup(srv.Close)
	return srv, api
}

func rpc(t *testing.T, srv *httptest.Server, token string, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestMcpInitializeHandshake(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	resp, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	res, _ := body["result"].(map[string]any)
	if res == nil {
		t.Fatalf("没有 result：%v", body)
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("协议版本没回对：%v", res["protocolVersion"])
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "bastionshell" {
		t.Fatalf("serverInfo.name 必须是 bastionshell（连字符会被客户端清洗）：%v", info)
	}
	if info["version"] != version {
		t.Fatalf("serverInfo.version = %v，想要 %v", info["version"], version)
	}
	if s, _ := res["instructions"].(string); !strings.Contains(s, "bastion_exec") {
		t.Fatal("initialize 应该带上使用说明（模型看得到它）")
	}
	if id, _ := body["id"].(float64); id != 1 {
		t.Fatalf("id 必须原样回显：%v", body["id"])
	}
}

func TestMcpInitializeFallsBackToLatestProtocol(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	res, _ := body["result"].(map[string]any)
	if res["protocolVersion"] != latestProtocolVersion {
		t.Fatalf("不认识的版本应该回最新的，得到 %v", res["protocolVersion"])
	}
}

func TestMcpToolsList(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res, _ := body["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) != 8 {
		t.Fatalf("桌面版应该暴露 8 个工具，得到 %d 个", len(tools))
	}
	names := map[string]bool{}
	readOnly := map[string]bool{}
	for _, raw := range tools {
		tl, _ := raw.(map[string]any)
		name, _ := tl["name"].(string)
		names[name] = true
		ann, _ := tl["annotations"].(map[string]any)
		readOnly[name], _ = ann["readOnlyHint"].(bool)
		if _, ok := tl["inputSchema"]; !ok {
			t.Errorf("%s 缺少 inputSchema", name)
		}
	}
	for _, want := range []string{"bastion_listSessions", "bastion_health", "bastion_tail", "bastion_listProfiles", "bastion_exec", "bastion_habits", "bastion_push", "bastion_pull"} {
		if !names[want] {
			t.Errorf("缺少工具 %s", want)
		}
	}
	if !readOnly["bastion_listSessions"] || !readOnly["bastion_tail"] || !readOnly["bastion_health"] || !readOnly["bastion_listProfiles"] {
		t.Error("只读工具必须标 readOnlyHint（客户端据此不弹确认框）")
	}
	if readOnly["bastion_exec"] || readOnly["bastion_habits"] || readOnly["bastion_push"] || readOnly["bastion_pull"] {
		t.Error("有副作用的工具（exec / habits / push / pull）绝不能标成只读")
	}
}

func TestMcpToolsCallDispatches(t *testing.T) {
	srv, api := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"bastion_exec","arguments":{"command":"uptime","terminal":"deploy@web"}}}`)
	res, _ := body["result"].(map[string]any)
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if first["text"] != "命令输出" {
		t.Fatalf("工具返回没透传：%v", content)
	}
	if api.lastCommand != "uptime" || api.lastTerminal != "deploy@web" {
		t.Fatalf("参数没传对：cmd=%q terminal=%q", api.lastCommand, api.lastTerminal)
	}
}

func TestMcpToolsCallMissingName(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"arguments":{}}}`)
	e, _ := body["error"].(map[string]any)
	if e == nil || e["code"].(float64) != -32602 {
		t.Fatalf("缺 name 应该回 -32602：%v", body)
	}
}

func TestMcpUnknownToolAndMethod(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"bastion_connect","arguments":{}}}`)
	e, _ := body["error"].(map[string]any)
	if e == nil || e["code"].(float64) != -32602 {
		t.Fatalf("未知工具应该回 -32602（桌面版没有 bastion_connect）：%v", body)
	}
	_, body = rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":6,"method":"resources/list"}`)
	e, _ = body["error"].(map[string]any)
	if e == nil || e["code"].(float64) != -32601 {
		t.Fatalf("不支持的方法应该回 -32601：%v", body)
	}
}

func TestMcpToolErrorBecomesIsErrorResult(t *testing.T) {
	// 工具内部抛错不能当成传输错误：要回一条 isError 结果，模型才读得到原因
	srv, _ := newTestMcpServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"bastion_exec","arguments":{"command":"   "}}}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	res, _ := body["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("空命令应该回 isError 结果：%v", body)
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "command") {
		t.Fatalf("错误文案要告诉模型缺什么参数：%v", first["text"])
	}
}

func TestMcpNotificationGets202(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	resp, _ := rpc(t, srv, testToken, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("通知应该回 202，得到 %d", resp.StatusCode)
	}
}

func TestMcpAuthRequired(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	for _, tok := range []string{"", "wrong-token-wrong-token-wrong-token"} {
		resp, _ := rpc(t, srv, tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token=%q 应该 401，得到 %d", tok, resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") != "Bearer" {
			t.Fatal("401 应该带 WWW-Authenticate: Bearer")
		}
	}
}

func TestMcpMethodAndPathRules(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	// GET → 405（我们不做服务端推送，不开 SSE）
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应该 405，得到 %d", resp.StatusCode)
	}
	// 别的路径 → 404
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/other", strings.NewReader("{}"))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("别的路径应该 404，得到 %d", resp2.StatusCode)
	}
}

func TestMcpBadJsonAndBatch(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{not json`)
	e, _ := body["error"].(map[string]any)
	if e == nil || e["code"].(float64) != -32700 {
		t.Fatalf("坏 JSON 应该回 -32700：%v", body)
	}
	_, body = rpc(t, srv, testToken, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	e, _ = body["error"].(map[string]any)
	if e == nil || e["code"].(float64) != -32600 {
		t.Fatalf("批量请求应该回 -32600：%v", body)
	}
	_, body = rpc(t, srv, testToken, `"just a string"`)
	e, _ = body["error"].(map[string]any)
	if e == nil || e["code"].(float64) != -32600 {
		t.Fatalf("非对象请求应该回 -32600：%v", body)
	}
}

func TestMcpPing(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":9,"method":"ping"}`)
	if _, ok := body["result"]; !ok {
		t.Fatalf("ping 应该有 result：%v", body)
	}
}

func TestMcpOversizeBodyRejected(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	big := `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"bastion_exec","arguments":{"command":"` +
		strings.Repeat("a", 2*1024*1024) + `"}}}`
	resp, _ := rpc(t, srv, testToken, big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大 body 应该 413，得到 %d", resp.StatusCode)
	}
}

func TestTokenEquals(t *testing.T) {
	if !tokenEquals("abc", "abc") {
		t.Fatal("相同 token 应该相等")
	}
	if tokenEquals("abc", "abd") || tokenEquals("abc", "ab") || tokenEquals("", "") || tokenEquals("abc", "") {
		t.Fatal("不同/空 token 不该相等")
	}
}

func TestNewMcpTokenIsRandomHex(t *testing.T) {
	a, b := newMcpToken(), newMcpToken()
	if len(a) != 64 {
		t.Fatalf("token 应该是 32 字节十六进制（64 字符），得到 %d", len(a))
	}
	if a == b {
		t.Fatal("两次生成的 token 不该一样")
	}
}

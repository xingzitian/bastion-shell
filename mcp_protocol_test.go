package main

// 桌面版 MCP 协议：dual-era（legacy ↔ 2026-07-28）、Tasks、MRTR、subscriptions/listen。
//
// 与扩展侧 src/test/mcpEra.test.ts + mrt.test.ts 一一对应。

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const modernMetaJSON = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"probe","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`

func rpcWithHeaders(t *testing.T, srvURL, token, body string, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestEraVersionList(t *testing.T) {
	if !isSupportedVersion(modernProtocolVersion) {
		t.Fatal("必须支持 2026-07-28")
	}
	if isSupportedVersion("1900-01-01") {
		t.Fatal("不该支持 1900-01-01")
	}
	if len(allProtocolVersions) < 4 {
		t.Fatalf("支持的版本太少了：%v", allProtocolVersions)
	}
}

func TestDiscoverShape(t *testing.T) {
	r := discoverResult("bastionshell", "0.9.2", "hi")
	if r["resultType"] != "complete" {
		t.Fatalf("resultType 必须是 complete：%v", r["resultType"])
	}
	vs, _ := r["supportedVersions"].([]string)
	found := false
	for _, v := range vs {
		if v == modernProtocolVersion {
			found = true
		}
	}
	if !found {
		t.Fatalf("supportedVersions 里应有 2026-07-28：%v", vs)
	}
	caps, _ := r["capabilities"].(map[string]any)
	ext, _ := caps["extensions"].(map[string]any)
	if _, ok := ext[tasksExtension]; !ok {
		t.Fatal("必须在 capabilities.extensions 里声明 Tasks 扩展")
	}
	if r["ttlMs"] == nil || r["cacheScope"] != "private" {
		t.Fatalf("discover 结果要带 ttlMs 与 cacheScope=private：%v", r)
	}
}

func TestHttpStatusForError(t *testing.T) {
	cases := []struct {
		code   int
		modern bool
		want   int
	}{
		{errUnsupported, true, 400},
		{errHeaderMismatch, true, 400},
		{errMethodMissing, true, 404},
		{errInvalidParams, true, 200},
		{errUnsupported, false, 200},
		{errMethodMissing, false, 200},
	}
	for _, c := range cases {
		if got := httpStatusForError(c.code, c.modern); got != c.want {
			t.Errorf("httpStatusForError(%d,%v)=%d，想要 %d", c.code, c.modern, got, c.want)
		}
	}
}

func TestRequestStateRoundTrip(t *testing.T) {
	s := encodeRequestState("j-0007")
	if decodeRequestState(s) != "j-0007" {
		t.Fatalf("encode/decode 不往返：%q", s)
	}
	for _, bad := range []string{"", "not-base64!!", "e30="} {
		if decodeRequestState(bad) != "" {
			t.Errorf("坏数据 %q 应解出空 jobId", bad)
		}
	}
}

func TestSecretPromptAndInputValue(t *testing.T) {
	for _, s := range []string{"Password: ", "[sudo] password for x:", "Verification code:", "动态码：", "Enter passphrase"} {
		if !isSecretPrompt(s) {
			t.Errorf("%q 应判为凭据类", s)
		}
	}
	for _, s := range []string{"(yes/no)", "请选择目标机", "[y/n]"} {
		if isSecretPrompt(s) {
			t.Errorf("%q 不该判为凭据类", s)
		}
	}
	if got := firstInputValue(map[string]any{"prompt": map[string]any{"content": "y"}}); got != "y" {
		t.Fatalf("应取到 y，得到 %q", got)
	}
	if got := firstInputValue(map[string]any{}); got != "" {
		t.Fatalf("空 responses 应返回空，得到 %q", got)
	}
}

func TestTaskSnapshotShape(t *testing.T) {
	done := taskSnapshot(jobView{JobID: "j-1", Status: statusCompleted, ExitCode: 0}, "", "")
	if done["status"] != "completed" || done["result"] == nil {
		t.Fatalf("completed 要带 result：%v", done)
	}
	failed := taskSnapshot(jobView{JobID: "j-2", Status: statusFailed, Reason: "session_lost"}, "", "")
	errObj, _ := failed["error"].(map[string]any)
	if errObj == nil || errObj["message"] != "会话已关闭" {
		t.Fatalf("session_lost 的错误文案不对：%v", failed)
	}
	need := taskSnapshot(jobView{JobID: "j-3", Status: statusInputRequired}, "Password:", "人工输入")
	if need["inputRequests"] == nil {
		t.Fatalf("input_required 要带 inputRequests：%v", need)
	}
}

func TestLegacyVsModernToolsList(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	// legacy：不带 _meta
	_, legacy := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if res, _ := legacy["result"].(map[string]any); res["resultType"] != nil || res["ttlMs"] != nil {
		t.Fatalf("legacy 不该带 resultType/ttlMs：%v", res)
	}
	// modern：带 _meta
	_, modern := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+modernMetaJSON+`}}`)
	res, _ := modern["result"].(map[string]any)
	if res["resultType"] != "complete" || res["ttlMs"] == nil || res["cacheScope"] != "private" {
		t.Fatalf("modern 应带 resultType/ttlMs/cacheScope：%v", res)
	}
}

func TestUnsupportedVersionHTTP400(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	resp, body := rpcWithHeaders(t, srv.URL, testToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01"}}}`,
		map[string]string{"MCP-Protocol-Version": "1900-01-01"})
	if resp.StatusCode != 400 {
		t.Fatalf("不支持版本应回 400，得到 %d", resp.StatusCode)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil || int(errObj["code"].(float64)) != errUnsupported {
		t.Fatalf("应回 -32022：%v", body)
	}
	data, _ := errObj["data"].(map[string]any)
	if data["supported"] == nil || data["requested"] != "1900-01-01" {
		t.Fatalf("错误里要带 supported/requested：%v", data)
	}
}

func TestHeaderMismatchHTTP400(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	resp, body := rpcWithHeaders(t, srv.URL, testToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMetaJSON+`}}`,
		map[string]string{"MCP-Protocol-Version": "2025-06-18"})
	if resp.StatusCode != 400 {
		t.Fatalf("头与 _meta 不一致应回 400，得到 %d", resp.StatusCode)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil || int(errObj["code"].(float64)) != errHeaderMismatch {
		t.Fatalf("应回 -32020：%v", body)
	}
}

func TestUnknownMethodStatus(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	// modern → 404
	resp, _ := rpcWithHeaders(t, srv.URL, testToken,
		`{"jsonrpc":"2.0","id":1,"method":"nope/nothing","params":{`+modernMetaJSON+`}}`,
		map[string]string{"MCP-Protocol-Version": modernProtocolVersion})
	if resp.StatusCode != 404 {
		t.Fatalf("modern 未知方法应 404，得到 %d", resp.StatusCode)
	}
	// legacy → 200
	resp2, _ := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":2,"method":"nope/nothing"}`)
	if resp2.StatusCode != 200 {
		t.Fatalf("legacy 未知方法应 200（老客户端会把 400/404 当端点不存在），得到 %d", resp2.StatusCode)
	}
}

func TestDiscoverOverHTTP(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	_, body := rpc(t, srv, testToken, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+modernMetaJSON+`}}`)
	res, _ := body["result"].(map[string]any)
	if res["resultType"] != "complete" {
		t.Fatalf("discover 应带 resultType：%v", res)
	}
	caps, _ := res["capabilities"].(map[string]any)
	ext, _ := caps["extensions"].(map[string]any)
	if _, ok := ext[tasksExtension]; !ok {
		t.Fatalf("discover 要声明 Tasks 扩展：%v", res)
	}
}

func TestSubscriptionsListenSSE(t *testing.T) {
	srv, _ := newTestMcpServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"filters":{"tasks":true}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("应该回 SSE，得到 %q", ct)
	}
	reader := bufio.NewReader(resp.Body)
	// 第一段：ack
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("读 SSE 失败：%v", err)
	}
	_ = line
	buf := make([]byte, 4096)
	n, _ := reader.Read(buf)
	if !strings.Contains(string(buf[:n]), "acknowledged") && !strings.Contains(string(buf[:n]), "keep-alive") {
		t.Fatalf("第一条应该是 ack：%q", string(buf[:n]))
	}
}

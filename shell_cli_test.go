package main

// CLI 薄壳的最小测试：只测**纯函数**与路由契约（不 spawn 真 node，避免测试依赖网络/档案）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractTermURL(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want string
	}{
		{
			name: "真实日志行",
			log:  "[bastion] 主机指纹（sha256）：xxx\n[bastion] MCP 已启动：http://127.0.0.1:55994/mcp\n[bastion] 浏览器打开：http://127.0.0.1:55995/term?token=abc123\n",
			want: "http://127.0.0.1:55995/term?token=abc123",
		},
		{
			name: "输出被切成两块（跨 chunk 拼接）",
			log:  "[bastion] 浏览器打开：http://127.0.0.1:520",
			want: "", // 单独一块不完整 → 不该误判
		},
		{
			name: "没有地址",
			log:  "[bastion] 连接失败",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractTermURL(c.log); got != c.want {
				t.Fatalf("extractTermURL() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestExtractTermURLAcrossChunks(t *testing.T) {
	// 分块到达：拼起来之后要能取到
	acc := "[bastion] 浏览器打开：http://127.0.0.1:52"
	acc += "999/term?token=deadbeef\n"
	if got := extractTermURL(acc); got != "http://127.0.0.1:52999/term?token=deadbeef" {
		t.Fatalf("跨块拼接失败：%q", got)
	}
}

func TestCliShellRouteRequiresProfile(t *testing.T) {
	mux := http.NewServeMux()
	registerCliShellRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/shell/cli", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 profile 应该 400，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "profile") {
		t.Fatalf("错误信息应说明要 profile：%s", rec.Body.String())
	}
}

func TestCliShellStopRejectsGet(t *testing.T) {
	mux := http.NewServeMux()
	registerCliShellRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/shell/cli/stop", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("stop 只接受 POST，实际 %d", rec.Code)
	}
}

func TestCliShellReportsMissingCliPath(t *testing.T) {
	t.Setenv("BASTIONSHELL_CLI_JS", "")
	// 清掉可能存在的兜底路径：直接断言错误信息里给了"怎么设"
	if _, err := cliScriptPath(); err == nil {
		t.Skip("本机 exe 同目录恰好有 bastion-cli/main.js，跳过")
	} else if !strings.Contains(err.Error(), "BASTIONSHELL_CLI_JS") {
		t.Fatalf("错误信息应给出设置方法：%v", err)
	}
}

func TestExtractEndpoint(t *testing.T) {
	line := "[bastion] 端点：{\"url\":\"http://127.0.0.1:39391/mcp\"," +
		"\"token\":\"tok123\",\"termUrl\":\"http://127.0.0.1:59520/term?token=tok123\"," +
		"\"port\":39391,\"actualPort\":39391,\"legacy\":true}\n"
	ep := extractEndpoint(line)
	if ep == nil {
		t.Fatal("没解析出来")
	}
	if ep.URL != "http://127.0.0.1:39391/mcp" || ep.Token != "tok123" {
		t.Fatalf("字段不对：%+v", ep)
	}
	if ep.TermURL == "" || ep.ActualPort != 39391 {
		t.Fatalf("termUrl/actualPort 不对：%+v", ep)
	}
	if extractEndpoint("没有端点行") != nil {
		t.Fatal("不该解析出东西")
	}
}
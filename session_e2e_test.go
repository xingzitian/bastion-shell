package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// jsonString 把一个字符串编成 JSON 字面量（测试里拼请求体用）
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return " + s + "
	}
	return string(b)
}

// mcpText 取 JSON-RPC 结果里第一段文本
func mcpText(body map[string]any) string {
	res, _ := body["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

// mcpPost 往真实 MCP 端点发一条 JSON-RPC
func mcpPost(t *testing.T, h *mcpHTTPHandle, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// ───────────────────────── 实机 E2E（需要 SSH 靶机） ─────────────────────────
//
// 没有靶机时整体 skip，所以 `go test ./` 在普通机器上照样是绿的。
// 靶机环境变量和 VS Code 扩展用的是同一套（见扩展的 src/test/transferE2E.test.ts）：
//
//	BASTION_E2E_SSH=127.0.0.1:2222      （host:port）
//	BASTION_E2E_USER=bastiontest
//	BASTION_E2E_KEY=...\id_ed25519_2222 （不填就按 %TEMP%\bastion-e2e\id_ed25519_<port> 找）
//
// 这一组测试是**唯一能证明 exec 引擎在真 SSH 上成立**的东西：单元测试用的是假会话，
// 真机上才会遇到 pty 回显、\r\n、MOTD、提示符这些真实的文本形态。

func e2eTarget(t *testing.T) (host string, port int, user, key string) {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv("BASTION_E2E_SSH"))
	if addr == "" {
		t.Skip("未设置 BASTION_E2E_SSH，跳过实机测试")
	}
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		t.Fatalf("BASTION_E2E_SSH 应该是 host:port，得到 %q", addr)
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("BASTION_E2E_SSH 端口不合法：%q", addr)
	}
	user = os.Getenv("BASTION_E2E_USER")
	if user == "" {
		user = "bastiontest"
	}
	key = os.Getenv("BASTION_E2E_KEY")
	if key == "" {
		key = filepath.Join(os.TempDir(), "bastion-e2e", fmt.Sprintf("id_ed25519_%d", port))
		if _, err := os.Stat(key); err != nil {
			// Windows 的 %TEMP% 和 %LOCALAPPDATA%\Temp 可能不是一个
			if local := os.Getenv("LOCALAPPDATA"); local != "" {
				key = filepath.Join(local, "Temp", "bastion-e2e", fmt.Sprintf("id_ed25519_%d", port))
			}
		}
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("找不到 E2E 私钥 %s：请设置 BASTION_E2E_KEY", key)
	}
	return parts[0], port, user, key
}

// e2eSession 对靶机开一条真会话，并把它登记进会话表（走的就是生产路径）
func e2eSession(t *testing.T) (*bastionSession, *ssh.Client) {
	t.Helper()
	host, port, user, key := e2eTarget(t)
	client, err := connect(host, port, user, "", key, "", "")
	if err != nil {
		t.Fatalf("连靶机失败（%s:%d）：%v", host, port, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	connID := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	rememberConn(connID, connMeta{Host: host, Port: port, User: user})
	sess, err := newBastionSession(sessionOpts{
		ConnID: connID,
		Client: client,
		Cols:   100,
		Rows:   30,
	})
	if err != nil {
		t.Fatalf("开会话失败：%v", err)
	}
	t.Cleanup(sess.close)
	return sess, client
}

func TestE2EExecEchoesOutputAndExitCode(t *testing.T) {
	s, _ := e2eSession(t)
	res, err := s.exec("echo hello-bastion", execOpts{QuietMs: 3000})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if !res.MarkerSeen {
		t.Fatalf("真机上必须等到哨兵；输出：%q", res.Output)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("退出码应该是 0，得到 %v（输出 %q）", res.ExitCode, res.Output)
	}
	if !strings.Contains(res.Output, "hello-bastion") {
		t.Fatalf("输出里应该有 hello-bastion，得到 %q", res.Output)
	}
}

func TestE2EExecReportsFailure(t *testing.T) {
	s, _ := e2eSession(t)
	res, err := s.exec("false", execOpts{QuietMs: 3000})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if res.ExitCode == nil || *res.ExitCode == 0 {
		t.Fatalf("false 必须拿到非 0 退出码，得到 %v", res.ExitCode)
	}
	if !strings.Contains(exitCodeNote(res), "非 0") {
		t.Fatalf("备注应该提示失败：%q", exitCodeNote(res))
	}
}

func TestE2EExecSurvivesSlowCommand(t *testing.T) {
	// 中间静默 2 秒：静止兜底窗口大于它时，应该靠哨兵拿到完整结果
	s, _ := e2eSession(t)
	res, err := s.exec("sleep 2; echo late-result", execOpts{QuietMs: 5000})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if !res.MarkerSeen || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("应该等到哨兵并拿到退出码 0，得到 %+v", res)
	}
	if !strings.Contains(res.Output, "late-result") {
		t.Fatalf("输出应该包含 late-result，得到 %q", res.Output)
	}
}

func TestE2EExecMultiLineAndQuoting(t *testing.T) {
	s, _ := e2eSession(t)
	res, err := s.exec(`printf 'a\nb\n' | wc -l`, execOpts{QuietMs: 3000})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if !strings.Contains(res.Output, "2") {
		t.Fatalf("wc -l 应该输出 2，得到 %q", res.Output)
	}
	// 引号/空格走一整圈，确认没有被注入层吃掉
	res, _ = s.exec(`echo "a b  c" | tr -s ' '`, execOpts{QuietMs: 3000})
	if !strings.Contains(res.Output, "a b c") {
		t.Fatalf("引号和空格应该原样到达远端，得到 %q", res.Output)
	}
}

func TestE2ESessionRegistryAndScreen(t *testing.T) {
	s, _ := e2eSession(t)
	if _, err := s.exec("echo registry-check", execOpts{QuietMs: 3000}); err != nil {
		t.Fatalf("exec 出错：%v", err)
	}

	// 会话应该出现在登记表里，名字是 用户@主机
	list := listSessions()
	if len(list) == 0 {
		t.Fatal("会话没登记进登记表")
	}
	found := false
	for _, x := range list {
		if x.id == s.id {
			found = true
		}
	}
	if !found {
		t.Fatalf("登记表里找不到会话 %s", s.id)
	}
	if !strings.Contains(s.name(), "@") {
		t.Fatalf("会话名应该形如 用户@主机，得到 %q", s.name())
	}

	// lookupSession 不指定名字时应该落到最近活动的会话
	got, errMsg := lookupSession("")
	if errMsg != "" || got == nil || got.id != s.id {
		t.Fatalf("lookupSession(\"\") 应该返回最近的会话，得到 %v / %q", got, errMsg)
	}

	// 真机落到 shell 之后，屏幕判定必须是 shell（否则 exec 会被自己的菜单拦截误伤）
	if state := s.screenState(); state != stateShell {
		t.Fatalf("真机 shell 提示符应该判成 shell，得到 %q（屏幕：%q）", state, s.screenTail(6))
	}
	if guard := menuGuard(s); guard != "" {
		t.Fatalf("正常 shell 不该被菜单拦截：%q", guard)
	}

	// tail（= bastion_tail）应该能看到刚才的命令回显
	tail := s.screenTail(40)
	if !strings.Contains(tail, "registry-check") {
		t.Fatalf("屏幕快照应该包含刚执行的命令，得到 %q", tail)
	}
}

func TestE2EApiLayerEndToEnd(t *testing.T) {
	s, _ := e2eSession(t)
	api := desktopSessionAPI{}

	// 先跑一条命令让会话落到 shell：**刚连上那一瞬间屏幕上还没有提示符**，
	// 这时候状态是「未知」（照旧执行，不误拦）—— 那是如实汇报，不是 bug。
	pre, err := api.Exec("echo warmup", s.name())
	if err != nil {
		t.Fatalf("warmup Exec 出错：%v", err)
	}
	if !strings.Contains(pre, "退出码 0") {
		t.Fatalf("warmup 没跑通：%q", pre)
	}

	// listSessions 必须列出这条会话，并带上状态
	out, err := api.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions 出错：%v", err)
	}
	if !strings.Contains(out, s.name()) {
		t.Fatalf("ListSessions 没列出这条会话：%q", out)
	}
	if !strings.Contains(out, "✅") {
		t.Fatalf("ListSessions 应该标出「在 shell 里」：%q", out)
	}

	// tail
	out, err = api.Tail(s.name(), 10)
	if err != nil || !strings.Contains(out, "屏幕最后 10 行") {
		t.Fatalf("Tail 返回不对：%v / %q", err, out)
	}

	// exec：终端名按名字指定，输出带退出码
	out, err = api.Exec("echo via-api", s.name())
	if err != nil {
		t.Fatalf("Exec 出错：%v", err)
	}
	if !strings.Contains(out, "via-api") || !strings.Contains(out, "退出码 0") {
		t.Fatalf("Exec 返回应该包含输出和退出码：%q", out)
	}

	// 指定不存在的会话：要说清去哪儿找，而不是猜一条
	out, _ = api.Exec("echo x", "不存在的会话名")
	if !strings.Contains(out, "找不到会话") || !strings.Contains(out, "bastion_listSessions") {
		t.Fatalf("找不到会话时的提示不合格：%q", out)
	}

	// health 自检
	out, err = api.Health()
	if err != nil || !strings.Contains(out, version) {
		t.Fatalf("Health 返回不对：%v / %q", err, out)
	}
	if !strings.Contains(out, "bastion_exec") {
		t.Fatalf("Health 应该报出工具名单：%q", out)
	}
}

func TestE2EEndpointServesRealSessionOverHttp(t *testing.T) {
	// 把整条链路串起来：真会话 + 真 HTTP 端点 + 真 JSON-RPC
	s, _ := e2eSession(t)
	t.Setenv("BASTIONSHELL_CONFIG_DIR", t.TempDir())

	handle, err := startMcpHTTP(newMcpServer(desktopSessionAPI{}), newMcpToken(), "127.0.0.1", 0, nil)
	if err != nil {
		t.Fatalf("起 MCP 端点失败：%v", err)
	}
	defer handle.Close()

	resp, body := mcpPost(t, handle, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bastion_exec","arguments":{"command":"echo over-mcp"}}}`)
	if resp != 200 {
		t.Fatalf("状态码 = %d，body = %v", resp, body)
	}
	res, _ := body["result"].(map[string]any)
	if res == nil {
		t.Fatalf("没有 result：%v", body)
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "over-mcp") {
		t.Fatalf("HTTP 那头没拿到命令输出：%q", text)
	}
	if !strings.Contains(text, "退出码 0") {
		t.Fatalf("应该带上退出码：%q", text)
	}
	if s.screenState() != stateShell && s.screenState() != stateUnknown {
		t.Fatalf("跑完命令后会话不该变成菜单态：%q", s.screenState())
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ───────────────────────── 传输的实机 E2E ─────────────────────────
//
// 靶机（见 session_e2e_test.go 的环境变量约定）**故意没有 lrzsz**，所以这里验的是
// **base64 降级通道 + 目录 tar 打包 + 回读校验**这几条 —— 恰好是"目标机什么都没装"
// 这种最难的场景。rz/sz 那条通道需要一台装了 lrzsz 的机器（见 README 的测试一节）。

// e2eRemoteTmp 在靶机上开一个临时目录，测试结束删掉
func e2eRemoteTmp(t *testing.T, s *bastionSession) string {
	t.Helper()
	res, err := s.exec("mktemp -d /tmp/bastion-e2e-XXXXXX", execOpts{QuietMs: 3000})
	if err != nil {
		t.Fatalf("建临时目录失败：%v", err)
	}
	dir := lastAbsolutePath(res.Output)
	if dir == "" {
		t.Fatalf("没拿到临时目录路径：%q", res.Output)
	}
	t.Cleanup(func() {
		_, _ = s.exec("rm -rf "+shellQuote(dir), execOpts{QuietMs: 3000})
	})
	return dir
}

// e2eLocalTmp 本机临时目录
func e2eLocalTmp(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func e2eHost(t *testing.T) (transferHost, *bastionSession) {
	t.Helper()
	s, _ := e2eSession(t)
	return newSessionTransferHost(s, "e2e"), s
}

func TestE2ETransferProbeSaysWhatTheBoxHas(t *testing.T) {
	h, _ := e2eHost(t)
	clearCapabilities("")
	caps := probeCapabilities(h, true)
	t.Logf("靶机能力：rz=%v sz=%v base64=%v tar=%v md5sum=%v %s",
		caps.Rz, caps.Sz, caps.Base64, caps.Tar, caps.MD5Sum, caps.Note)
	// 这两条是 base64 降级的前提：coreutils 一定有 base64，靶机也一定有 tar
	if !caps.Base64 {
		t.Fatal("靶机应该有 base64 —— 它是降级通道的前提")
	}
	if !caps.MD5Sum {
		t.Fatal("靶机应该有 md5sum —— 没有它就只能按大小判断（弱证据）")
	}
	// 通道选择要和能力一致
	if got := choosePushMethod(caps, false); got == "" {
		t.Fatal("至少得有 base64 这条路")
	}
	if caps.Rz && choosePushMethod(caps, false) != "rz" {
		t.Fatal("有 rz 就该优先 rz")
	}
	if !caps.Rz && choosePushMethod(caps, false) != "base64" {
		t.Fatal("没有 rz 就该降级 base64")
	}
}

func TestE2ETransferPushFileAndVerifyContent(t *testing.T) {
	h, s := e2eHost(t)
	dir := e2eRemoteTmp(t, s)
	local := e2eLocalTmp(t)

	// 文件名里有中文和空格：远端 shell 引用出错的话这一步就会挂
	content := "server {\n  listen 8080;\n}\n# 中文注释\n"
	src := filepath.Join(local, "web 配置.conf")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	out := PushPath(h, src, dir, "")
	if !out.OK {
		t.Fatalf("上传应该成功：%s", out.Message)
	}
	if out.Method != "base64" && out.Method != "rz" {
		t.Fatalf("通道不对：%s", out.Method)
	}
	if !strings.Contains(out.Message, "不要再用 ls / md5sum 自己验一遍") {
		t.Fatalf("结果里应该带证据并明确说不用再验一遍：%s", out.Message)
	}

	// **独立复核**：不看传输工具的判词，直接用另一条命令把远端文件内容读回来比
	got, err := s.exec("cat "+shellQuote(dir+"/web 配置.conf"), execOpts{QuietMs: 3000})
	if err != nil {
		t.Fatalf("复核失败：%v", err)
	}
	if !strings.Contains(got.Output, "listen 8080") || !strings.Contains(got.Output, "中文注释") {
		t.Fatalf("远端内容不对：%q", got.Output)
	}
	// 权限：传上去的文件必须能被当前账号读（否则"传上去等于白传"）
	mode, _ := s.exec("stat -c '%a %U' "+shellQuote(dir+"/web 配置.conf"), execOpts{QuietMs: 3000})
	t.Logf("远端文件权限/属主：%s", strings.TrimSpace(mode.Output))
	if strings.HasPrefix(strings.TrimSpace(mode.Output), "0") {
		t.Fatalf("⚠️ 远端文件权限是 0（读不了）—— 传上去等于白传：%s", mode.Output)
	}
}

func TestE2ETransferPushDirPacksAndExtracts(t *testing.T) {
	h, s := e2eHost(t)
	remoteDir := e2eRemoteTmp(t, s)
	local := e2eLocalTmp(t)

	srcDir := filepath.Join(local, "conf")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("B\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := PushPath(h, srcDir, remoteDir, "")
	if !out.OK {
		t.Fatalf("目录上传应该成功：%s", out.Message)
	}
	if !strings.Contains(out.Message, "目录已上传并解开") {
		t.Fatalf("文案该说清是目录：%s", out.Message)
	}
	// 解开后应该能看到目录结构和内容
	list, _ := s.exec("find "+shellQuote(remoteDir)+" -type f | sort", execOpts{QuietMs: 3000})
	if !strings.Contains(list.Output, "conf/a.txt") || !strings.Contains(list.Output, "conf/sub/b.txt") {
		t.Fatalf("解开后的结构不对：%q", list.Output)
	}
	// 临时 tar 包必须被清掉（别在远端留垃圾）
	if strings.Contains(list.Output, ".tar.gz") {
		t.Fatalf("远端留下了临时包：%q", list.Output)
	}
	// 本地临时包也要清掉
	entries, _ := os.ReadDir(local)
	for _, e := range entries {
		if strings.Contains(e.Name(), "bastion-") {
			t.Fatalf("本地留下了临时包：%s", e.Name())
		}
	}
}

func TestE2ETransferSkipExistingFile(t *testing.T) {
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "skip")
	h, s := e2eHost(t)
	dir := e2eRemoteTmp(t, s)
	local := e2eLocalTmp(t)
	src := filepath.Join(local, "keep.txt")
	if err := os.WriteFile(src, []byte("第一次\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out := PushPath(h, src, dir, ""); !out.OK {
		t.Fatalf("第一次上传应该成功：%s", out.Message)
	}
	// 改本机内容再传一次：skip 模式下**不应该**覆盖远端
	if err := os.WriteFile(src, []byte("第二次\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := PushPath(h, src, dir, "")
	if out.OK || !out.Skipped {
		t.Fatalf("skip 模式第二次应该跳过：%+v", out)
	}
	got, _ := s.exec("cat "+shellQuote(dir+"/keep.txt"), execOpts{QuietMs: 3000})
	if !strings.Contains(got.Output, "第一次") {
		t.Fatalf("远端内容被改了（skip 模式不该覆盖）：%q", got.Output)
	}
}

func TestE2ETransferOverwriteMode(t *testing.T) {
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "overwrite")
	h, s := e2eHost(t)
	dir := e2eRemoteTmp(t, s)
	local := e2eLocalTmp(t)
	src := filepath.Join(local, "swap.txt")
	if err := os.WriteFile(src, []byte("旧\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := PushPath(h, src, dir, ""); !out.OK {
		t.Fatalf("第一次上传失败：%s", out.Message)
	}
	if err := os.WriteFile(src, []byte("新\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := PushPath(h, src, dir, "")
	if !out.OK {
		t.Fatalf("overwrite 模式应该成功：%s", out.Message)
	}
	got, _ := s.exec("cat "+shellQuote(dir+"/swap.txt"), execOpts{QuietMs: 3000})
	if !strings.Contains(got.Output, "新") {
		t.Fatalf("远端内容没被覆盖：%q", got.Output)
	}
	if !strings.Contains(out.Message, "逐字节一致") {
		t.Fatalf("覆盖后应该带回读证据：%s", out.Message)
	}
}

func TestE2ETransferPullFile(t *testing.T) {
	h, s := e2eHost(t)
	remoteDir := e2eRemoteTmp(t, s)
	localSrc := e2eLocalTmp(t)
	localDst := e2eLocalTmp(t)

	content := strings.Repeat("下载内容 line\n", 500)
	src := filepath.Join(localSrc, "pull me.txt")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := PushPath(h, src, remoteDir, ""); !out.OK {
		t.Fatalf("先上传失败：%s", out.Message)
	}

	out := PullPath(h, remoteDir+"/pull me.txt", localDst, "")
	if !out.OK {
		t.Fatalf("下载应该成功：%s", out.Message)
	}
	got, err := os.ReadFile(out.LocalPath)
	if err != nil {
		t.Fatalf("读落盘文件失败：%v", err)
	}
	if string(got) != content {
		t.Fatalf("拉回来的内容不一致（本机 %d 字节，远端 %d 字节）", len(content), len(got))
	}
}

func TestE2ETransferPullMissingFileFails(t *testing.T) {
	h, s := e2eHost(t)
	dir := e2eRemoteTmp(t, s)
	out := PullPath(h, dir+"/根本没有这个文件.txt", e2eLocalTmp(t), "")
	if out.OK {
		t.Fatal("远端没有这个文件必须失败")
	}
	if !strings.Contains(out.Message, "不存在或当前账号读不到") && !strings.Contains(out.Message, "没能从远端拿到") {
		t.Fatalf("失败原因要说清楚：%s", out.Message)
	}
}

func TestE2ETransferBadRemoteDirIsReportedNotSilentlyElsewhere(t *testing.T) {
	h, _ := e2eHost(t)
	local := e2eLocalTmp(t)
	src := filepath.Join(local, "a.txt")
	if err := os.WriteFile(src, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := PushPath(h, src, "/no/such/dir-"+randomToken(4), "")
	if out.OK {
		t.Fatal("远端目录进不去时必须失败")
	}
	if !strings.Contains(out.Message, "进不去") {
		t.Fatalf("要说清是目录进不去，而不是偷偷传到别处：%s", out.Message)
	}
}

func TestE2ETransferToolExposedOverMcp(t *testing.T) {
	// 把整条链路串起来：真会话 + 真端点 + 真 JSON-RPC + 真文件传输
	s, _ := e2eSession(t)
	dir := e2eRemoteTmp(t, s)
	local := e2eLocalTmp(t)
	src := filepath.Join(local, "via-mcp.txt")
	if err := os.WriteFile(src, []byte("mcp 传上来的\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASTIONSHELL_CONFIG_DIR", t.TempDir())

	handle, err := startMcpHTTP(newMcpServer(desktopSessionAPI{}), newMcpToken(), "127.0.0.1", 0, nil)
	if err != nil {
		t.Fatalf("起 MCP 端点失败：%v", err)
	}
	defer handle.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bastion_push","arguments":{"localPath":` +
		jsonString(src) + `,"remoteDir":` + jsonString(dir) + `}}}`
	status, resp := mcpPost(t, handle, body)
	if status != 200 {
		t.Fatalf("状态码 %d：%v", status, resp)
	}
	text := mcpText(resp)
	if !strings.Contains(text, "✅ 已上传") {
		t.Fatalf("通过 MCP 上传应该成功：%s", text)
	}
	// 反过来拉回本机
	body2 := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bastion_pull","arguments":{"remotePath":` +
		jsonString(dir+"/via-mcp.txt") + `,"localDir":` + jsonString(local) + `}}}`
	status, resp = mcpPost(t, handle, body2)
	if status != 200 {
		t.Fatalf("状态码 %d：%v", status, resp)
	}
	if got := mcpText(resp); !strings.Contains(got, "已下载") {
		t.Fatalf("通过 MCP 下载应该成功：%s", got)
	}
}

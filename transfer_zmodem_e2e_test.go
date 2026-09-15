package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ───────────────────── 实机 E2E：rz / sz（zmodem）通道 ─────────────────────
//
// 和 transfer_e2e_test.go 的区别：那一组验的是 **base64 降级**（靶机上什么都没装的场景），
// 这一组验的是**首选的 rz/sz 通道** —— 它要求**目标机装了 lrzsz**，所以单独一组、
// 单独用环境变量开：
//
//	BASTION_E2E_ZMODEM=1          # 明确表示"这次靶机上有 lrzsz"
//	BASTION_E2E_SSH=127.0.0.1:2222
//	...
//
// ⚠️ 这一组必须**单独一次 go test 调用**跑：`initLrzsz()` 会把全局的传输模式切成
// zmodem（Windows 版真实运行时的模式），而其它用例是在 trzsz 模式下写的断言。
//
//	go test -run TestE2EZmodem -v ./     # 只跑这一组
func e2eZmodem(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("BASTION_E2E_ZMODEM")) == "" {
		t.Skip("未设置 BASTION_E2E_ZMODEM，跳过 rz/sz 通道的实机测试（需要靶机装 lrzsz）")
	}
	initLrzsz()
	if !useZmodem {
		t.Fatalf("这台机器上 initLrzsz() 没能启用 zmodem（内嵌 lrzsz 缺失？），当前 useZmodem=%v", useZmodem)
	}
}

// TestE2EZmodemRzPushAndPermissions 【重要】rz 上传 + **远端权限实测**
//
// 这条测试回答一个悬了很久的问题：桌面版的内嵌 lrzsz 上传时，
// 远端文件的权限位到底对不对（扩展侧曾经传出 0000 的文件，那批文件服务读不了）。
// 判定不看猜测：传完直接在远端 `stat -c %a` + `[ -r ]` + `md5sum` 三样一起看。
func TestE2EZmodemRzPushAndPermissions(t *testing.T) {
	e2eZmodem(t)
	h, s := e2eHost(t)
	clearCapabilities("")
	caps := probeCapabilities(h, true)
	if !caps.Rz {
		t.Fatalf("靶机没探测到 rz —— 这一组测试的前提是目标机装了 lrzsz：%+v", caps)
	}
	dir := e2eRemoteTmp(t, s)
	local := e2eLocalTmp(t)

	content := "server {\n  listen 8080;\n}\n# 中文注释\n"
	src := filepath.Join(local, "rz 上传.conf")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	out := PushPath(h, src, dir, "")
	if !out.OK {
		t.Fatalf("rz 上传应该成功：%s", out.Message)
	}
	if out.Method != "rz" {
		t.Fatalf("有 rz 就该走 rz，得到 %q", out.Method)
	}

	remoteFile := dir + "/rz 上传.conf"
	// ① 权限：**这就是「0000 到底有没有」的答案**
	mode, _ := s.exec("stat -c '%a %U %G' "+shellQuote(remoteFile), execOpts{QuietMs: 3000})
	modeText := strings.TrimSpace(mode.Output)
	t.Logf("【权限实测】远端文件权限/属主：%s", modeText)
	if strings.HasPrefix(modeText, "0") {
		t.Fatalf("⚠️ 桌面版 rz 上传把远端文件权限写成了 0（读不了，传上去等于白传）：%s", modeText)
	}
	readable, _ := s.exec("[ -r "+shellQuote(remoteFile)+" ] && echo yes || echo no", execOpts{QuietMs: 3000})
	if !strings.Contains(readable.Output, "yes") {
		t.Fatalf("⚠️ 远端文件当前账号读不了：%q", readable.Output)
	}
	// ② 内容：用远端自己的 md5sum 比（不依赖传输工具的判词）
	localHash := localMD5(src)
	remoteHash, _ := s.exec("h=$(md5sum "+shellQuote(remoteFile)+"); echo ${h%% *}", execOpts{QuietMs: 3000})
	if got := strings.TrimSpace(remoteHash.Output); !strings.Contains(got, localHash) {
		t.Fatalf("远端内容与本机不一致：本机 %s，远端 %q", localHash, got)
	}

	// ③ 结果文案要如实反映权限（读不了时必须显眼地报出来）
	if !strings.Contains(out.Message, "逐字节一致") {
		t.Fatalf("应该带回读证据：%s", out.Message)
	}
	if strings.Contains(out.Message, "读不了") {
		t.Fatalf("权限正常时不该报读不了：%s", out.Message)
	}
}

// TestE2EZmodemSzPull sz 下载：把刚传上去的文件拉回来，内容必须一致
func TestE2EZmodemSzPull(t *testing.T) {
	e2eZmodem(t)
	h, s := e2eHost(t)
	dir := e2eRemoteTmp(t, s)
	localSrc := e2eLocalTmp(t)
	localDst := e2eLocalTmp(t)

	content := strings.Repeat("下载内容 line\n", 400)
	src := filepath.Join(localSrc, "roundtrip.txt")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := PushPath(h, src, dir, ""); !out.OK {
		t.Fatalf("先上传失败：%s", out.Message)
	}

	out := PullPath(h, dir+"/roundtrip.txt", localDst, "")
	if !out.OK {
		t.Fatalf("sz 下载应该成功：%s", out.Message)
	}
	if out.Method != "sz" {
		t.Fatalf("有 sz 就该走 sz，得到 %q", out.Method)
	}
	got, err := os.ReadFile(out.LocalPath)
	if err != nil {
		t.Fatalf("读落盘文件失败：%v", err)
	}
	if string(got) != content {
		t.Fatalf("拉回来的内容不一致（本机 %d 字节，远端 %d 字节）", len(content), len(got))
	}
	t.Logf("落盘路径：%s", out.LocalPath)

	// 同一个文件再拉一次：本地 rz 带 `-E`（同名改名），第二次可能落到 name.0 ——
	// 不能因为"原名没变"就报失败（pickDownloaded 的注释里写了原因）
	out2 := PullPath(h, dir+"/roundtrip.txt", localDst, "")
	if !out2.OK {
		t.Fatalf("第二次下载应该也成功：%s", out2.Message)
	}
	got2, err := os.ReadFile(out2.LocalPath)
	if err != nil || string(got2) != content {
		t.Fatalf("第二次拉回来的内容不一致：%v", err)
	}
	t.Logf("第二次落盘路径：%s", out2.LocalPath)
}

// TestE2EZmodemDirViaTarRz 目录：本地 tar 打包 → rz → 远端解开
func TestE2EZmodemDirViaTarRz(t *testing.T) {
	e2eZmodem(t)
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
	if out.Method != "tar+rz" {
		t.Fatalf("有 rz 的目录应该走 tar+rz，得到 %q", out.Method)
	}
	list, _ := s.exec("find "+shellQuote(remoteDir)+" -type f | sort", execOpts{QuietMs: 3000})
	if !strings.Contains(list.Output, "conf/a.txt") || !strings.Contains(list.Output, "conf/sub/b.txt") {
		t.Fatalf("解开后的结构不对：%q", list.Output)
	}
	if strings.Contains(list.Output, ".tar.gz") {
		t.Fatalf("远端留下了临时包：%q", list.Output)
	}
	// 解开的文件也要能读（tar 解包保留打包时的权限，这里顺手一并验掉）
	readable, _ := s.exec("[ -r "+shellQuote(remoteDir+"/conf/a.txt")+" ] && echo yes || echo no", execOpts{QuietMs: 3000})
	if !strings.Contains(readable.Output, "yes") {
		t.Fatalf("解开的文件读不了：%q", readable.Output)
	}
}

// TestE2EZmodemOverwriteAndSkip rz 的覆盖/跳过在真机上的行为
func TestE2EZmodemOverwriteAndSkip(t *testing.T) {
	e2eZmodem(t)
	h, s := e2eHost(t)
	dir := e2eRemoteTmp(t, s)
	local := e2eLocalTmp(t)
	src := filepath.Join(local, "swap.txt")
	if err := os.WriteFile(src, []byte("第一版\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "overwrite")
	if out := PushPath(h, src, dir, ""); !out.OK {
		t.Fatalf("第一次上传失败：%s", out.Message)
	}
	if err := os.WriteFile(src, []byte("第二版\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := PushPath(h, src, dir, "")
	if !out.OK {
		t.Fatalf("覆盖模式应该成功：%s", out.Message)
	}
	got, _ := s.exec("cat "+shellQuote(dir+"/swap.txt"), execOpts{QuietMs: 3000})
	if !strings.Contains(got.Output, "第二版") {
		t.Fatalf("覆盖没有生效：%q", got.Output)
	}

	// 换成 skip：又不该覆盖了
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "skip")
	if err := os.WriteFile(src, []byte("第三版\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skip := PushPath(h, src, dir, "")
	if skip.OK || !skip.Skipped {
		t.Fatalf("skip 模式应该跳过：%+v", skip)
	}
	got, _ = s.exec("cat "+shellQuote(dir+"/swap.txt"), execOpts{QuietMs: 3000})
	if !strings.Contains(got.Output, "第二版") {
		t.Fatalf("skip 模式不该改远端内容：%q", got.Output)
	}
}

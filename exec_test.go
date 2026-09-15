package main

import (
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ───────────────────────── exec 引擎的单元测试 ─────────────────────────
//
// 这些测试**不需要 SSH**：用一个假会话（stdin 是被测代码写入的假管道，
// 远端行为由回调模拟）把整个 exec 循环跑起来 —— 哨兵命中、退出码、静止兜底、
// 「完全没回显」兜底、菜单拦截，都能在这里确定性地验。

var reMarker = regexp.MustCompile(`__BASTION_DONE_[0-9a-z]+_[0-9a-z]+__`)

type fakeStdin struct {
	mu  sync.Mutex
	fn  func([]byte)
	got []string
}

func (f *fakeStdin) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.got = append(f.got, string(p))
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		fn(p)
	}
	return len(p), nil
}

func (f *fakeStdin) Close() error { return nil }

func (f *fakeStdin) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// newTestSession 造一个不接 SSH 的会话：写进去的命令由 respond 决定"远端"回什么。
// respond 在写入之后**异步**触发（真实远端也是异步的）。
func newTestSession(respond func(cmd string) string) *bastionSession {
	s := &bastionSession{
		id:      "test-1",
		user:    "deploy",
		host:    "10.0.0.10",
		tail:    newTailBuf(64 * 1024),
		created: time.Now(),
	}
	s.stdin = &fakeStdin{fn: func(b []byte) {
		if respond == nil {
			return
		}
		out := respond(strings.TrimRight(string(b), "\r"))
		if out == "" {
			return
		}
		go func() {
			time.Sleep(20 * time.Millisecond)
			s.tail.write([]byte(out))
		}()
	}}
	return s
}

// newTestSessionLate 造一个"先回显、过一会儿再出结果"的会话 ——
// 用来模拟中间静默的命令（sleep 之类）。
func newTestSessionLate(echo func(cmd string) string, delay time.Duration, late func(cmd string) string) *bastionSession {
	s := &bastionSession{
		id:      "test-late",
		user:    "deploy",
		host:    "10.0.0.10",
		tail:    newTailBuf(64 * 1024),
		created: time.Now(),
	}
	s.stdin = &fakeStdin{fn: func(b []byte) {
		cmd := strings.TrimRight(string(b), "\r")
		if out := echo(cmd); out != "" {
			go func() {
				time.Sleep(20 * time.Millisecond)
				s.tail.write([]byte(out))
			}()
		}
		go func() {
			time.Sleep(delay)
			s.tail.write([]byte(late(cmd)))
		}()
	}}
	return s
}

func markerOf(cmd string) string { return reMarker.FindString(cmd) }

func TestIsMarkerSafe(t *testing.T) {
	cases := []struct {
		cmd            string
		allowPlainSudo bool
		want           bool
	}{
		{"ls -l", false, true},
		{"systemctl restart nginx", false, true},
		{"", false, false},
		{"sudo -i", true, false},
		{"su -", true, false},
		{"sudo -s", true, false},
		{"sudo --login", true, false},
		{"sudo ls", false, false},             // 没确认免密时不敢加哨兵（弹密码会卡住）
		{"sudo ls", true, true},               // 习惯确认免密 → 可以加
		{"sudo grep -i foo bar", true, false}, // 保守：出现 -i 就当交互式
		{"su root", true, false},              // su 一律不加
		{"sleep 100 &", false, false},         // 后台任务
		{"exec bash", false, false},           // 换 shell
		{"ls && exec true", false, false},
	}
	for _, c := range cases {
		if got := isMarkerSafe(c.cmd, c.allowPlainSudo); got != c.want {
			t.Errorf("isMarkerSafe(%q, %v) = %v，想要 %v", c.cmd, c.allowPlainSudo, got, c.want)
		}
	}
}

func TestReadMarkerLineIgnoresCommandEcho(t *testing.T) {
	marker := "__BASTION_DONE_abc_xyz__"
	// 命令回显里带着哨兵的 token，但**不是单独一行** → 不能算完成
	echo := "ls; ec=$?; echo " + marker + ":$ec\r\n"
	if seen, _ := readMarkerLine(echo, marker); seen {
		t.Fatal("命令回显里的哨兵不该被当成完成标记")
	}
	// 真正执行完的那一行
	real := echo + "file1  file2\r\n" + marker + ":0\r\n"
	seen, rc := readMarkerLine(real, marker)
	if !seen || rc == nil || *rc != 0 {
		t.Fatalf("应该识别出哨兵行并读到退出码 0，得到 seen=%v rc=%v", seen, rc)
	}
	// 非 0 退出码
	_, rc = readMarkerLine(echo+"out\r\n"+marker+":137\r\n", marker)
	if rc == nil || *rc != 137 {
		t.Fatalf("应该读到退出码 137，得到 %v", rc)
	}
	// pty 会在哨兵行前面贴 \x1b[?2004l —— 这一条是桌面版踩过的坑，必须仍然认出来
	noisy := "\x1b[?2004l" + marker + ":0\r\n"
	if seen, _ := readMarkerLine(noisy, marker); !seen {
		t.Fatal("哨兵行前面带 pty 噪声时应该仍然认出来")
	}
}

func TestStripMarkerRemovesEchoAndMarkerLines(t *testing.T) {
	marker := "__BASTION_DONE_abc_xyz__"
	in := "echo hi; ec=$?; echo " + marker + ":$ec\nhi\n" + marker + ":0"
	got := stripMarker(in, marker)
	if strings.Contains(got, marker) {
		t.Fatalf("哨兵没剔干净：%q", got)
	}
	if strings.TrimSpace(got) != "hi" {
		t.Fatalf("剔除后的输出应该是纯结果，得到 %q", got)
	}
}

// 【真机回归】pty 会在哨兵中间插一个裸 \r（终端宽度一到就回行），
// 于是命令回显里的哨兵被切成两半，`\r` 折叠之后剩下 `a__:$ec` 这种残片。
// 下面这串是靶机上抓到的**原始字节**（见 debug_echo_test.go 的 dump），一字未改。
func TestStripMarkerRemovesCarriageReturnSplitEcho(t *testing.T) {
	marker := "__BASTION_DONE_mu14eqjb_1dkseaxb97ppa__"
	raw := "\x1b[?2004h" +
		"\x1b[01;32mbastiontest@DESKTOP-TESTBOX\x1b[00m:\x1b[01;34m~\x1b[00m$ " +
		"echo sdk-interop-ok; ec=$?; echo __BASTION_DONE_mu14eqjb_1dkseaxb97ppa\ra__:$ec\r\n" +
		"\x1b[?2004l\rsdk-interop-ok\r\n" +
		marker + ":0\r\n" +
		"\x1b[?2004h\x1b[01;32mbastiontest@DESKTOP-TESTBOX\x1b[00m:\x1b[01;34m~\x1b[00m$ \x1b[K"

	// 先截到哨兵行为止：否则远端重画的提示符会被当成命令输出的一部分
	cut := cutAtMarker(raw, marker)
	if !strings.Contains(cut, "sdk-interop-ok\r\n"+marker+":0\r\n") {
		t.Fatalf("截断位置不对：%q", cut)
	}
	if strings.Contains(cut, "\x1b[K") {
		t.Fatalf("哨兵之后的提示符不该留在捕获窗口里：%q", cut)
	}

	text := stripMarker(tailText(cut, 200000), marker)
	if strings.Contains(text, "$ec") {
		t.Fatalf("折行切碎的哨兵残片漏给了模型：%q", text)
	}
	if strings.Contains(text, marker) {
		t.Fatalf("哨兵没剔干净：%q", text)
	}
	if !strings.Contains(text, "sdk-interop-ok") {
		t.Fatalf("真正的输出被误删了：%q", text)
	}
	if strings.Contains(text, "DESKTOP-TESTBOX") {
		t.Fatalf("命令回显漏给了模型：%q", text)
	}
}

// 【真机回归】两行式命令：含哨兵的那一行必须短到不会触到列宽
func TestExecWritesMarkerOnItsOwnLine(t *testing.T) {
	var sent string
	s := newTestSession(func(cmd string) string {
		sent = cmd
		m := markerOf(cmd)
		return "out\r\n" + m + ":0\r\n"
	})
	if _, err := s.exec("echo hi", execOpts{QuietMs: 300}); err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if strings.Contains(sent, "; echo __BASTION_DONE_") {
		t.Fatalf("哨兵不该和命令挤在同一行（会被 pty 折行切开）：%q", sent)
	}
	if !strings.Contains(sent, "echo hi; ec=$?") {
		t.Fatalf("命令后面要接上状态捕获：%q", sent)
	}
	if !strings.Contains(sent, "\necho "+markerOf(sent)+":$ec") {
		t.Fatalf("哨兵应该单独一行、并以 $ec 收尾：%q", sent)
	}
	// 两行式下含哨兵那行的长度必须远小于常见终端宽度（80），否则又会折行
	lastLine := sent[strings.LastIndex(sent, "\n")+1:]
	if len(lastLine) > 60 {
		t.Fatalf("含哨兵的那一行太长（%d 字符），有折行风险：%q", len(lastLine), lastLine)
	}
}

func TestExecReadsOutputAndExitCode(t *testing.T) {
	s := newTestSession(func(cmd string) string {
		m := markerOf(cmd)
		return "echo hi; ec=$?; echo " + m + ":$ec\r\nhi\r\n" + m + ":0\r\n"
	})
	res, err := s.exec("echo hi", execOpts{QuietMs: 300})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if !res.MarkerSafe || !res.MarkerSeen {
		t.Fatalf("应该走哨兵判定：%+v", res)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("退出码应该是 0，得到 %v", res.ExitCode)
	}
	if strings.TrimSpace(res.Output) != "hi" {
		t.Fatalf("输出应该是 hi，得到 %q", res.Output)
	}
	if !strings.Contains(exitCodeNote(res), "退出码 0") {
		t.Fatalf("备注应该说明命令成功：%q", exitCodeNote(res))
	}
	// 命令本身必须被写进会话（用户要能在终端里看见）
	if w := s.stdin.(*fakeStdin).written(); len(w) != 1 || !strings.Contains(w[0], "echo hi") {
		t.Fatalf("命令没写进会话：%q", w)
	}
}

func TestExecReportsNonZeroExit(t *testing.T) {
	s := newTestSession(func(cmd string) string {
		m := markerOf(cmd)
		return "false\r\n" + m + ":1\r\n"
	})
	res, _ := s.exec("false", execOpts{QuietMs: 300})
	if res.ExitCode == nil || *res.ExitCode != 1 {
		t.Fatalf("退出码应该是 1，得到 %v", res.ExitCode)
	}
	if !strings.Contains(exitCodeNote(res), "非 0") {
		t.Fatalf("备注应该提示失败：%q", exitCodeNote(res))
	}
}

func TestExecFallsBackToQuietWhenNoMarker(t *testing.T) {
	// 远端没打哨兵（比如命令是交互式的，或者 shell 被别的程序占着）
	s := newTestSession(func(cmd string) string {
		return "一些输出，但永远不会有哨兵\r\n"
	})
	start := time.Now()
	res, err := s.exec("some-interactive-thing", execOpts{QuietMs: 200})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if res.MarkerSeen {
		t.Fatal("没等到哨兵时 MarkerSeen 必须是 false —— 否则输出会被当成完整结果")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("静止兜底没生效，等了 %v", elapsed)
	}
	if !strings.Contains(exitCodeNote(res), "没有真正执行完") {
		t.Fatalf("备注必须警告输出不完整：%q", exitCodeNote(res))
	}
}

func TestExecGivesUpWhenNothingComesBack(t *testing.T) {
	// 完全没有回显（远端卡住 / 会话假死）：不能挂满 30 分钟
	s := newTestSession(nil)
	start := time.Now()
	res, err := s.exec("echo hi", execOpts{QuietMs: 5000, NoDataMs: 150})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if res.MarkerSeen {
		t.Fatal("没有回显时不该声称命令跑完了")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("「没有回显」兜底没生效，等了 %v", elapsed)
	}
}

func TestExecMarkerCompletesWithoutWaitingForQuiet(t *testing.T) {
	// 哨兵一到就立刻收工 —— 不该傻等 quietMs 那个窗口（默认 3 秒，
	// 每条命令都白等 3 秒的话，AI 一次排查十几条命令就是一分钟浪费）
	s := newTestSession(func(cmd string) string {
		m := markerOf(cmd)
		return "echo hi; ec=$?; echo " + m + ":$ec\r\nhi\r\n" + m + ":0\r\n"
	})
	start := time.Now()
	res, err := s.exec("echo hi", execOpts{QuietMs: 5000})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if !res.MarkerSeen || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("应该靠哨兵拿到结果，得到 %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("哨兵命中后应当立刻返回，实际等了 %v", elapsed)
	}
}

func TestExecToleratesSilentGapShorterThanQuiet(t *testing.T) {
	// 命令中间静默（sleep）但没超过静止窗口：应该等到哨兵，拿到完整结果
	s := newTestSessionLate(
		func(cmd string) string {
			m := markerOf(cmd)
			return "sleep 1 && echo late; ec=$?; echo " + m + ":$ec\r\n"
		},
		600*time.Millisecond,
		func(cmd string) string { return "late\r\n" + markerOf(cmd) + ":0\r\n" },
	)
	res, err := s.exec("sleep 1 && echo late", execOpts{QuietMs: 3000})
	if err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	if !res.MarkerSeen || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("应该靠哨兵拿到结果，得到 %+v", res)
	}
	if !strings.Contains(res.Output, "late") {
		t.Fatalf("输出应该包含 late，得到 %q", res.Output)
	}
}

func TestExecUnsafeCommandHasNoMarker(t *testing.T) {
	s := newTestSession(func(cmd string) string {
		if strings.Contains(cmd, "__BASTION_DONE_") {
			t.Errorf("不适合加哨兵的命令里不该出现哨兵：%q", cmd)
		}
		return "Password: \r\n"
	})
	res, _ := s.exec("sudo -i", execOpts{QuietMs: 200})
	if res.MarkerSafe {
		t.Fatal("sudo -i 不该被判为适合加哨兵")
	}
	if !res.MarkerSeen {
		t.Fatal("没加哨兵时不该报「没等到标记」")
	}
	if res.ExitCode != nil {
		t.Fatalf("没加哨兵时退出码必须是未知，得到 %v", *res.ExitCode)
	}
	if !strings.Contains(exitCodeNote(res), "退出码未知") {
		t.Fatalf("备注应说明退出码未知：%q", exitCodeNote(res))
	}
}

func TestMenuGuardBlocksExecOnMenuScreen(t *testing.T) {
	s := newTestSession(func(cmd string) string { return "" })
	s.tail.write([]byte(realHostMenu))
	if s.screenState() != stateMenu {
		t.Fatalf("真实主菜单屏幕应该判成 menu，得到 %q", s.screenState())
	}
	guard := menuGuard(s)
	if guard == "" {
		t.Fatal("停在菜单上时必须拦下来 —— 否则命令会被菜单当成自己的输入吃掉")
	}
	if !strings.Contains(guard, "没有发出去") {
		t.Fatalf("拦截文案没说清命令没发出去：%q", guard)
	}
}

func TestMenuGuardAllowsShell(t *testing.T) {
	s := newTestSession(func(cmd string) string { return "" })
	s.tail.write([]byte(realShell))
	if guard := menuGuard(s); guard != "" {
		t.Fatalf("已经落到 shell 的会话不该被拦，得到 %q", guard)
	}
}

func TestLooksLikePasswordPrompt(t *testing.T) {
	if !looksLikePasswordPrompt("sudo ls /root", "[sudo] password for deploy: ") {
		t.Fatal("sudo 密码提示应该被识别")
	}
	if !looksLikePasswordPrompt("su -", "密码：") {
		t.Fatal("中文密码提示应该被识别")
	}
	if looksLikePasswordPrompt("ls -l", "total 0") {
		t.Fatal("普通命令不该被判成密码交互")
	}
	if looksLikePasswordPrompt("sudo ls", "") {
		t.Fatal("没有输出时不该判成密码交互")
	}
}

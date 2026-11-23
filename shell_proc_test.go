package main

// 「两个子进程句柄」的回归测试。
//
// 2026-11-23 真机踩到的坑：Go 的 startUiBackend（核心界面后端）和 startCliShell（⚡ 终端）
// 共用了同一个 theCliShell 句柄，而 startCliShell 换档案前会 stopCliShellLocked() ——
// 于是用户一点 ⚡，给界面当后端的 core 就被杀了，界面立刻 ECONNRESET。
//
// 这一组盯的就是那条界线：停 ⚡ **不能**碰 core；停 core **不能**碰 ⚡。

import (
	"os"
	"os/exec"
	"testing"
)

// fakeProc 起一个很快就会自己退出的进程，只是为了拿到非 nil 的 *exec.Cmd。
// 用测试二进制本身（`-test.run=^$` 什么都不跑）而不是 cmd/sleep：Windows 与 WSL 都能用。
func fakeProc(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("起假进程失败：%v", err)
	}
	go func() { _ = cmd.Wait() }() // 收尸，别留僵尸
	return cmd
}

func TestStopCliShellKeepsCoreAlive(t *testing.T) {
	core := fakeProc(t)
	coreShell.mu.Lock()
	coreShell.proc = core
	coreShell.mu.Unlock()
	theCliShell.mu.Lock()
	theCliShell.proc = fakeProc(t)
	theCliShell.endpoint = &cliEndpoint{URL: "http://127.0.0.1:1/mcp", TermURL: "http://127.0.0.1:1/term"}
	theCliShell.url = "http://127.0.0.1:1/term"
	theCliShell.prof = "xzt"
	theCliShell.mu.Unlock()

	stopCliShell() // = 用户点 ⏹ / 换一组档案

	if coreShellPid() != core.Process.Pid {
		t.Fatalf("停 ⚡ 把核心界面后端也停了（core pid=%d，期望 %d）", coreShellPid(), core.Process.Pid)
	}
	theCliShell.mu.Lock()
	running := theCliShell.endpoint != nil
	theCliShell.mu.Unlock()
	if running {
		t.Fatal("停 ⚡ 之后 endpoint 还在 → /api/shell/endpoint 会继续报 running:true（前端按钮状态骗人）")
	}

	stopCoreShell()
	if coreShellPid() != 0 {
		t.Fatalf("stopCoreShell 没清掉句柄：pid=%d", coreShellPid())
	}
}

func TestStopCoreShellKeepsCliShell(t *testing.T) {
	serve := fakeProc(t)
	theCliShell.mu.Lock()
	theCliShell.proc = serve
	theCliShell.mu.Unlock()
	coreShell.mu.Lock()
	coreShell.proc = fakeProc(t)
	coreShell.mu.Unlock()

	stopCoreShell() // = 应用退出时先收 core

	theCliShell.mu.Lock()
	got := theCliShell.proc
	theCliShell.mu.Unlock()
	if got != serve {
		t.Fatal("停 core 把 ⚡ 终端也停了（两个句柄又混在一起了）")
	}
	stopCliShell()
}

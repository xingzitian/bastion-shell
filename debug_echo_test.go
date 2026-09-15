package main

import (
	"testing"
)

// 诊断用（不是断言测试）：把真实回显的**原始字节**打出来。
// 只在设了 BASTION_E2E_SSH 时跑，用来确认「哨兵回显被换行切开」到底长什么样 ——
// 猜形状写修正是不可靠的，先看清字节再改。
func TestDebugDumpRawEcho(t *testing.T) {
	s, _ := e2eSession(t)
	mark := s.tail.mark()
	if _, err := s.exec("echo sdk-interop-ok", execOpts{QuietMs: 3000}); err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	t.Logf("会话名长度=%d，终端列数=100", len(s.name()))
	t.Logf("原始窗口：\n%q", s.tail.sinceRaw(mark))
	t.Logf("整理后的输出：\n%q", s.screenTail(40))
}

// 用「很长的提示符」逼出换行：远端 PS1 长的时候，回显会被 pty 按列宽折行，
// 哨兵就会被切成两半 —— 这正是真机上看到 `__:$ec` 残渣的原因。
func TestDebugDumpRawEchoWithLongPrompt(t *testing.T) {
	s, _ := e2eSession(t)
	if _, err := s.exec(`PS1='bastiontest@example-production-bastion-gateway-01:/opt/very/long/path$ '`, execOpts{QuietMs: 3000}); err != nil {
		t.Fatalf("设 PS1 失败：%v", err)
	}
	mark := s.tail.mark()
	if _, err := s.exec("echo marker-noise-check", execOpts{QuietMs: 3000}); err != nil {
		t.Fatalf("exec 出错：%v", err)
	}
	t.Logf("长提示符下的原始窗口：\n%q", s.tail.sinceRaw(mark))
}

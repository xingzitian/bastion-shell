package main

import (
	"strings"
	"testing"
)

// 这一层的测试用例**照搬 VS Code 扩展的 src/test/terminal.test.ts 与 menu.test.ts**，
// 包括用户真机上贴回来的屏幕原文（已脱敏）。理由：这些用例里的每一条都对应一次
// 真机踩坑，重写一份"看着差不多"的用例等于把那些坑重新踩一遍。

func TestStripAnsiRemovesColorsAndCsi(t *testing.T) {
	in := "\x1b[32mgreen\x1b[0m and \x1b[1;33mbold\x1b[0m"
	if got := stripAnsi(in); got != "green and bold" {
		t.Fatalf("stripAnsi = %q", got)
	}
	// OSC（终端标题 / OSC52 剪贴板）
	if got := stripAnsi("\x1b]0;title\x07after"); got != "after" {
		t.Fatalf("OSC 没剥干净: %q", got)
	}
	// 光标移动 / 清屏
	if got := stripAnsi("a\x1b[2K\x1b[1Ab"); got != "ab" {
		t.Fatalf("CSI 没剥干净: %q", got)
	}
	if got := stripAnsi(""); got != "" {
		t.Fatalf("空串应该原样返回，得到 %q", got)
	}
}

func TestTailTextDropsCarriageReturnOverwrites(t *testing.T) {
	// 进度条：同一行被反复重写，只应保留最后一段
	in := "下载中 10%\r下载中 50%\r下载中 100%\n完成\n"
	got := tailText(in, 80)
	want := "下载中 100%\n完成"
	if got != want {
		t.Fatalf("tailText = %q，想要 %q", got, want)
	}
}

func TestTailTextTrimsTrailingBlankLinesAndCaps(t *testing.T) {
	in := "a\nb\nc\n\n\n"
	if got := tailText(in, 80); got != "a\nb\nc" {
		t.Fatalf("尾部空行没去掉: %q", got)
	}
	if got := tailText("a\nb\nc", 2); got != "b\nc" {
		t.Fatalf("只该保留末 2 行: %q", got)
	}
	if got := tailText("a\nb", 0); got != "b" {
		t.Fatalf("maxLines<=0 应至少给 1 行: %q", got)
	}
	if got := tailText("", 10); got != "" {
		t.Fatalf("空输入应返回空: %q", got)
	}
}

func TestSafeReadStartBacksOffSplitEscape(t *testing.T) {
	buf := []byte("abc\x1b[32mdef")
	// 标记正好落在 \x1b[3 后面：必须退回 \x1b 处，否则读出来的开头是 "2mdef"
	if got := safeReadStart(buf, 6); got != 3 {
		t.Fatalf("切在转义序列中间应退回 3，得到 %d", got)
	}
	// 标记在转义序列之后：不用退
	if got := safeReadStart(buf, 9); got != 9 {
		t.Fatalf("转义序列已结束不该退，得到 %d", got)
	}
	if got := safeReadStart(buf, 0); got != 0 {
		t.Fatalf("0 应返回 0，得到 %d", got)
	}
	if got := safeReadStart(buf, 999); got != 0 {
		t.Fatalf("越界应返回 0，得到 %d", got)
	}
}

func TestLastN(t *testing.T) {
	lines := []string{"a", "b", "c"}
	if got := strings.Join(lastN(lines, 2), ""); got != "bc" {
		t.Fatalf("lastN(2) = %q", got)
	}
	if got := strings.Join(lastN(lines, 9), ""); got != "abc" {
		t.Fatalf("n 大于长度应全取，得到 %q", got)
	}
}

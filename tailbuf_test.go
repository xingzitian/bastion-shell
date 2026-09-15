package main

import (
	"testing"
	"time"
)

func TestTailBufMarkOnlySeesNewOutput(t *testing.T) {
	tb := newTailBuf(1024)
	tb.write([]byte("旧菜单：1) 输入 IP 进行搜索登录\n"))
	mark := tb.mark()
	tb.write([]byte("新的一屏\n"))

	got := tb.since(mark, 40)
	if got != "新的一屏" {
		t.Fatalf("since(mark) 应该只看到标记之后的内容，得到 %q", got)
	}
	if all := tb.tail(40); all != "旧菜单：1) 输入 IP 进行搜索登录\n新的一屏" {
		t.Fatalf("tail 应该是整段缓冲，得到 %q", all)
	}
}

func TestTailBufMarkSurvivesTruncation(t *testing.T) {
	tb := newTailBuf(8)
	old := tb.mark()
	tb.write([]byte("0123456789")) // 10 字节，超过 2*max 才裁 → 先不裁
	tb.write([]byte("abcdefghij")) // 20 字节 → 裁到 8，dropped=12

	if got := tb.mark(); got != 20 {
		t.Fatalf("逻辑索引应该一直增长到 20，得到 %d", got)
	}
	// 缓冲被裁之后，老标记指向的内容已经没了：应该退化成「从头开始」，而不是错位读残渣
	if got := tb.since(old, 40); got != "cdefghij" {
		t.Fatalf("裁剪后的老标记应该退化成整段缓冲，得到 %q", got)
	}
	if after := tb.since(20, 40); after != "" {
		t.Fatalf("当前标记之后应该没有内容，得到 %q", after)
	}
}

func TestTailBufSinceBacksOffSplitEscape(t *testing.T) {
	tb := newTailBuf(1024)
	tb.write([]byte("abc\x1b[32m"))
	mark := tb.mark() - 2 // 标记落在 \x1b[3 中间
	tb.write([]byte("def"))

	got := tb.since(mark, 40)
	// 读出来必须是一行干净文本（ANSI 已被 tailText 剥掉），而不是 "2mdef" 这种残渣
	if got != "def" {
		t.Fatalf("切在转义序列中间应该退回到 \\x1b 并剥干净，得到 %q", got)
	}
}

func TestTailBufLastDataAt(t *testing.T) {
	tb := newTailBuf(64)
	if tb.lastDataAt().UnixNano() != 0 {
		t.Fatalf("没写过数据时 lastDataAt 应该是零值")
	}
	before := time.Now()
	tb.write([]byte("x"))
	if tb.lastDataAt().Before(before) {
		t.Fatalf("lastDataAt 应该被刷新")
	}
}

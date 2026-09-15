package main

import (
	"sync"
	"time"
)

// tailMaxBytes 每条会话保留的滚动输出上限。
// 256KB ≈ 几千行 —— 够 AI 看屏幕、够菜单识别回溯，又不会随会话时长无限涨。
const tailMaxBytes = 256 * 1024

// tailBuf 会话输出的滚动缓冲。
//
// 为什么要它：桌面版原来终端输出只是**从管道过一遍就发给前端**，后端一个字节都不留 ——
// 于是「这条会话现在屏幕上是什么」这件事在后端根本无法回答，而 AI 的
// tail / 菜单识别 / 命令完成判定全都要用它。
//
// 索引约定（两套坐标，别混）：
//   - **逻辑索引**：从会话开始算起的字节偏移，只会增长。mark() 返回的就是它。
//   - **物理索引**：data 切片里的下标。头部被裁掉之后，两者差一个 dropped。
//
// 有了 dropped 计数，早先记下的 mark 在缓冲被裁之后依然指向**同一个位置**
// （最早被裁掉的部分退化为 0，也就是「从头开始」），不会读到错位的残渣。
type tailBuf struct {
	mu      sync.Mutex
	data    []byte
	dropped int
	max     int
	lastAt  int64 // 最近一次收到数据的时刻（UnixNano），判定「输出静止」用
}

func newTailBuf(max int) *tailBuf {
	if max <= 0 {
		max = tailMaxBytes
	}
	return &tailBuf{max: max}
}

// write 追加输出，并在超过 2 倍上限时裁到上限。
//
// 为什么是「涨到 2 倍再裁」而不是每次越界就裁：裁剪要整体拷贝，逐块裁就成了
// O(n²)（大文件输出时能把会话拖卡）。攒到 2 倍裁一次，摊下来每字节还是常数。
func (t *tailBuf) write(p []byte) {
	if len(p) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > 2*t.max {
		cut := len(t.data) - t.max
		rest := make([]byte, t.max)
		copy(rest, t.data[cut:])
		t.data = rest
		t.dropped += cut
	}
	t.lastAt = time.Now().UnixNano()
}

// mark 记一个「屏幕标记」：此刻缓冲区的逻辑长度。
//
// 为什么需要它：缓冲里留着上一步的旧菜单。输完 IP 再读 tail，
// 旧主菜单还在里面 —— 于是「又回到输 IP」这种强特征会被旧内容误命中。
// 所以每次操作前先打标记，之后只看标记**之后**画出来的东西。
func (t *tailBuf) mark() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped + len(t.data)
}

// since 取标记之后的新输出（已去 ANSI、`\r` 覆盖、尾部空行）
func (t *tailBuf) since(mark, maxLines int) string {
	t.mu.Lock()
	start := t.physicalLocked(mark)
	raw := string(t.data[start:])
	t.mu.Unlock()
	return tailText(raw, maxLines)
}

// sinceRaw 取标记之后的**原始**输出（不截行、不去 ANSI）。
// exec 引擎用它：命令完成判定必须在完整数据上做，截了行就可能把一个
// 被 \r 覆盖的哨兵行截掉。
func (t *tailBuf) sinceRaw(mark int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	start := t.physicalLocked(mark)
	return string(t.data[start:])
}

// tail 取最近 maxLines 行（整段缓冲，不只是标记之后）
func (t *tailBuf) tail(maxLines int) string {
	t.mu.Lock()
	raw := string(t.data)
	t.mu.Unlock()
	return tailText(raw, maxLines)
}

// lastDataAt 最近一次收到数据的时刻
func (t *tailBuf) lastDataAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Unix(0, t.lastAt)
}

// physicalLocked 逻辑索引 → 物理索引（调用方必须持锁）
func (t *tailBuf) physicalLocked(logical int) int {
	p := logical - t.dropped
	if p < 0 {
		p = 0
	}
	if p > len(t.data) {
		p = len(t.data)
	}
	// 标记可能正好把一个 ANSI 转义序列切成两半 —— 退回到那个 \x1b
	return safeReadStart(t.data, p)
}

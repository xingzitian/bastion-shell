package main

import (
	"regexp"
	"strings"
)

// 终端文本处理的纯函数层。
//
// 这一层是**从 VS Code 扩展（bastion-vscode/src/terminal.ts）一比一搬过来的**，
// 不是重写：`stripAnsi` / `tailText` / `safeReadStart` 每一个都对应着一次真机上的
// 踩坑（见下面各自的注释）。两个实现各写一套的话，菜单识别的行为迟早会分叉 ——
// 那正是 docs/two-flavors.md 里说的「A 版认得出、B 版认不出」。

var (
	// OSC ... BEL / ST（终端标题、OSC52 剪贴板等）
	reOscSeq = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	// CSI：颜色、光标移动、清屏
	reCsiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// 字符集切换
	reCharsetSeq = regexp.MustCompile(`\x1b[()][A-Za-z0-9]`)
	// 其他单字符转义
	reSingleSeq = regexp.MustCompile(`\x1b[=>78]`)
)

// stripAnsi 清掉终端输出里的 ANSI 转义序列（颜色、光标、标题等），只留可见文本。
// ZMODEM 控制序列不在这里处理 —— 那是 trzsz 过滤器的事。
func stripAnsi(s string) string {
	if s == "" {
		return ""
	}
	s = reOscSeq.ReplaceAllString(s, "")
	s = reCsiSeq.ReplaceAllString(s, "")
	s = reCharsetSeq.ReplaceAllString(s, "")
	s = reSingleSeq.ReplaceAllString(s, "")
	return s
}

// tailText 把终端原始输出整理成人类/AI 可读的纯文本：
// 去掉 ANSI 转义序列、按 \r 覆盖只保留最后一段（进度条）、去掉尾部空行，最后取末 maxLines 行。
func tailText(raw string, maxLines int) string {
	if raw == "" {
		return ""
	}
	clean := stripAnsi(raw)
	parts := strings.Split(clean, "\n")
	lines := make([]string, 0, len(parts))
	for _, line := range parts {
		// \r 覆盖：同一行被反复重写的进度条只保留最后一段
		segs := strings.Split(line, "\r")
		kept := ""
		for _, seg := range segs {
			if seg != "" {
				kept = seg
			}
		}
		lines = append(lines, strings.TrimRight(kept, " \t"))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if maxLines < 1 {
		maxLines = 1
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// safeReadStart 读取起点：标记落在缓冲区里的位置。如果它正好把一个 ANSI 转义序列切成两半，
// 就退回到那个 `\x1b` 再开始读。
//
// 为什么非要有这个函数：切出来的后半段开头会粘着 `2m` 这种残渣，
// 而 `^\s*opt>`、`^\s*id>` 这类**行首锚定**的规则就再也匹配不上了 ——
// 这正是之前「菜单明明在屏幕上、日志里也看得见，就是认不出来」的元凶。
func safeReadStart(buf []byte, n int) int {
	if n <= 0 || n > len(buf) {
		return 0
	}
	from := n - 32
	if from < 0 {
		from = 0
	}
	esc := lastIndexByteBefore(buf, 0x1b, n)
	if esc < from {
		return n
	}
	body := buf[esc+1 : n]
	// \x1b 之后若已出现「终止字符」（跳过 [ ] ( ) # % 这些引导符），
	// 说明这条转义序列在标记之前就结束了
	if len(body) > 0 && strings.IndexByte("[]()#%", body[0]) >= 0 {
		body = body[1:]
	}
	for _, b := range body {
		if b >= 0x40 && b <= 0x7e {
			return n
		}
	}
	return esc
}

// lastIndexByteBefore 在 buf[:end] 里从后往前找 b，找不到返回 -1
func lastIndexByteBefore(buf []byte, b byte, end int) int {
	if end > len(buf) {
		end = len(buf)
	}
	for i := end - 1; i >= 0; i-- {
		if buf[i] == b {
			return i
		}
	}
	return -1
}

// nonEmptyLines 去掉空行（菜单识别只看有内容的行）
func nonEmptyLines(s string) []string {
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// lastN 取切片末尾 n 个元素（不足则全取）
func lastN(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

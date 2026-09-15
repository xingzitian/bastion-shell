package main

import (
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 命令执行引擎。
//
// 「跑一条命令，拿回输出和成败」这件事在桌面版原来**完全不存在** ——
// 命令是人在终端里敲的，后端看不到结果。AI 的 exec、部署执行、健康检查
// 全都建在这一层上。
//
// 完成判定和 VS Code 版同一套：**哨兵标记 + 退出码**为主，「输出静止」兜底。
// 只有哨兵判定能扛住「中间长时间静默」的命令（`sleep 10 && echo`），
// 也只有退出码能让 AI 分清「命令跑了但没输出」和「命令失败了」。
//
// 与扩展侧的两处**有意不同**（都写在注释里，别以为是不小心漏了）：
//   1. 标记的存在性判断先做 stripAnsi —— 真机上 pty 会在哨兵行前贴 `\x1b[?2004l`，
//      不做剥离就会漏判；
//   2. 加了一个「完全没有回显」的短兜底（默认 30s）。扩展侧只受 maxMs 约束，
//      但 MCP 是一问一答的 HTTP 调用，让一次工具调用挂 30 分钟没有意义。

var (
	reSudoSu          = regexp.MustCompile(`^(sudo|su)(\s|$)`)
	reSudoPlain       = regexp.MustCompile(`^sudo\s+`)
	reSudoInteractive = regexp.MustCompile(`(^|\s)(-i|-s|--login|--shell)(\s|$)`)
	reTrailingAmp     = regexp.MustCompile(`&\s*$`)
	reExecShell       = regexp.MustCompile(`(^|[;&|]\s*)exec(\s|$)`)
)

// isMarkerSafe 判断命令是否适合追加哨兵标记（标记永远不会打印的命令不能加，否则只能等超时）。
//   - su / sudo -i / sudo -s 会换成交互式 shell → 不适合
//   - 尾部 `&` 后台任务、`exec` 换 shell → 不适合
//   - 普通 `sudo <命令>` 本身是安全的，但只有当习惯确认该账号 sudo 免密
//     （allowPlainSudo）时才启用；否则一旦弹密码提示命令就卡住，只能靠「输出静止」兜底。
func isMarkerSafe(command string, allowPlainSudo bool) bool {
	c := strings.TrimSpace(command)
	if c == "" {
		return false
	}
	if reSudoSu.MatchString(c) {
		if !allowPlainSudo {
			return false
		}
		if !reSudoPlain.MatchString(c) {
			return false // su 一律不加
		}
		// 保守起见：只要出现 -i / -s / --login / --shell 就当交互式 shell 处理
		// （哪怕像 `sudo grep -i` 这样被误判，也只是退回静止判定，不会出错）
		if reSudoInteractive.MatchString(c) {
			return false
		}
	}
	if reTrailingAmp.MatchString(c) {
		return false
	}
	if reExecShell.MatchString(c) {
		return false
	}
	return true
}

// newMarker 造一个哨兵（绝不会和目标机上的既有内容撞）
func newMarker() string {
	return fmt.Sprintf("__BASTION_DONE_%s_%s__",
		strconv.FormatInt(time.Now().UnixNano()/1e6, 36),
		strconv.FormatInt(rand.Int63(), 36))
}

// readMarkerLine 找哨兵行，并把它带的**退出码**读出来。
//
// 行格式：`<marker>:<rc>`（老版本是裸 `<marker>`，这里也兼容）。
// 只有「单独一行」才算命中 —— 命令回显里那串 `; echo __MARKER__:$ec` 不算，
// 否则命令一回显就被当成执行完了。
func readMarkerLine(s, marker string) (bool, *int) {
	if marker == "" {
		return false, nil
	}
	plain := stripAnsi(s)
	for _, line := range strings.Split(plain, "\n") {
		t := strings.TrimSpace(line)
		if t == marker {
			return true, nil
		}
		if strings.HasPrefix(t, marker+":") {
			rc, err := strconv.Atoi(strings.TrimSpace(t[len(marker)+1:]))
			if err != nil {
				return true, nil
			}
			return true, &rc
		}
	}
	return false, nil
}

// stripMarker 从返回给 AI 的输出里剔除哨兵相关行。
//
// 为什么要**按行 + 三条规则**，而不是一句 `line.Contains(marker)`：
// 真机上 pty 会在哨兵中间插一个裸 `\r`（终端宽度一到就回行），
// 于是命令回显里的哨兵是**被切开的**。实测原始字节（见 debug_echo_test.go）：
//
//	$ echo sdk-interop-ok; ec=$?; echo __BASTION_DONE_mu14eqjb_1dkseaxb97ppa\ra__:$ec
//
// 折叠 `\r` 之后剩下 `a__:$ec` —— 里面没有完整哨兵，只认完整哨兵就会把它漏给模型。
// 三条规则分别是：
//  1. 命中完整哨兵的行（命令回显 + 哨兵输出行）；
//  2. 以 `ec=$?` 结尾的行 —— 那是我们追加的状态捕获，只可能出现在回显里；
//  3. `\r` 折行切碎后剩下的残片（形如 `a__:$ec`：以 `$ec` 结尾且带 `__` 或 `:`）。
func stripMarker(s, marker string) string {
	if marker == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		raw := stripAnsi(line)
		trimmed := strings.TrimSpace(raw)
		if strings.Contains(raw, marker) {
			continue
		}
		if strings.HasSuffix(trimmed, "ec=$?") {
			continue
		}
		if strings.HasSuffix(trimmed, "$ec") && (strings.Contains(trimmed, "__") || strings.Contains(trimmed, ":")) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// cutAtMarker 把捕获窗口截断到**哨兵行**为止。
//
// 为什么必须截：哨兵行之后紧跟着远端重新画出来的提示符，而「break 之后再读一次
// 缓冲区」会把这个提示符也读进来 —— 于是每条命令的输出尾部都挂着一行
// `user@host:~$`，看起来像命令输出的一部分。
//
// 用 LastIndex：不折行时哨兵在回显和输出里各出现一次，取最后一个才落在输出行上。
func cutAtMarker(raw, marker string) string {
	if marker == "" {
		return raw
	}
	idx := strings.LastIndex(raw, marker)
	if idx < 0 {
		return raw
	}
	end := idx + len(marker)
	if nl := strings.IndexByte(raw[end:], '\n'); nl >= 0 {
		end += nl + 1
	} else {
		end = len(raw)
	}
	return raw[:end]
}

// execOpts 执行参数（零值即默认）
type execOpts struct {
	// QuietMs 输出静止多久算完成（默认 3000）
	QuietMs int
	// MaxMs 总上限（默认 30 分钟）
	MaxMs int
	// NoDataMs 完全没有回显时的兜底（默认 30 秒）
	NoDataMs int
	// AllowPlainSudo 免密 sudo 时才敢给 `sudo <命令>` 加哨兵
	AllowPlainSudo bool
}

// execResult 一条命令的结果
type execResult struct {
	Output     string
	ExitCode   *int
	MarkerSeen bool
	MarkerSafe bool
}

// exec 在会话上执行一条命令：命令写进终端（人实时看得见回显与输出），
// 同时抓取输出与退出码回传。
func (s *bastionSession) exec(command string, opts execOpts) (execResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return execResult{}, fmt.Errorf("空命令")
	}
	if s.closed.Load() {
		return execResult{}, fmt.Errorf("会话已关闭")
	}

	// 一次只跑一条：两条命令并行注入会互相吃掉对方的输出
	s.execMu.Lock()
	defer s.execMu.Unlock()

	// 等第一屏画完再写：刚 open shell 时远端还在打 MOTD / 堡垒机还在画菜单
	s.waitReady(20 * time.Second)
	if s.closed.Load() {
		return execResult{}, fmt.Errorf("会话已关闭")
	}

	quietMs := opts.QuietMs
	if quietMs <= 0 {
		quietMs = 3000
	}
	maxMs := opts.MaxMs
	if maxMs <= 0 {
		maxMs = 30 * 60 * 1000
	}
	noDataMs := opts.NoDataMs
	if noDataMs <= 0 {
		noDataMs = 30 * 1000
	}

	marker := newMarker()
	safe := isMarkerSafe(command, opts.AllowPlainSudo)
	cmd := strings.TrimRight(strings.ReplaceAll(command, "\r\n", "\n"), "\r\n")
	if safe {
		// 尾巴上带退出码：没有它，AI 只能靠读输出猜命令成没成。
		//
		// ⚠️ 这里**故意把哨兵放到第二行**（不是扩展侧那种 `; ec=$?; echo <marker>:$ec`
		// 一行到底）。原因见 stripMarker 的注释：一行的写法在真机上会被 pty 按列宽折行，
		// 哨兵连同命令回显一起被切开，残片漏进 AI 看到的输出里。
		// 拆成两行之后，**含哨兵的那一行很短**（不会触到列宽），折行只会切到
		// 不含哨兵的第一行 —— 这类残渣在结构上就不可能出现。
		cmd += "; ec=$?\necho " + marker + ":$ec"
	}

	mark := s.tail.mark()
	started := time.Now()
	if err := s.writeCommand(cmd); err != nil {
		return execResult{}, err
	}
	touchSession(s.id)

	deadline := started.Add(time.Duration(maxMs) * time.Millisecond)
	noDataDeadline := started.Add(time.Duration(noDataMs) * time.Millisecond)
	seen := false
	var rc *int

	for {
		raw := s.tail.sinceRaw(mark)
		if safe {
			if ok, code := readMarkerLine(raw, marker); ok {
				seen, rc = true, code
				break
			}
		}
		gotData := s.tail.lastDataAt().After(started)
		if gotData && time.Since(s.tail.lastDataAt()) >= time.Duration(quietMs)*time.Millisecond {
			break
		}
		if !gotData && time.Now().After(noDataDeadline) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		if s.closed.Load() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	raw := s.tail.sinceRaw(mark)
	if safe {
		raw = cutAtMarker(raw, marker)
	}
	text := tailText(raw, 200000)
	if safe {
		text = stripMarker(text, marker)
	}
	return execResult{
		Output:     text,
		ExitCode:   rc,
		MarkerSeen: !safe || seen,
		MarkerSafe: safe,
	}, nil
}

// exitCodeNote 命令的退出码备注 —— **没有它，AI 只能靠读输出猜命令成没成**。
//
// 三种情况分开说，因为这三种的下一步完全不同：
//   - 拿到退出码 → 直接给数字（0 = 成功；非 0 通常表示失败）；
//   - 有哨兵但没退出码 → 极少见（远端残留），说"未知"；
//   - 没等到哨兵 → **命令很可能压根没执行完**（终端被别的程序占着，或命令是交互式的），
//     这时候把输出当正常结果看会得出错误结论。
func exitCodeNote(res execResult) string {
	if !res.MarkerSeen {
		return "\n\n[⚠️ 没等到命令结束标记：这条命令很可能**没有真正执行完**" +
			"（终端被别的程序占着、或命令需要交互式输入）。上面的输出不完整，不要当成正常结果；" +
			"必要时用 bastion_tail 看看屏幕上现在是什么。]"
	}
	if res.ExitCode == nil {
		return "\n\n[退出码未知：这条命令是交互式/需要人工输入的，拿不到可靠的结束标记]"
	}
	rc := *res.ExitCode
	if rc == 0 {
		return "\n\n[退出码 0：命令成功]"
	}
	return fmt.Sprintf("\n\n[退出码 %d：命令以非 0 退出，通常表示失败 —— 请结合输出判断原因]", rc)
}

// menuGuard 会话停在菜单上时的拦截（exec / 后续的 push / pull 共用）。
// 返回空串 = 可以继续；返回内容 = 这就是要回给 AI 的话（命令别发）。
func menuGuard(s *bastionSession) string {
	if s.screenState() != stateMenu {
		return ""
	}
	tail := s.screenTail(screenTailLines)
	return menuStuckMessage(tail)
}

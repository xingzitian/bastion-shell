package main

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trzsz/trzsz-go/trzsz"
	"golang.org/x/crypto/ssh"
)

// ───────────────────────── 会话登记表 ─────────────────────────
//
// 桌面版原来**没有登记表**：`ssh.Session` 活在 handleWS 的栈上，WS 一断就没了，
// 终端输出也只是从管道过一遍就发给前端、后端一个字节不留。
// 结果是「当前打开的会话」在后端根本不存在 —— 任何"从旁边操作一下这条会话"的能力
// （AI 执行命令、屏幕快照、命令完成判定）都无从下手。
//
// 这个文件补的就是这块地基：把每条会话登记下来，并给它一个滚动输出缓冲。
// VS Code 版把同样的东西放在 `state.ts` 的 terminals 表 + BastionTerminal.tailBuf 里。

// connMeta 连接池里一条 SSH 连接的元信息（谁、连的哪）。
// 会话登记表要拿它给会话起名字 —— 会话 WS 的 connect 消息里只有 connId，
// 目标机是用户在终端里自己敲的，后端看不到。
type connMeta struct {
	Host string
	Port int
	User string
}

var connMetaStore = struct {
	sync.Mutex
	m map[string]connMeta
}{m: map[string]connMeta{}}

func rememberConn(connID string, m connMeta) {
	if connID == "" {
		return
	}
	connMetaStore.Lock()
	defer connMetaStore.Unlock()
	connMetaStore.m[connID] = m
}

func connMetaOf(connID string) connMeta {
	connMetaStore.Lock()
	defer connMetaStore.Unlock()
	return connMetaStore.m[connID]
}

func forgetConn(connID string) {
	connMetaStore.Lock()
	defer connMetaStore.Unlock()
	delete(connMetaStore.m, connID)
}

// transferListener 传输状态变化（开始/结束）的监听者
type transferListener func(transferring bool)

// bastionSession 一条已认证的会话（人和 AI 共用同一条）
type bastionSession struct {
	id     string
	connID string
	user   string
	host   string
	port   int

	sess  *ssh.Session
	stdin io.WriteCloser // 写进 trzsz 过滤器 → 远端 pty（用户输入和 exec 注入共用）
	tf    *trzsz.TrzszFilter
	tail  *tailBuf

	// writeMu 串行化写 stdin：WS 输入循环和 exec 引擎是两条独立的写路径
	writeMu sync.Mutex
	// execMu 保证一次只跑一条 exec（两条命令并行注入会互相吃掉对方的输出）
	execMu sync.Mutex

	tlMu       sync.Mutex
	listeners  []transferListener
	transferMu sync.Mutex
	transferOn bool
	transferCh chan struct{}

	closed    atomic.Bool
	closeOnce sync.Once

	// ready 会话「画完第一屏」了就关掉它。
	// 为什么需要：刚 open shell 时远端还在打 MOTD / 堡垒机还在画菜单，
	// 这时候把命令写进去会被当成还没画完那一屏的输入（VS Code 版对应 shellReady）。
	ready     chan struct{}
	readyOnce sync.Once

	created     time.Time
	downloadDir string
}

// name 会话名（形如 用户@主机）。目标机是登录之后才知道的，所以这里给的是
// **SSH 那一端**（堡垒机档案就是堡垒机地址）—— AI 用屏幕上的提示符进一步确认目标机。
func (s *bastionSession) name() string {
	if s.user != "" && s.host != "" {
		return fmt.Sprintf("%s@%s", s.user, s.host)
	}
	if s.host != "" {
		return s.host
	}
	return s.id
}

// label 带序号的名字：同一台堡垒机上开了多条会话时，光看 用户@主机 分不出来
func (s *bastionSession) label() string {
	return s.name()
}

// screenState 这条会话现在能不能直接执行命令
func (s *bastionSession) screenState() screenState {
	if s.closed.Load() {
		return stateUnknown
	}
	return classifySessionScreen(s.tail.tail(screenTailLines))
}

// screenTail 屏幕最后几行（原始 → 已整理成可读文本）
func (s *bastionSession) screenTail(maxLines int) string {
	return s.tail.tail(maxLines)
}

// lastPromptLine 屏幕上最后一行有内容的文字 —— 目标机的提示符就在那儿。
// 用它区分「同一个堡垒机上开的不同目标机」：名字都是 用户@堡垒机，提示符才认得出来。
func (s *bastionSession) lastPromptLine() string {
	lines := nonEmptyLines(s.tail.tail(8))
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if len([]rune(last)) > 80 {
		return ""
	}
	return last
}

// writeRaw 往会话里写原始字节（用户输入 + exec 注入都走这里）
func (s *bastionSession) writeRaw(b []byte) error {
	if s.closed.Load() {
		return fmt.Errorf("会话已关闭")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.stdin.Write(b)
	return err
}

// writeCommand 写一条命令（补回车；把 \r\n 归一成 \n，避免多送一个回车）
func (s *bastionSession) writeCommand(cmd string) error {
	return s.writeRaw([]byte(strings.ReplaceAll(cmd, "\r\n", "\n") + "\r"))
}

// resize 终端尺寸变化（前端窗口/分屏拖动）
func (s *bastionSession) resize(cols, rows int) {
	if s.closed.Load() || cols <= 0 || rows <= 0 {
		return
	}
	_ = s.sess.WindowChange(rows, cols)
	if s.tf != nil {
		s.tf.SetTerminalColumns(int32(cols))
	}
}

// uploadFiles 把本机文件交给 trzsz 过滤器上传（拖拽 / 前端选文件都走这条）
func (s *bastionSession) uploadFiles(paths []string) error {
	if s.tf == nil {
		return fmt.Errorf("会话不支持文件传输")
	}
	return s.tf.UploadFiles(paths)
}

// ───────────────────────── rz / sz（给传输工具用） ─────────────────────────

// 传输等待的兜底上限：大文件慢是正常的，但不能无限等
const (
	transferStartTimeout = 25 * time.Second
	transferDoneTimeout  = 30 * time.Minute
)

// uploadFile 走 rz 上传一个本地文件。
//
// mode 决定用哪个 rz 变体（lrzsz 自己的语义，不自己造）：
//   - overwrite → `rz -y`（覆盖同名文件）
//   - rename    → `rz -E`（同名时自动改名，不覆盖）
//   - skip      → `rz -y`（调用方**在上传前**已经确认过远端没有同名文件；
//     这里仍然用 -y 是为了避开 lrzsz 在"文件已存在"时的交互提问 ——
//     那个提问会卡在等输入上，把整条会话挂住）
//
// ⚠️ 这条路要求**目标机装了 lrzsz**（远端跑 rz）。调用方（transfer.go 的能力探测）
// 会先探一次，没有 rz 就自动走 base64 降级。桌面的 Windows 版用的是 zmodem 模式
// （内嵌 sz.exe 当发送端），所以远端只需要有 rz。
func (s *bastionSession) uploadFile(localPath, mode string) error {
	if s.tf == nil {
		return fmt.Errorf("会话不支持文件传输")
	}
	cmd := "rz -y"
	if mode == "rename" {
		cmd = "rz -E"
	}
	s.tf.SetDragFileUploadCommand(cmd)

	wait := s.transferWaitChan()
	if err := s.tf.UploadFiles([]string{localPath}); err != nil {
		return err
	}
	return s.waitTransfer(wait, "上传")
}

// downloadFile 走 sz 把一个远端文件拉到 localDir，返回落盘路径。
//
// 实现方式就是**替用户敲一行 `sz <路径>`**：过滤器（zmodem 或 trzsz）会认出这条
// 命令并把文件收下来，收到 SetDefaultDownloadPath 指定的目录。
func (s *bastionSession) downloadFile(remotePath, localDir string) (string, error) {
	if s.tf == nil {
		return "", fmt.Errorf("会话不支持文件传输")
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return "", err
	}
	s.tf.SetDefaultDownloadPath(localDir)
	local := filepath.Join(localDir, filepath.Base(remotePath))
	// 传输前后各拍一张目录快照：用差集判断"到底下下来了什么"，
	// **不靠 mtime**（ZMODEM 会保留源文件 mtime，见 chooseDownloaded 的注释）
	beforeNames := dirNameSet(localDir)
	beforeSize, beforeMod := int64(-1), time.Time{}
	if st, err := os.Stat(local); err == nil {
		beforeSize, beforeMod = st.Size(), st.ModTime()
	}

	wait := s.transferWaitChan()
	if err := s.writeCommand("sz " + shellQuote(remotePath)); err != nil {
		return "", err
	}
	if err := s.waitTransfer(wait, "下载"); err != nil {
		return "", err
	}
	afterNames := dirNameSet(localDir)

	if picked := chooseDownloaded(localDir, filepath.Base(remotePath), beforeNames, afterNames, beforeSize, beforeMod); picked != "" {
		return picked, nil
	}
	tail := s.screenTail(6)
	return "", fmt.Errorf("文件没有落盘（期望 %s）—— 目标机可能没装 lrzsz（sz），或者路径不对。\n"+
		"落盘目录里现在有：%s\n屏幕最后几行：\n%s", local, listDirNames(localDir), tail)
}

// listDirNames 列目录里的文件名（排障用：文件到底落到哪儿去了）
func listDirNames(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "（读不到：" + err.Error() + "）"
	}
	if len(entries) == 0 {
		return "（空）"
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ", ")
}

// dirNameSet 目录里的文件名集合（传输前后各拍一张，用差集判断"下来了什么"）
func dirNameSet(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

// chooseDownloaded 传输结束后判断**到底下下来的是哪个文件**（返回空串 = 没下来）。
//
// 为什么要靠「目录快照的前后差集」而不是「mtime 最新的那个」：
// ZMODEM 会**保留源文件的 mtime**（扩展侧为这件事踩过坑），所以刚下回来的
// `name.0` 的 mtime 可能比上一次下的 `name` 还旧 —— 按 mtime 挑会挑错，
// 于是"明明下来了却报失败"。
//
// 之所以不能只盯 `<localDir>/<basename>`：trzsz-go 的 zmodem 下载固定给本地 rz 加 `-E`
// （同名时改名），所以第二次下载会落到 `name.0`、`name.1`
// （见 third_party/trzsz-go/trzsz/zmodem.go）。
func chooseDownloaded(localDir, base string, before, after map[string]bool, beforeSize int64, beforeMod time.Time) string {
	// 1) 新出现的文件里挑（改名后的 name.N 也算，取 N 最大的那个）
	best, bestRank := "", -1
	for name := range after {
		if before[name] {
			continue
		}
		if name != base && !isVersionedName(name, base) {
			continue
		}
		rank := 0
		if name != base {
			var err error
			rank, err = strconv.Atoi(name[len(base)+1:])
			if err != nil {
				continue
			}
			rank++
		}
		if rank > bestRank {
			best, bestRank = name, rank
		}
	}
	if best != "" {
		return filepath.Join(localDir, best)
	}
	// 2) 没有新文件：原来那个被就地换掉了也算成功（这条只作兜底，
	//    因为"传输保留了 mtime"会让它判不出来 —— 但大小变了还是看得出来的）
	full := filepath.Join(localDir, base)
	if after[base] {
		if st, err := os.Stat(full); err == nil {
			if beforeSize != st.Size() || !st.ModTime().Equal(beforeMod) {
				return full
			}
		}
	}
	return ""
}

// isVersionedName `name.3` 这种（本地 rz -E 改名后的样子，不是 `name.txt.bak`）
func isVersionedName(name, base string) bool {
	if !strings.HasPrefix(name, base+".") {
		return false
	}
	_, err := strconv.Atoi(name[len(base)+1:])
	return err == nil
}

// waitTransfer 等一次传输真的开始、并且结束
func (s *bastionSession) waitTransfer(wait chan struct{}, what string) error {
	start := time.Now()
	for !s.isTransferring() {
		// 传输可能已经开始**并且结束了**（小文件几百毫秒就完了），
		// 那就直接算完成 —— 不然会误报"没有开始"
		select {
		case <-wait:
			return nil
		default:
		}
		if time.Since(start) > transferStartTimeout {
			return fmt.Errorf("%s没有开始（目标机可能没装 lrzsz，或者远端拒绝了这条命令）", what)
		}
		if s.closed.Load() {
			return fmt.Errorf("会话已关闭")
		}
		time.Sleep(80 * time.Millisecond)
	}
	select {
	case <-wait:
		return nil
	case <-time.After(transferDoneTimeout):
		return fmt.Errorf("%s超时（超过 %s 还没结束）", what, transferDoneTimeout)
	}
}

// observe 输出落地：写进滚动缓冲（这是「屏幕快照」和命令完成判定的唯一数据源）
func (s *bastionSession) observe(b []byte) {
	s.tail.write(b)
}

// ── 会话就绪（第一屏画完） ──

// waitScreenSettle 等「当前这一屏画完」：先等出现数据（最多 first），再等输出静止 quiet。
//
// 为什么用「等静止 + 读整屏」而不是「在数据流里找提示文本」——
// 堡垒机画菜单是「一屏一屏刷」的，中间夹着 `\r` 回行覆盖和光标移动，
// 数据流里同一行会有好几遍叠在一起，跟屏幕上真正显示的东西不是一回事。
// 等它画完再读屏幕，拿到的才是屏幕上的文字（这是扩展侧踩了两次坑之后的结论）。
func (s *bastionSession) waitScreenSettle(first, quiet, max time.Duration) bool {
	start := time.Now()
	for {
		last := s.tail.lastDataAt()
		hasData := last.UnixNano() != 0
		if hasData && time.Since(last) >= quiet {
			return true
		}
		elapsed := time.Since(start)
		if !hasData && elapsed >= first {
			return false
		}
		if elapsed >= max {
			return hasData
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *bastionSession) markReady() {
	s.readyOnce.Do(func() { close(s.ready) })
}

// waitReady 等会话就绪（最多 max）
func (s *bastionSession) waitReady(max time.Duration) {
	if s.ready == nil {
		return
	}
	select {
	case <-s.ready:
	case <-time.After(max):
	}
}

// ── 传输状态 ──

func (s *bastionSession) noteTransfer(transferring bool) {
	s.transferMu.Lock()
	if transferring {
		// ⚠️ **不要在这里新建通道**（2026-09-15 真机抓到的死等 bug）：
		// 等的人是在「触发传输之前」就把通道拿在手里的，这里一换，
		// 他手里那个就永远不会被关闭 —— 传输明明成功了，却一直等到超时。
		// 通道只在「结束」时关闭并置空，下一次传输自然会拿到新的。
		if s.transferCh == nil {
			s.transferCh = make(chan struct{})
		}
		s.transferOn = true
	} else {
		if s.transferOn && s.transferCh != nil {
			close(s.transferCh)
		}
		s.transferCh = nil
		s.transferOn = false
	}
	s.transferMu.Unlock()

	s.tlMu.Lock()
	ls := append([]transferListener(nil), s.listeners...)
	s.tlMu.Unlock()
	for _, l := range ls {
		l(transferring)
	}
}

// addTransferListener 监听传输开始/结束（前端要它来发 upload:done）
func (s *bastionSession) addTransferListener(l transferListener) {
	if l == nil {
		return
	}
	s.tlMu.Lock()
	defer s.tlMu.Unlock()
	s.listeners = append(s.listeners, l)
}

// waitTransferEnd 等当前这次传输结束；返回 false = 超时。
// 注意：必须在**触发传输之前**拿到通道，所以先调 transferWaitChan()。
func (s *bastionSession) transferWaitChan() chan struct{} {
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	if s.transferCh == nil {
		s.transferCh = make(chan struct{})
	}
	return s.transferCh
}

func (s *bastionSession) isTransferring() bool {
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	return s.transferOn
}

// ── 生命周期 ──

// finish 会话已经没了：从登记表摘掉（不碰底层连接，那条由 close 负责）
func (s *bastionSession) finish() {
	if s.closed.Swap(true) {
		return
	}
	unregisterSession(s.id)
}

func (s *bastionSession) close() {
	s.closeOnce.Do(func() {
		s.finish()
		if s.tf != nil {
			s.tf.Close()
		}
		if s.sess != nil {
			_ = s.sess.Close()
		}
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
	})
}

// ── 登记表 ──

var sessionRegistry = struct {
	sync.Mutex
	m          map[string]*bastionSession
	order      []string
	lastActive string
}{m: map[string]*bastionSession{}}

func registerSession(s *bastionSession) {
	sessionRegistry.Lock()
	defer sessionRegistry.Unlock()
	sessionRegistry.m[s.id] = s
	sessionRegistry.order = append(sessionRegistry.order, s.id)
	sessionRegistry.lastActive = s.id
}

func unregisterSession(id string) {
	sessionRegistry.Lock()
	defer sessionRegistry.Unlock()
	delete(sessionRegistry.m, id)
	out := sessionRegistry.order[:0]
	for _, x := range sessionRegistry.order {
		if x != id {
			out = append(out, x)
		}
	}
	sessionRegistry.order = out
	if sessionRegistry.lastActive == id {
		sessionRegistry.lastActive = ""
		if n := len(sessionRegistry.order); n > 0 {
			sessionRegistry.lastActive = sessionRegistry.order[n-1]
		}
	}
}

// touchSession 记下「最近用的那条」，exec/tail 不指定会话时就落到它身上
func touchSession(id string) {
	sessionRegistry.Lock()
	defer sessionRegistry.Unlock()
	if _, ok := sessionRegistry.m[id]; ok {
		sessionRegistry.lastActive = id
	}
}

// listSessions 按开立顺序返回（最近用的那条另有标记）
func listSessions() []*bastionSession {
	sessionRegistry.Lock()
	defer sessionRegistry.Unlock()
	out := make([]*bastionSession, 0, len(sessionRegistry.order))
	for _, id := range sessionRegistry.order {
		if s, ok := sessionRegistry.m[id]; ok && !s.closed.Load() {
			out = append(out, s)
		}
	}
	return out
}

func lastActiveSession() *bastionSession {
	sessionRegistry.Lock()
	id := sessionRegistry.lastActive
	s := sessionRegistry.m[id]
	sessionRegistry.Unlock()
	if s != nil && !s.closed.Load() {
		return s
	}
	return nil
}

// lookupSession 按终端名/会话 id 找会话；want 为空则用最近活动的那条。
// 返回的字符串非空表示「没找到」，内容就是要回给 AI 的话。
func lookupSession(want string) (*bastionSession, string) {
	sessions := listSessions()
	if len(sessions) == 0 {
		return nil, "没有活动堡垒机会话：请先在 BastionShell 里连一台机器（连接 + 进入 shell），" +
			"之后 AI 就能在**同一条会话**上执行命令，不需要重新认证。"
	}
	w := strings.TrimSpace(want)
	if w == "" {
		if s := lastActiveSession(); s != nil {
			return s, ""
		}
		return sessions[0], ""
	}
	for _, s := range sessions {
		if s.id == w {
			return s, ""
		}
	}
	for _, s := range sessions {
		if s.name() == w {
			return s, ""
		}
	}
	for _, s := range sessions {
		if strings.Contains(s.name(), w) || strings.Contains(s.id, w) {
			return s, ""
		}
	}
	return nil, fmt.Sprintf("找不到会话「%s」。先用 bastion_listSessions 看有哪些会话（名字形如 用户@主机）", w)
}

// sessionLabels 会话 id → 展示名。同一台堡垒机上开了多条时补 #2 #3，
// 否则 AI 看到三行一模一样的 用户@主机 根本没法指定目标。
func sessionLabels() map[string]string {
	sessions := listSessions()
	count := map[string]int{}
	for _, s := range sessions {
		count[s.name()]++
	}
	seen := map[string]int{}
	out := make(map[string]string, len(sessions))
	for _, s := range sessions {
		base := s.name()
		if count[base] > 1 {
			seen[base]++
			out[s.id] = fmt.Sprintf("%s#%d", base, seen[base])
		} else {
			out[s.id] = base
		}
	}
	return out
}

// ───────────────────────── 建会话 ─────────────────────────

type sessionOpts struct {
	ID     string
	ConnID string
	Client *ssh.Client
	Cols   int
	Rows   int
	// Out 终端数据出口（WS 写回前端；测试里换成收集器）
	Out func([]byte)
}

// newBastionSession 建一条会话：pty + shell + trzsz 过滤器 + 输出登记，
// 并把它放进登记表。**这是唯一建会话的入口** —— handleWS 和 Go 测试都走它，
// 免得「测试里跑通的路径」和「真机上跑的路径」其实是两份实现。
func newBastionSession(o sessionOpts) (*bastionSession, error) {
	if o.Client == nil {
		return nil, fmt.Errorf("缺少 SSH 连接")
	}
	cols, rows := o.Cols, o.Rows
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}

	sess, err := o.Client.NewSession()
	if err != nil {
		return nil, err
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}

	// trzsz 过滤器：包在客户端(前端)与服务端(SSH)之间，自动处理 rz/sz 文件传输
	clientIn, stdinPipe := io.Pipe()   // 终端输入：前端 → stdinPipe → clientIn → 过滤器 → stdin
	stdoutPipe, clientOut := io.Pipe() // 终端输出：stdout → 过滤器 → clientOut → stdoutPipe → 前端
	dragCmd := "trz -y"
	if useZmodem {
		dragCmd = "rz -y"
	}
	tf := trzsz.NewTrzszFilter(clientIn, clientOut, stdin, stdout, trzsz.TrzszOptions{
		TerminalColumns: int32(cols),
		EnableZmodem:    useZmodem, // Windows 上用内嵌 lrzsz 走 zmodem（服务器只需 rz/sz）；其它平台走原生 trzsz
	})
	tf.SetDragFileUploadCommand(dragCmd)
	downloadDir := defaultDownloadDir()
	_ = os.MkdirAll(downloadDir, 0o755)
	tf.SetDefaultDownloadPath(downloadDir)

	if err := sess.Shell(); err != nil {
		tf.Close()
		_ = sess.Close()
		return nil, err
	}

	meta := connMetaOf(o.ConnID)
	s := &bastionSession{
		id:          o.ID,
		connID:      o.ConnID,
		user:        meta.User,
		host:        meta.Host,
		port:        meta.Port,
		sess:        sess,
		stdin:       stdinPipe,
		tf:          tf,
		tail:        newTailBuf(tailMaxBytes),
		ready:       make(chan struct{}),
		created:     time.Now(),
		downloadDir: downloadDir,
	}
	if s.id == "" {
		s.id = fmt.Sprintf("s%d%04d", time.Now().UnixNano()/1e6, rand.Intn(10000))
	}
	tf.SetTransferStateCallback(s.noteTransfer)

	// 终端输出：stdoutPipe → 前端 + 滚动缓冲
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				s.observe(chunk)
				if o.Out != nil {
					o.Out(chunk)
				}
			}
			if err != nil {
				s.finish()
				return
			}
		}
	}()

	registerSession(s)
	log.Printf("[会话 %s] 已登记（%s，共 %d 条）", s.id, s.name(), len(listSessions()))

	// 第一屏画完 → 就绪（exec 会等它，免得命令被还没画完的那一屏吃掉）
	go func() {
		s.waitScreenSettle(8*time.Second, 800*time.Millisecond, 20*time.Second)
		s.markReady()
	}()
	return s, nil
}

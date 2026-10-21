package main

// 执行作业（exec job）—— 与扩展侧同一套语义（见扩展的 docs/exec-protocol.md）：
// 状态词对齐 MCP 官方 Tasks 扩展：working / input_required / completed / failed / cancelled。
//
// 桌面版没有"作业日志 tee 到远端"这一步（堡垒机会话里读远端文件需要抢终端），
// 这里用**内存滚动缓冲**：桌面版的 exec 本来就一路收着输出（observe → tailBuf），
// 多存一份按字节偏移切片即可 —— 不依赖远端写文件，也不会把终端抢走。

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	jobBufferMax = 8 * 1024 * 1024
	// 单轮 wait 上限：低于 MCP 客户端的单次调用预算（常见 60s）
	waitBudget = 55 * time.Second
)

type execStatus string

const (
	statusWorking       execStatus = "working"
	statusInputRequired execStatus = "input_required"
	statusCompleted     execStatus = "completed"
	statusFailed        execStatus = "failed"
	statusCancelled     execStatus = "cancelled"
)

type execJob struct {
	id         string
	sessionID  string
	command    string
	startedAt  time.Time
	state      execStatus
	exitCode   int
	hasExit    bool
	buf        bytes.Buffer
	dropped    int64
	lastLine   string
	markerSeen bool
	cmdAtStart int64 // 屏幕游标
	prompt     string
	hint       string
	reason     string
	// marker / quietMs / lastOutputAt：**作业自己**判完成（exec 超时返回后仍然有效）
	marker       string
	quietMs      int64
	lastOutputAt time.Time
	scanTail     string

	mu      sync.Mutex
	waiters []chan struct{}
}

type jobView struct {
	JobID       string     `json:"jobId"`
	Status      execStatus `json:"status"`
	ExitCode    int        `json:"exitCode,omitempty"`
	ElapsedMs   int64      `json:"elapsedMs"`
	Cursor      int64      `json:"cursor"`
	CursorMoved bool       `json:"cursorMoved"`
	LastLine    string     `json:"lastLine"`
	MarkerSeen  bool       `json:"markerSeen"`
	Prompt      string     `json:"prompt,omitempty"`
	Hint        string     `json:"hint,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

var (
	jobsMu   sync.Mutex
	jobs     = map[string]*execJob{}
	jobOrder []string
	jobSeq   int
	// 每条会话当前"正在跑"的作业（同一时刻只允许一个，避免命令插队）
	activeJobBySession = map[string]string{}
)

func newJobID() string {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	jobSeq++
	return fmt.Sprintf("j-%04d", jobSeq)
}

func createJob(sessionID string, cursorAtStart int64, command string) *execJob {
	j := &execJob{
		id:           newJobID(),
		sessionID:    sessionID,
		command:      command,
		startedAt:    time.Now(),
		state:        statusWorking,
		cmdAtStart:   cursorAtStart,
		quietMs:      15000,
		lastOutputAt: time.Now(),
	}
	jobsMu.Lock()
	jobs[j.id] = j
	jobOrder = append(jobOrder, j.id)
	activeJobBySession[sessionID] = j.id
	jobsMu.Unlock()
	return j
}

func getJob(id string) *execJob {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	return jobs[id]
}

func activeJobOf(sessionID string) *execJob {
	jobsMu.Lock()
	id := activeJobBySession[sessionID]
	jobsMu.Unlock()
	if id == "" {
		return nil
	}
	return getJob(id)
}

func (j *execJob) wake() {
	j.mu.Lock()
	ws := j.waiters
	j.waiters = nil
	j.mu.Unlock()
	for _, ch := range ws {
		close(ch)
	}
}

// appendOutput 追加一块输出（会话输出汇每来一块就调它）
func (j *execJob) appendOutput(b []byte) {
	if len(b) == 0 {
		return
	}
	j.mu.Lock()
	j.buf.Write(b)
	if j.buf.Len() > jobBufferMax {
		drop := j.buf.Len() - jobBufferMax
		j.buf.Next(drop)
		j.dropped += int64(drop)
	}
	tail := j.buf.Bytes()
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
	}
	if line := lastNonEmptyLine(string(tail)); line != "" {
		j.lastLine = line
	}
	j.mu.Unlock()
	// ★ 完成判定归作业自己：exec 超时返回后，标记仍然能被这里解析出来
	if rc, ok := j.scanMarker(string(b)); ok {
		j.complete(rc, true, true)
		return
	}
	j.wake()
}

// scanMarker 在"上一块的尾巴 + 这一块"里找哨兵，返回退出码。
// 拼尾巴是因为标记可能被切在两次数据块之间；回显里是字面量 "$ec"，正则要求 :<数字> 才算。
func (j *execJob) scanMarker(chunk string) (int, bool) {
	j.mu.Lock()
	marker := j.marker
	hay := j.scanTail + chunk
	if marker != "" {
		j.scanTail = tailBytes(hay, len(marker)+64)
	}
	j.mu.Unlock()
	if marker == "" {
		return 0, false
	}
	re := regexp.MustCompile(regexp.QuoteMeta(marker) + `:(\d+)`)
	m := re.FindStringSubmatch(hay)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// quietExpired 输出已经静止够久了吗（静默只是"可以兜底"，还要调用方确认提示符已回）
func (j *execJob) quietExpired(now time.Time) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == statusWorking && now.Sub(j.lastOutputAt) >= time.Duration(j.quietMs)*time.Millisecond
}

func lastNonEmptyLine(s string) string {
	lines := bytes.Split([]byte(s), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		t := string(bytes.TrimRight(lines[i], "\r"))
		if len(bytes.TrimSpace([]byte(t))) > 0 {
			if len(t) > 300 {
				t = t[:300]
			}
			return t
		}
	}
	return ""
}

func (j *execJob) endOffset() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.dropped + int64(j.buf.Len())
}

// sliceFrom 按**绝对字节偏移**切片；offset 落在已丢弃区间时就从头给
func (j *execJob) sliceFrom(since int64, maxBytes int) (chunk string, next int64, truncated bool) {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	start := since - j.dropped
	if start < 0 {
		start = 0
	}
	if start >= int64(j.buf.Len()) {
		return "", j.dropped + int64(j.buf.Len()), false
	}
	data := j.buf.Bytes()[start:]
	end := int64(maxBytes)
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return string(data[:end]), j.dropped + start + end, j.dropped+start+end < j.dropped+int64(j.buf.Len())
}

func (j *execJob) complete(exitCode int, hasExit bool, markerSeen bool) {
	j.mu.Lock()
	if j.state != statusWorking && j.state != statusInputRequired {
		j.mu.Unlock()
		return
	}
	j.exitCode = exitCode
	j.hasExit = hasExit
	j.markerSeen = markerSeen
	if hasExit && exitCode != 0 {
		j.state = statusFailed
		j.reason = "exit_nonzero"
	} else {
		j.state = statusCompleted
	}
	sessionID := j.sessionID
	id := j.id
	j.mu.Unlock()
	clearActive(sessionID, id)
	j.wake()
}

func (j *execJob) markInputRequired(prompt, hint string) {
	j.mu.Lock()
	if j.state != statusWorking {
		j.mu.Unlock()
		return
	}
	j.state = statusInputRequired
	j.prompt = prompt
	j.hint = hint
	j.mu.Unlock()
	j.wake()
}

func (j *execJob) resume() {
	j.mu.Lock()
	if j.state == statusInputRequired {
		j.state = statusWorking
		j.prompt, j.hint = "", ""
	}
	j.mu.Unlock()
}

func (j *execJob) cancel() {
	j.mu.Lock()
	if j.state == statusCompleted || j.state == statusFailed || j.state == statusCancelled {
		j.mu.Unlock()
		return
	}
	j.state = statusCancelled
	j.reason = "cancelled"
	sessionID, id := j.sessionID, j.id
	j.mu.Unlock()
	clearActive(sessionID, id)
	j.wake()
}

func (j *execJob) fail(reason string) {
	j.mu.Lock()
	if j.state == statusCompleted || j.state == statusFailed || j.state == statusCancelled {
		j.mu.Unlock()
		return
	}
	j.state = statusFailed
	j.reason = reason
	sessionID, id := j.sessionID, j.id
	j.mu.Unlock()
	clearActive(sessionID, id)
	j.wake()
}

func clearActive(sessionID, id string) {
	jobsMu.Lock()
	if activeJobBySession[sessionID] == id {
		delete(activeJobBySession, sessionID)
	}
	jobsMu.Unlock()
}

func (j *execJob) view(cursorNow int64) jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobView{
		JobID:       j.id,
		Status:      j.state,
		ExitCode:    j.exitCode,
		ElapsedMs:   time.Since(j.startedAt).Milliseconds(),
		Cursor:      cursorNow,
		CursorMoved: cursorNow > j.cmdAtStart,
		LastLine:    j.lastLine,
		MarkerSeen:  j.markerSeen,
		Prompt:      j.prompt,
		Hint:        j.hint,
		Reason:      j.reason,
	}
}

func clampWaitMs(ms int64, def int64) int64 {
	if ms <= 0 {
		if ms == 0 {
			return def
		}
		return def
	}
	if ms > int64(waitBudget/time.Millisecond) {
		return int64(waitBudget / time.Millisecond)
	}
	return ms
}

// waitFor 等作业：终态 / pattern 命中 / 超时，任一条件即返回
func (j *execJob) waitFor(pattern string, waitMs int64, sinceOffset int64, cursorNow func() int64) jobView {
	// 已经满足就别等
	if j.terminalState() || (pattern != "" && j.matchPattern(pattern, sinceOffset)) {
		return j.view(cursorNow())
	}
	ch := make(chan struct{})
	j.mu.Lock()
	j.waiters = append(j.waiters, ch)
	j.mu.Unlock()

	timeout := time.Duration(clampWaitMs(waitMs, 20000)) * time.Millisecond
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	_ = timer

	select {
	case <-ch:
	case <-deadline.C:
	}
	return j.view(cursorNow())
}

func (j *execJob) terminalState() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == statusCompleted || j.state == statusFailed || j.state == statusCancelled
}

func (j *execJob) matchPattern(pattern string, sinceOffset int64) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	chunk, _, _ := j.sliceFrom(sinceOffset, 512*1024)
	return re.MatchString(chunk)
}

// matchedLine 返回命中的那一行（给 AI 看证据）
func (j *execJob) matchedLine(pattern string, sinceOffset int64) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	chunk, _, _ := j.sliceFrom(sinceOffset, 512*1024)
	m := re.FindString(chunk)
	return m
}

// ── 交互提示识别（与扩展侧同一组模式）──

var inputPrompts = []struct {
	re   *regexp.Regexp
	hint string
}{
	{regexp.MustCompile(`(?i)\[sudo\]\s*password\s*for\s+\S+\s*:`), "sudo 在等密码 —— 让用户在终端里输入，或改用免密 sudo"},
	{regexp.MustCompile(`(?im)^\s*(password|passwd|密码)\s*(for\s+\S+)?\s*[:：]\s*$`), "在等密码 —— 让用户在终端里输入（不要把密码交给 AI）"},
	{regexp.MustCompile(`(?i)(verification code|动态码|验证码|otp)\s*[:：]`), "在等动态码 —— 让用户在终端里输入"},
	{regexp.MustCompile(`(?im)(\(yes/no\)|\[y/n\]|\(y/n\))\s*\??\s*$`), "在等确认（y/n）—— 让用户决定"},
	{regexp.MustCompile(`(?i)(enter passphrase|passphrase for key)`), "在等口令（passphrase）—— 让用户输入"},
	{regexp.MustCompile(`(请选择|请选择目标机|请选择账号|输入编号|资产\s*ID|菜单)`), "停在堡垒机菜单/选择项上 —— 让用户在终端里走完这一步"},
	{regexp.MustCompile(`(?i)(press any key|任意键继续)`), "在等按键 —— 让用户在终端里按一下"},
}

func detectInputPrompt(text string) (prompt string, hint string, ok bool) {
	if len(text) > 2000 {
		text = text[len(text)-2000:]
	}
	for _, p := range inputPrompts {
		if m := p.re.FindString(text); m != "" {
			if len(m) > 200 {
				m = m[:200]
			}
			return m, p.hint, true
		}
	}
	return "", "", false
}

// sessionByID 按会话 id 找会话（作业相关工具按 jobId 回溯会话用）
func sessionByID(id string) *bastionSession {
	for _, s := range listSessions() {
		if s.id == id {
			return s
		}
	}
	return nil
}

// ── 最近关闭的会话（带 TTL）──

var closedSIDs = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func rememberClosedSID(sid string) {
	if sid == "" {
		return
	}
	closedSIDs.Lock()
	defer closedSIDs.Unlock()
	closedSIDs.m[sid] = time.Now()
}

func isRecentlyClosedSID(sid string) bool {
	if sid == "" {
		return false
	}
	closedSIDs.Lock()
	defer closedSIDs.Unlock()
	for k, at := range closedSIDs.m {
		if time.Since(at) > 10*time.Minute {
			delete(closedSIDs.m, k)
		}
	}
	_, ok := closedSIDs.m[sid]
	return ok
}

// startJobWatcher 作业看门狗（每秒一次）。
//
// 静默**不能单独**作为完成依据：`sleep 45` 也是静默 —— 必须"静默 + 提示符已经回来了"才算跑完；
// 屏幕回到堡垒机菜单则是 `exit` 之类把 shell 退了，记 session_exited。
func startJobWatcher() {
	watcherOnce.Do(func() {
		go func() {
			for {
				time.Sleep(time.Second)
				for _, j := range snapshotJobs() {
					if j.terminalState() {
						continue
					}
					s := sessionByID(j.sessionID)
					if s == nil {
						j.fail("session_lost")
						continue
					}
					if !j.quietExpired(time.Now()) {
						continue
					}
					switch s.screenState() {
					case stateMenu:
						j.fail("session_exited")
					case stateShell:
						j.completeQuiet()
					}
				}
			}
		}()
	})
}

var watcherOnce sync.Once

// snapshotJobs 当前所有作业（看门狗遍历用；避免长时间持锁）
func snapshotJobs() []*execJob {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	out := make([]*execJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j)
	}
	return out
}

// completeQuiet 静默兜底完成（没看到哨兵，但提示符已回）
func (j *execJob) completeQuiet() {
	j.complete(0, false, false)
}

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// **传输的标准工具**：所有"传文件"的入口最后都走这里（桌面版侧）。
//
// 移植自 VS Code 扩展的 src/transferPath.ts。收敛的是同一件事：
//
//	**一个入口** PushPath() / PullPath()
//	**一套词汇** 跳过 / 覆盖 / 改名（rz -y、rz -E、base64 降级都是实现细节）
//	**一份证据** 传完回读远端（哈希 + 大小 + 路径），成功失败都有依据
//
// 通道选择（**自动**，并把"用了哪条、为什么"明确告诉调用方）：
//
//	上传：`rz`（目标机装了 lrzsz）→ 没有 rz 就 **base64 分块塞进 shell**（不用装任何东西，慢但通用）
//	下载：`sz` → 没有 sz 就 **远端 base64 打出来、本地解码**
//	目录：先本地 `tar -czf` 打包 → 传 → 远端 `tar -xzf` 解开 → 清理临时包
//
// 堡垒机那条链路的现实：**目标机在堡垒机后面，只给了我们一条交互式 shell**，
// 所以 SFTP/端口转发都用不上 —— rz/sz 或 base64 是仅有的两条路。

// transferHost 传文件需要宿主提供的能力。
// 真实实现是 sessionTransferHost（包着 bastionSession）；测试里用假的，
// 这样「通道选择 / 校验判据 / 文案」都能在不开 SSH 的情况下测。
type transferHost interface {
	Name() string
	// Run 跑一条远端命令，返回输出与退出码
	Run(cmd string) (string, *int)
	// UploadRz 走 rz（ZMODEM）上传一个本地文件；mode 决定用哪个 rz 变体
	UploadRz(localPath, mode string) error
	// DownloadSz 走 sz 把远端文件拉回来，返回落盘路径
	DownloadSz(remotePath, localDir string) (string, error)
	Log(msg string)
}

// sessionTransferHost 真实宿主
type sessionTransferHost struct {
	s     *bastionSession
	label string
}

func newSessionTransferHost(s *bastionSession, label string) sessionTransferHost {
	return sessionTransferHost{s: s, label: label}
}

func (h sessionTransferHost) Name() string { return h.label }

func (h sessionTransferHost) Run(cmd string) (string, *int) {
	res, err := h.s.exec(cmd, execOpts{})
	if err != nil {
		return "", nil
	}
	return res.Output, res.ExitCode
}

func (h sessionTransferHost) UploadRz(localPath, mode string) error {
	return h.s.uploadFile(localPath, mode)
}

func (h sessionTransferHost) DownloadSz(remotePath, localDir string) (string, error) {
	return h.s.downloadFile(remotePath, localDir)
}

func (h sessionTransferHost) Log(msg string) {
	log.Printf("[transfer] %s %s", h.label, msg)
}

// ───────────────────────── 能力探测（每个会话探一次，缓存） ─────────────────────────

type capabilities struct {
	// Rz / Sz 是**当前传输模式对应**的那两个命令的意思：
	// zmodem 模式下是远端的 rz/sz，trzsz 模式下是远端的 trz/tsz。
	Rz     bool
	Sz     bool
	Base64 bool
	Tar    bool
	MD5Sum bool
	// Channel 是这次实际用的协议通道（写进日志与文案，别让人以为是 lrzsz）
	Channel string
	Note    string
}

var capsCache = struct {
	sync.Mutex
	m map[string]capabilities
}{m: map[string]capabilities{}}

// probeCapabilities 一条命令问全：`command -v` 不输出就说明没有。
//
// ⚠️ 探哪些命令**取决于当前跑的是哪套传输协议**（2026-09-15 修正）：
//   - zmodem 模式（Windows 版内嵌 lrzsz）：远端要有 **rz/sz**，本地是内嵌的 sz.exe/rz.exe；
//   - trzsz 模式（其它平台）：远端要有 **trz/tsz**，本地走 trzsz 协议。
//
// 探错一边的后果很实在：目标机装了 lrzsz、但桌面版跑在 trzsz 模式时，
// 探测会说"有 rz 通道"，可过滤器根本不认 ZMODEM —— 于是"看起来有通道、传起来传不动"。
// 所以按模式探，并且把模式和探到的东西都写进日志。
func probeCapabilities(h transferHost, refresh bool) capabilities {
	capsCache.Lock()
	if cached, ok := capsCache.m[h.Name()]; ok && !refresh {
		capsCache.Unlock()
		return cached
	}
	capsCache.Unlock()

	caps := capabilities{Channel: channelHint()}
	found := map[string]bool{}
	out, _ := h.Run(`for c in rz sz trz tsz base64 tar md5sum; do printf "%s=" "$c"; command -v "$c" >/dev/null 2>&1 && echo yes || echo no; done`)
	if strings.TrimSpace(out) == "" {
		// 探测拿不到任何输出：一律当作"没有"，走降级 —— 但要把原因带上，
		// 否则日志里只有"全是无"，看不出是"真没装"还是"命令没跑成"
		caps.Note = "探测失败：没有拿到任何输出（会话可能不在 shell 上）"
	}
	for _, line := range strings.Split(out, "\n") {
		m := regexp.MustCompile(`^(rz|sz|trz|tsz|base64|tar|md5sum)=(yes|no)`).FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		found[m[1]] = m[2] == "yes"
	}
	if useZmodem {
		caps.Rz, caps.Sz = found["rz"], found["sz"]
	} else {
		caps.Rz, caps.Sz = found["trz"], found["tsz"]
	}
	caps.Base64, caps.Tar, caps.MD5Sum = found["base64"], found["tar"], found["md5sum"]
	h.Log(fmt.Sprintf("传输模式：%s；目标机可用命令：rz=%s sz=%s trz=%s tsz=%s base64=%s tar=%s md5sum=%s%s",
		caps.Channel,
		yesNo(found["rz"]), yesNo(found["sz"]), yesNo(found["trz"]), yesNo(found["tsz"]),
		yesNo(caps.Base64), yesNo(caps.Tar), yesNo(caps.MD5Sum), noteSuffix(caps.Note)))
	h.Log(fmt.Sprintf("本次可用的文件通道：上传=%s 下载=%s",
		orNone(choosePushMethod(caps, false)), orNone(choosePullMethod(caps))))
	capsCache.Lock()
	capsCache.m[h.Name()] = caps
	capsCache.Unlock()
	return caps
}

// channelHint 当前模式对应的"远端要装什么"
func channelHint() string {
	if useZmodem {
		return "lrzsz（rz/sz）"
	}
	return "trzsz（trz/tsz）"
}

func orNone(s string) string {
	if s == "" {
		return "无（只能走 base64 降级）"
	}
	return s
}

// clearCapabilities 清缓存（测试 / 换会话时用）
func clearCapabilities(name string) {
	capsCache.Lock()
	defer capsCache.Unlock()
	if name == "" {
		capsCache.m = map[string]capabilities{}
		return
	}
	delete(capsCache.m, name)
}

func yesNo(b bool) string {
	if b {
		return "有"
	}
	return "无"
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "（" + note + "）"
}

// randomToken 生成 n 字节的十六进制随机串（哨兵里带随机后缀用）
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func base64StdEncode(data []byte) string       { return base64.StdEncoding.EncodeToString(data) }
func base64StdDecode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// choosePushMethod 纯逻辑：上传该用哪条通道（可单测）
func choosePushMethod(c capabilities, isDir bool) string {
	if isDir {
		if !c.Tar {
			return ""
		}
		if c.Rz {
			return "tar+rz"
		}
		if c.Base64 {
			return "tar+base64"
		}
		return ""
	}
	if c.Rz {
		return "rz"
	}
	if c.Base64 {
		return "base64"
	}
	return ""
}

// choosePullMethod 纯逻辑：下载该用哪条通道（可单测）
func choosePullMethod(c capabilities) string {
	if c.Sz {
		return "sz"
	}
	if c.Base64 {
		return "base64"
	}
	return ""
}

// chunkBase64 纯逻辑：base64 切块（可单测）。
// 块大小要远小于 shell/代理的命令行上限。
func chunkBase64(b64 string, size int) []string {
	if size <= 0 {
		size = 4000
	}
	out := make([]string, 0, len(b64)/size+1)
	for i := 0; i < len(b64); i += size {
		end := i + size
		if end > len(b64) {
			end = len(b64)
		}
		out = append(out, b64[i:end])
	}
	return out
}

// base64 降级的上限：太大就别硬塞（每块一次 exec，800KB 要两百多次往返）
const (
	b64SoftLimit = 256 * 1024
	b64HardLimit = 4 * 1024 * 1024
)

// methodLabel 通道名（给人和 AI 看的人话）
func methodLabel(m string) string {
	switch m {
	case "rz":
		return "rz（lrzsz）"
	case "sz":
		return "sz（lrzsz）"
	case "base64":
		return "base64 降级（目标机上没有 " + channelHint() + "）"
	case "tar+rz":
		return "tar 打包 + rz"
	case "tar+base64":
		return "tar 打包 + base64 降级"
	}
	return m
}

// ───────────────────────── 上传 ─────────────────────────

type pushOutcome struct {
	OK         bool
	Method     string
	LocalPath  string
	RemotePath string
	Bytes      int64
	Mode       string
	Skipped    bool
	Message    string
}

// overwriteMode 当前生效的覆盖方式。
// 扩展侧读的是 VS Code 设置（默认 skip）；桌面版没有设置项，用环境变量兜，
// 默认同样是 **skip**（宁可不覆盖：覆盖掉别人正在用的文件是不可逆的）。
func overwriteMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BASTIONSHELL_UPLOAD_OVERWRITE"))) {
	case "overwrite", "rename", "skip":
		return strings.ToLower(strings.TrimSpace(os.Getenv("BASTIONSHELL_UPLOAD_OVERWRITE")))
	}
	return "skip"
}

// PushPath 上传一个本机文件或目录
func PushPath(h transferHost, localPath, remoteDir, forceMethod string) pushOutcome {
	localPath = strings.TrimSpace(localPath)
	mode := overwriteMode()
	fail := func(message string, method string) pushOutcome {
		if method == "" {
			method = "rz"
		}
		return pushOutcome{OK: false, Method: method, LocalPath: localPath, Mode: mode, Message: message}
	}
	if localPath == "" {
		return fail("错误：需要一个本机路径", "")
	}
	st, err := os.Stat(localPath)
	if err != nil {
		return fail("错误：本机找不到这个路径："+localPath, "")
	}
	isDir := st.IsDir()
	name := filepath.Base(localPath)

	// 目标目录：**必须在同一条命令里 `cd` + `pwd`**。
	//
	// ⚠️ 不能分成两条（扩展侧 2026-09-14 由端到端测试抓到的真 bug）：每条远端命令都可能是
	// **独立的 shell**（非 pty 的 exec 就是这样），`cd` 不会保留，接着 `pwd` 会返回 $HOME ——
	// 文件被传到 home 目录，而校验因为"在同一个错地方自洽"还报成功。
	remoteDir = strings.TrimSpace(remoteDir)
	var probeOut string
	if remoteDir != "" {
		probeOut, _ = h.Run(fmt.Sprintf("cd %s 2>/dev/null && pwd || printf '__BASTION_CD_FAIL__\\n'", shellQuote(remoteDir)))
	} else {
		probeOut, _ = h.Run("pwd")
	}
	if remoteDir != "" && strings.Contains(probeOut, "__BASTION_CD_FAIL__") {
		return fail(fmt.Sprintf("错误：远端目录 %s 进不去。请确认目录存在且有权限；"+
			"也可以不传 remoteDir，直接传到会话当前目录。", remoteDir), "")
	}
	// 之后所有远端路径都用**绝对路径**：不依赖 cwd 是否保留，用户中途 cd 走了也不会传错地方
	dirAbs := lastAbsolutePath(probeOut)
	at := func(n string) string {
		if dirAbs == "" {
			return n
		}
		return strings.TrimRight(dirAbs, "/") + "/" + n
	}

	caps := probeCapabilities(h, false)
	method := forceMethod
	if method == "" {
		method = choosePushMethod(caps, isDir)
	}
	if method == "" {
		extra := ""
		if isDir {
			extra = "，或者没有 tar（传目录要用）"
		}
		return fail("❌ 目标机上既没有 **rz**（lrzsz），也没有 **base64**"+extra+" —— "+
			"两条通道都走不了。请让用户在这台机器上装一下（`yum install -y lrzsz` 或 `apt-get install -y lrzsz`），"+
			"或者把文件内容直接贴出来。", "")
	}

	// 目录：先本地打包（Windows 10+ 自带 tar.exe，Linux/macOS 也有）
	toSend := localPath
	var tempTar string
	sendBytes := st.Size()
	if isDir {
		packed, errMsg := packDir(localPath)
		if errMsg != "" {
			return fail("❌ 本地打包失败："+errMsg, method)
		}
		toSend = packed
		tempTar = packed
		if info, err := os.Stat(toSend); err == nil {
			sendBytes = info.Size()
		}
		h.Log(fmt.Sprintf("已把目录打包：%s（%s）", filepath.Base(toSend), fmtBytes(sendBytes)))
	}
	if tempTar != "" {
		defer func() { _ = os.Remove(tempTar) }()
	}

	sendName := filepath.Base(toSend)
	beforeCount := 0
	if globSafe(sendName) {
		beforeCount = len(remoteInfo(h, shellQuote(at(sendName))))
	}

	// 「跳过」不靠 rz 的行为，而是**传之前先看一眼**：存在就压根不传。
	// 这样"跳过"的语义是我们自己的，不依赖目标机 rz 的版本差异。
	if mode == "skip" && !isDir {
		if len(remoteInfo(h, shellQuote(at(sendName)))) > 0 {
			return pushOutcome{
				OK: false, Method: method, LocalPath: localPath, Bytes: sendBytes, Mode: mode, Skipped: true,
				Message: fmt.Sprintf("⚠️ 远端已存在同名文件，按「覆盖方式=%s」**跳过了，没有传**：%s\n"+
					"要覆盖就设环境变量 `BASTIONSHELL_UPLOAD_OVERWRITE=overwrite`（或让用户自己传）。", mode, name),
			}
		}
	}

	switch method {
	case "rz", "tar+rz":
		if err := h.UploadRz(toSend, mode); err != nil {
			return fail("❌ 上传失败："+err.Error(), method)
		}
	case "base64", "tar+base64":
		if msg, ok := pushViaBase64(h, toSend, at(sendName)); !ok {
			return fail("❌ base64 通道上传失败："+msg, method)
		}
	}

	// 目录：远端解开
	if isDir {
		tarRemote := at(sendName)
		_, rc := h.Run(fmt.Sprintf("tar -xzf %s -C %s && rm -f %s",
			shellQuote(tarRemote), shellQuote(orDot(dirAbs)), shellQuote(tarRemote)))
		if rc != nil && *rc != 0 {
			return fail(fmt.Sprintf("❌ 文件传上去了，但远端解包失败（tar 退出码 %d）—— "+
				"远端目录里留下了 %s，可以自己手动 `tar -xzf %s` 试。", *rc, sendName, sendName), method)
		}
		label := remoteDir
		if label == "" {
			label = "会话当前目录"
		}
		msg := fmt.Sprintf("✅ 目录已上传并解开：%s → %s\n通道：%s（打包后 %s；解包成功后临时包已删除）。",
			name, label, methodLabel(method), fmtBytes(sendBytes))
		if !caps.Rz {
			msg += "\n（目标机上没有 " + channelHint() + "，走的是 base64 降级）"
		}
		return pushOutcome{OK: true, Method: method, LocalPath: localPath, RemotePath: label,
			Bytes: sendBytes, Mode: mode, Message: msg}
	}

	shellWord := shellQuote(at(sendName))
	if mode == "rename" {
		shellWord += "*"
	}
	after := remoteInfo(h, shellWord)
	var target *remoteFileInfo
	for i := range after {
		if target == nil || after[i].MTime > target.MTime {
			target = &after[i]
		}
	}
	verdict := judgeUpload(localMD5(toSend), target, sendBytes, mode, beforeCount, len(after))

	methodNote := methodLabel(method)
	// 只在标签本身没说清原因时才补一句（methodLabel('base64') 已经带了「目标机没有 lrzsz」，
	// 再补一次就成了「base64 降级（目标机没有 lrzsz）（目标机没有 lrzsz，自动降级）」）
	if !caps.Rz && !strings.Contains(methodNote, "lrzsz") {
		methodNote += "（目标机上没有 " + channelHint() + "，自动降级）"
	}
	// 权限异常要**单独、显眼**地说一次。
	// 为什么要重复强调：真机上 AI 把 `----------`（0000）在总结里改写成 `-rw-r--r--` 了 ——
	// 一句夹在"判定依据"里的"读不了"它没当回事，而这件事的后果是**文件传上去等于白传**
	// （服务/非 root 用户读不了，部署的配置文件直接失效）。
	permWarning := ""
	if target != nil && !target.Readable {
		permWarning = "\n⚠️ **注意：远端这个文件当前账号读不了（权限 0000）—— 传上去等于白传**：服务/非 root 用户用不了它。\n" +
			"   先修一下再继续：`chmod 644 <文件>`（脚本用 `chmod 755`）；已经存在的老文件也要一起修。\n" +
			"   这条不是猜测 —— 回读时会**实打实检查 `[ -r ]`**，读不了就报出来。"
	}
	remoteShown := verdict.RemotePath
	if remoteShown == "" {
		if remoteDir != "" {
			remoteShown = remoteDir
		} else {
			remoteShown = "会话当前目录"
		}
	}
	head := "✅ 已上传"
	if !verdict.OK {
		head = "❌ 上传没成功"
	}
	return pushOutcome{
		OK: verdict.OK, Method: method, LocalPath: localPath, RemotePath: verdict.RemotePath,
		Bytes: sendBytes, Mode: mode,
		Message: fmt.Sprintf("%s %s（本机 %s）→ %s\n通道：%s；覆盖方式：%s\n判定依据（传完回读远端）：%s%s\n"+
			"—— 这条回读就是证据，**不要再用 ls / md5sum 自己验一遍**。",
			head, name, fmtBytes(sendBytes), remoteShown, methodNote, mode, verdict.Note, permWarning),
	}
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

// packDir 本地打包目录（tar -czf，Windows 10+ 自带 tar.exe）
func packDir(dir string) (string, string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err.Error()
	}
	parent := filepath.Dir(abs)
	base := filepath.Base(abs)
	tarPath := filepath.Join(filepath.Dir(dir), fmt.Sprintf(".%s.bastion-%s.tar.gz", base, strconv.FormatInt(time.Now().UnixNano()/1e6, 36)))
	cmd := exec.Command("tar", "-czf", tarPath, "-C", parent, base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, lookErr := exec.LookPath("tar"); lookErr != nil {
			return "", "本机没有可用的 tar（传目录要用它打包）"
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", msg
	}
	return tarPath, ""
}

// pushViaBase64 base64 分块塞进 shell（不需要目标机装任何东西）
func pushViaBase64(h transferHost, localFile, remotePath string) (string, bool) {
	info, err := os.Stat(localFile)
	if err != nil {
		return err.Error(), false
	}
	size := info.Size()
	if size > b64HardLimit {
		return fmt.Sprintf("文件 %s 超过 base64 通道的上限（%s）—— 这条路要一块一块发，大文件不划算",
			fmtBytes(size), fmtBytes(b64HardLimit)), false
	}
	data, err := os.ReadFile(localFile)
	if err != nil {
		return err.Error(), false
	}
	b64 := base64StdEncode(data)
	chunks := chunkBase64(b64, 4000)
	name := filepath.Base(localFile)
	part := remotePath + ".bastion-part"
	slow := ""
	if size > b64SoftLimit {
		slow = " —— 文件不小，会比较慢"
	}
	h.Log(fmt.Sprintf("目标机没有 rz，改用 base64 分块上传：%s（%s，%d 块）%s", name, fmtBytes(size), len(chunks), slow))

	for i, chunk := range chunks {
		op := ">"
		if i > 0 {
			op = ">>"
		}
		_, rc := h.Run(fmt.Sprintf("printf '%%s' '%s' %s %s", chunk, op, shellQuote(part)))
		if rc != nil && *rc != 0 {
			h.Run("rm -f " + shellQuote(part))
			return fmt.Sprintf("第 %d/%d 块写入失败（退出码 %d）", i+1, len(chunks), *rc), false
		}
	}
	_, rc := h.Run(fmt.Sprintf("base64 -d < %s > %s && rm -f %s", shellQuote(part), shellQuote(remotePath), shellQuote(part)))
	if rc != nil && *rc != 0 {
		return fmt.Sprintf("base64 解码失败（退出码 %d）", *rc), false
	}
	return "", true
}

// ───────────────────────── 下载 ─────────────────────────

type pullOutcome struct {
	OK        bool
	Method    string
	LocalPath string
	Bytes     int64
	Message   string
}

// PullPath 把一个远端文件拉回本机
func PullPath(h transferHost, remotePath, localDir, forceMethod string) pullOutcome {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return pullOutcome{Method: "sz", Message: "错误：需要 remotePath（远端文件路径）"}
	}
	localDir = strings.TrimSpace(localDir)
	if localDir == "" {
		return pullOutcome{Method: "sz", Message: "错误：需要 localDir（本机落盘目录）"}
	}
	caps := probeCapabilities(h, false)
	method := forceMethod
	if method == "" {
		method = choosePullMethod(caps)
	}
	if method == "" {
		return pullOutcome{Method: "sz", Message: "❌ 目标机上既没有 **" + channelHint() + "**，也没有 **base64** —— 两条通道都走不了。" +
			"请让用户装一下（zmodem 模式装 lrzsz，trzsz 模式装 trzsz），或者让他 `cat` 出来给你看。"}
	}

	if method == "sz" {
		localPath, err := h.DownloadSz(remotePath, localDir)
		if err != nil {
			return pullOutcome{Method: "sz", Message: fmt.Sprintf("❌ 下载失败：%v\n（远端：%s）", err, remotePath)}
		}
		var bytes int64
		if info, err := os.Stat(localPath); err == nil {
			bytes = info.Size()
		}
		return pullOutcome{OK: true, Method: "sz", LocalPath: localPath, Bytes: bytes,
			Message: fmt.Sprintf("✅ 已下载 %s（%s）→ %s\n通道：%s",
				filepath.Base(localPath), fmtBytes(bytes), localPath, methodLabel("sz"))}
	}

	return pullViaBase64(h, remotePath, localDir)
}

// pullViaBase64 远端 base64 打出来、本地解码（目标机没有 sz 时的通用退路）
func pullViaBase64(h transferHost, remotePath, localDir string) pullOutcome {
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return pullOutcome{Method: "base64", Message: "创建本地目录失败：" + err.Error()}
	}
	sizeOut, _ := h.Run(fmt.Sprintf("stat -c %%s %s 2>/dev/null || echo NA", shellQuote(remotePath)))
	var size int64
	hasSize := false
	if m := regexp.MustCompile(`(\d+)`).FindStringSubmatch(sizeOut); m != nil {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			size, hasSize = v, true
		}
	}
	if hasSize && size > b64HardLimit {
		return pullOutcome{Method: "base64", Message: fmt.Sprintf(
			"❌ 远端文件 %s 超过 base64 通道上限（%s）—— 这条路要整段打出来，大文件不划算",
			fmtBytes(size), fmtBytes(b64HardLimit))}
	}
	sizeNote := ""
	if hasSize {
		sizeNote = "（" + fmtBytes(size) + "）"
	}
	h.Log("目标机没有 sz，改用 base64 拉取：" + remotePath + sizeNote)

	// 用哨兵把 payload 夹住：exec 的输出里还带着命令回显，不能直接当 base64 解。
	//
	// ⚠️ 与扩展侧的一处**有意不同**：哨兵带随机后缀（扩展侧是固定字符串），
	// 而且下面用 LastIndex 取。原因就是我们自己在 exec 那层踩过的坑 ——
	// pty 折行会把命令回显切碎，切碎后残片里**仍然带着固定哨兵字符串**，
	// 用 indexOf 就会从残片开始截，取到一堆命令原文。
	nonce := randomToken(6)
	beginTok := "__B64_BEGIN_" + nonce + "__"
	endTok := "__B64_END_" + nonce + "__"
	// ⚠️ **两个** base64 调用都要吞掉 stderr（`2>/dev/null`）。
	// 扩展侧的写法是 `base64 -w0 X 2>/dev/null || base64 X` —— 兜底那条没吞 stderr，
	// 于是文件不存在时 `base64: ...: No such file or directory` **会混进 payload 里**，
	// 解出来就是一句 "illegal base64 data at input byte 6"（2026-09-15 真机抓到的）。
	out, _ := h.Run(fmt.Sprintf("printf '%s\\n'; base64 -w0 %s 2>/dev/null || base64 %s 2>/dev/null; printf '\\n%s\\n'",
		beginTok, shellQuote(remotePath), shellQuote(remotePath), endTok))
	begin := strings.LastIndex(out, beginTok)
	end := strings.LastIndex(out, endTok)
	if begin < 0 || end < 0 || end <= begin {
		return pullOutcome{Method: "base64", Message: fmt.Sprintf(
			"❌ 没能从远端拿到 base64 内容（可能是路径不对或没权限）：%s", remotePath)}
	}
	b64 := strings.Join(strings.Fields(out[begin+len(beginTok):end]), "")
	if b64 == "" {
		// 空 payload 的三种可能分开说清楚，**不能**当成"传回了一个空文件"就报成功 ——
		// 那会让 AI 以为拿到了内容。
		if !hasSize {
			return pullOutcome{Method: "base64", Message: fmt.Sprintf(
				"❌ 远端文件不存在或当前账号读不到：%s\n"+
					"（远端 `stat` 拿不到大小，base64 也是空的；先确认路径和权限，别把它当成空文件。）", remotePath)}
		}
		if size > 0 {
			return pullOutcome{Method: "base64", Message: fmt.Sprintf(
				"❌ 远端报告这个文件有 %s，但一个字节都没回出来：%s\n"+
					"（多半是没读权限，或者远端的 base64 不接受这些参数。）", fmtBytes(size), remotePath)}
		}
	}
	buf, err := base64StdDecode(b64)
	if err != nil {
		return pullOutcome{Method: "base64", Message: fmt.Sprintf(
			"❌ 远端回出来的内容不是有效的 base64（可能有别的输出混进来了）：%s", remotePath)}
	}
	localPath := filepath.Join(localDir, filepath.Base(remotePath))
	if err := os.WriteFile(localPath, buf, 0o644); err != nil {
		return pullOutcome{Method: "base64", Message: "❌ 本地写盘失败：" + err.Error()}
	}
	return pullOutcome{OK: true, Method: "base64", LocalPath: localPath, Bytes: int64(len(buf)),
		Message: fmt.Sprintf("✅ 已下载 %s（%s）→ %s\n通道：base64（目标机上没有 %s，自动降级）",
			filepath.Base(remotePath), fmtBytes(int64(len(buf))), localPath, channelHint())}
}

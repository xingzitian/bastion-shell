package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 传输工具的逻辑测试。
//
// 分成两层：
//   - **纯函数**（通道选择 / 分块 / 校验判据 / 引用 / 格式化）：直接断言；
//   - **假宿主**：模拟一台远端机的文件系统与那几条命令，把整条 push/pull 流程跑通 ——
//     这样「方法选择 → 传 → 回读校验 → 文案」是可测的，不需要 SSH。
//     真机（有 sshd 的靶机）那条路见 transfer_e2e_test.go。

// ───────────────────────── 纯函数 ─────────────────────────

func TestChooseMethods(t *testing.T) {
	full := capabilities{Rz: true, Sz: true, Base64: true, Tar: true, MD5Sum: true}
	noRz := capabilities{Sz: true, Base64: true, Tar: true}
	noTar := capabilities{Rz: true, Base64: true}
	bare := capabilities{Base64: true}

	cases := []struct {
		caps  capabilities
		isDir bool
		want  string
	}{
		{full, false, "rz"},
		{full, true, "tar+rz"},
		{noRz, false, "base64"},
		{noRz, true, "tar+base64"},
		{noTar, true, ""}, // 目录必须有 tar
		{bare, false, "base64"},
		{capabilities{}, false, ""}, // 什么都没有
	}
	for _, c := range cases {
		if got := choosePushMethod(c.caps, c.isDir); got != c.want {
			t.Errorf("choosePushMethod(isDir=%v) = %q，想要 %q", c.isDir, got, c.want)
		}
	}
	noSz := capabilities{Rz: true, Base64: true}
	if got := choosePullMethod(noSz); got != "base64" {
		t.Errorf("没有 sz 应该降级 base64，得到 %q", got)
	}
	if got := choosePullMethod(full); got != "sz" {
		t.Errorf("有 sz 应该走 sz，得到 %q", got)
	}
	if got := choosePullMethod(capabilities{}); got != "" {
		t.Errorf("什么都没有应该返回空，得到 %q", got)
	}
}

func TestChunkBase64RoundTrip(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("hello world ", 1000)))
	chunks := chunkBase64(b64, 4000)
	if joined := strings.Join(chunks, ""); joined != b64 {
		t.Fatal("分块拼回去不等于原文（分块错一个字符，远端解码就废）")
	}
	if len(chunks) != (len(b64)+3999)/4000 {
		t.Fatalf("块数不对：%d", len(chunks))
	}
	for i, c := range chunks[:len(chunks)-1] {
		if len(c) != 4000 {
			t.Fatalf("第 %d 块长度应是 4000，得到 %d", i, len(c))
		}
	}
	if len(chunkBase64("", 10)) != 0 {
		t.Fatal("空输入应该是零块")
	}
}

func TestMethodLabelIsHumanWords(t *testing.T) {
	// 通道名是人话，不该把 rz -y / rz -E 这种实现细节漏给用户
	for _, m := range []string{"rz", "sz", "base64", "tar+rz", "tar+base64"} {
		label := methodLabel(m)
		if strings.Contains(label, "-y") || strings.Contains(label, "-E") {
			t.Fatalf("通道名里漏了实现细节：%q", label)
		}
		if label == "" {
			t.Fatalf("%s 没有可读名", m)
		}
	}
}

func TestGlobSafeAndShellQuote(t *testing.T) {
	if !globSafe("app.conf") || !globSafe("中文名.txt") || !globSafe("a-b_c.1") {
		t.Fatal("正常文件名应该判为可安全拼 glob")
	}
	if globSafe("a b.txt") || globSafe("a*.txt") || globSafe("a;rm -rf /") {
		t.Fatal("带空格/通配/命令分隔符的文件名不能硬拼进 glob")
	}
	if got := shellQuote("a b'txt"); got != `'a b'\''txt'` {
		t.Fatalf("shell 引用不对：%s", got)
	}
}

func TestLastAbsolutePath(t *testing.T) {
	out := "$ cd /opt/app 2>/dev/null && pwd\r\n/opt/app\r\n[deploy@web ~]$"
	if got := lastAbsolutePath(out); got != "/opt/app" {
		t.Fatalf("应该取最后一个绝对路径行，得到 %q", got)
	}
	// 命令回显里那行（`cd /opt/app && pwd`）不能被当成结果 —— 它不以 / 开头
	if got := lastAbsolutePath("cd /tmp && pwd\n/tmp"); got != "/tmp" {
		t.Fatalf("得到 %q", got)
	}
	if got := lastAbsolutePath("__BASTION_CD_FAIL__"); got != "" {
		t.Fatalf("没有绝对路径时应该是空串，得到 %q", got)
	}
}

func TestFmtBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KB", 792 * 1024: "792 KB", 5 * 1024 * 1024: "5.0 MB"}
	for n, want := range cases {
		if got := fmtBytes(n); got != want {
			t.Errorf("fmtBytes(%d) = %q，想要 %q", n, got, want)
		}
	}
}

func TestJudgeUploadVerdicts(t *testing.T) {
	same := "d41d8cd98f00b204e9800998ecf8427e"
	// 哈希一致 = 铁证
	r := judgeUpload(same, &remoteFileInfo{Hash: same, Size: 10, HasSize: true, Path: "/tmp/a"}, 10, "overwrite", 0, 1)
	if !r.OK || !strings.Contains(r.Note, "逐字节一致") {
		t.Fatalf("哈希一致应该判成功：%+v", r)
	}
	// 哈希不一致
	r = judgeUpload(same, &remoteFileInfo{Hash: "ffffffffffffffffffffffffffffffff", Size: 10, HasSize: true, Path: "/tmp/a"}, 10, "overwrite", 0, 1)
	if r.OK || !strings.Contains(r.Note, "内容对不上") {
		t.Fatalf("哈希不一致应该判失败：%+v", r)
	}
	// 远端找不到
	if r := judgeUpload(same, nil, 10, "overwrite", 0, 0); r.OK {
		t.Fatal("远端找不到文件必须判失败")
	}
	// 算不出哈希 → 弱判据，且要说清为什么
	r = judgeUpload(same, &remoteFileInfo{Size: 10, HasSize: true, Readable: true, Path: "/tmp/a"}, 10, "overwrite", 0, 1)
	if !r.OK || !strings.Contains(r.Note, "弱证据") || !strings.Contains(r.Note, "缺少 md5sum") {
		t.Fatalf("弱判据文案不合格：%+v", r)
	}
	r = judgeUpload(same, &remoteFileInfo{Size: 10, HasSize: true, Readable: false, Path: "/tmp/a"}, 10, "overwrite", 0, 1)
	if !r.OK || !strings.Contains(r.Note, "读不了") {
		t.Fatalf("不可读时要单独说清：%+v", r)
	}
	// 大小不一致
	if r := judgeUpload(same, &remoteFileInfo{Size: 9, HasSize: true, Readable: true, Path: "/tmp/a"}, 10, "overwrite", 0, 1); r.OK {
		t.Fatalf("大小不一致必须判失败：%+v", r)
	}
	// 改名模式：没多出新文件 = 可疑
	r = judgeUpload(same, &remoteFileInfo{Hash: same, Size: 10, HasSize: true, Path: "/tmp/a"}, 10, "rename", 1, 1)
	if r.OK || !strings.Contains(r.Note, "改名模式") {
		t.Fatalf("改名模式没看到新文件要判失败：%+v", r)
	}
	// mtime 只是说明，**不参与判定**（ZMODEM 会把源文件 mtime 带过去）
	oldM := int64(1600000000)
	r = judgeUpload(same, &remoteFileInfo{Hash: same, Size: 10, HasSize: true, MTime: oldM, HasMTime: true, Path: "/tmp/a"}, 10, "overwrite", 0, 1)
	if !r.OK || !strings.Contains(r.Note, "源文件的时间戳") {
		t.Fatalf("老的 mtime 不该影响判定：%+v", r)
	}
}

// ───────────────────────── 假宿主 ─────────────────────────

// fakeHost 模拟一台远端机：只认传输工具真正会用到的那几条命令
type fakeHost struct {
	name   string
	caps   map[string]bool
	files  map[string]string // 绝对路径 → 内容
	mtimes map[string]int64  // 绝对路径 → mtime（ZMODEM 会把源文件 mtime 带过来）
	dirs   map[string]bool
	pwd    string
	logs   []string
	rzFail string // 非空 = rz 上传直接失败
	rzDone []string
}

func newFakeHost(caps ...string) *fakeHost {
	h := &fakeHost{
		name:   fmt.Sprintf("fake@host-%d", atomic.AddInt64(&fakeHostSeq, 1)),
		caps:   map[string]bool{},
		files:  map[string]string{},
		mtimes: map[string]int64{},
		dirs:   map[string]bool{"/": true, "/opt": true, "/opt/app": true},
		pwd:    "/home/deploy",
	}
	for _, c := range caps {
		h.caps[c] = true
	}
	return h
}

func (h *fakeHost) Name() string { return h.name }
func (h *fakeHost) Log(msg string) {
	h.logs = append(h.logs, msg)
}

func (h *fakeHost) UploadRz(localPath, mode string) error {
	if h.rzFail != "" {
		return fmt.Errorf("%s", h.rzFail)
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	target := h.pwd + "/" + filepath.Base(localPath)
	if mode == "rename" {
		if _, exists := h.files[target]; exists {
			target = target + ".1"
		}
	}
	h.files[target] = string(data)
	// ZMODEM 会把**源文件的 mtime** 带过去：新传上去的文件 mtime 是新的，
	// 远端原有的旧文件还是老的 —— 「改名模式」就是靠这个差值找到新文件的。
	h.mtimes[target] = 1800000000
	h.rzDone = append(h.rzDone, mode+" "+filepath.Base(localPath))
	return nil
}

func (h *fakeHost) DownloadSz(remotePath, localDir string) (string, error) {
	content, ok := h.files[remotePath]
	if !ok {
		return "", fmt.Errorf("远端没有这个文件")
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return "", err
	}
	local := filepath.Join(localDir, filepath.Base(remotePath))
	if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
		return "", err
	}
	return local, nil
}

var (
	reChunkWrite = regexp.MustCompile(`^printf '%s' '([A-Za-z0-9+/=]*)' (>>?) '([^']*)'$`)
	reB64Decode  = regexp.MustCompile(`^base64 -d < '([^']*)' > '([^']*)' && rm -f '([^']*)'$`)
	reStatSize   = regexp.MustCompile(`^stat -c %s '([^']*)' 2>/dev/null \|\| echo NA$`)
	reCdProbe    = regexp.MustCompile(`^cd '([^']*)' 2>/dev/null && pwd \|\| printf '__BASTION_CD_FAIL__\\n'$`)
	reB64Pull    = regexp.MustCompile(`^printf '(__B64_BEGIN_[0-9a-f]+__)\\n'; base64 -w0 '([^']*)' 2>/dev/null \|\| base64 '([^']*)' 2>/dev/null; printf '\\n(__B64_END_[0-9a-f]+__)\\n'$`)
)

func (h *fakeHost) Run(cmd string) (string, *int) {
	zero := 0
	if strings.HasPrefix(cmd, "for c in rz sz trz tsz") {
		var b strings.Builder
		for _, c := range []string{"rz", "sz", "trz", "tsz", "base64", "tar", "md5sum"} {
			yn := "no"
			if h.caps[c] {
				yn = "yes"
			}
			fmt.Fprintf(&b, "%s=%s\n", c, yn)
		}
		return b.String(), &zero
	}
	if cmd == "pwd" {
		return h.pwd + "\n", &zero
	}
	if m := reCdProbe.FindStringSubmatch(cmd); m != nil {
		if h.dirs[m[1]] {
			// ⚠️ 这里模拟的是**常驻的 pty 会话**：cd 之后 shell 就留在那个目录里，
			// 后面的 rz 上传也是落到那儿（真机行为）。非 pty 的一次性 exec 不会这样 ——
			// 那正是扩展侧那个「文件传到 home 目录、校验还在同一个错地方自洽」的 bug 成因。
			h.pwd = m[1]
			return h.pwd + "\n", &zero
		}
		return "__BASTION_CD_FAIL__\n", &zero
	}
	if m := reChunkWrite.FindStringSubmatch(cmd); m != nil {
		chunk, op, part := m[1], m[2], m[3]
		if op == ">" {
			h.files[part] = chunk
		} else {
			h.files[part] += chunk
		}
		return "", &zero
	}
	if m := reB64Decode.FindStringSubmatch(cmd); m != nil {
		part, target := m[1], m[2]
		raw, err := base64.StdEncoding.DecodeString(h.files[part])
		if err != nil {
			return "base64: invalid input\n", intPtr(1)
		}
		h.files[target] = string(raw)
		delete(h.files, part)
		return "", &zero
	}
	if m := reStatSize.FindStringSubmatch(cmd); m != nil {
		if content, ok := h.files[m[1]]; ok {
			return fmt.Sprintf("%d\n", len(content)), &zero
		}
		return "NA\n", &zero
	}
	if m := reB64Pull.FindStringSubmatch(cmd); m != nil {
		content, ok := h.files[m[2]]
		if !ok {
			return m[1] + "\n\n" + m[4] + "\n", &zero
		}
		return m[1] + "\n" + base64.StdEncoding.EncodeToString([]byte(content)) + "\n" + m[4] + "\n", &zero
	}
	if strings.HasPrefix(cmd, "for f in ") {
		return h.remoteInfoOutput(cmd), &zero
	}
	if strings.HasPrefix(cmd, "tar -xzf ") {
		// 解包：把 <dir>/<name>.tar.gz 里的内容"解开"（假宿主只记一笔）
		return "", &zero
	}
	if strings.HasPrefix(cmd, "rm -f ") {
		return "", &zero
	}
	return "", &zero
}

func intPtr(n int) *int { return &n }

// remoteInfoOutput 模拟 `remoteInfo` 那条命令：按文件名/前缀匹配
func (h *fakeHost) remoteInfoOutput(cmd string) string {
	word := strings.TrimSuffix(strings.TrimPrefix(cmd, "for f in "), `; do [ -f "$f" ] || continue; `)
	word = strings.SplitN(word, ";", 2)[0]
	prefix := false
	if strings.HasSuffix(word, "*") {
		prefix = true
		word = strings.TrimSuffix(word, "*")
	}
	word = strings.Trim(word, "'")
	var b strings.Builder
	for path, content := range h.files {
		base := filepath.Base(path)
		matched := base == word || path == word
		if prefix {
			matched = strings.HasPrefix(base, word) || strings.HasPrefix(path, word)
		}
		if !matched {
			continue
		}
		mt := h.mtimes[path]
		if mt == 0 {
			mt = 1700000000
		}
		fmt.Fprintf(&b, "%s|%d|%d|yes|%s\n", md5Hex([]byte(content)), len(content), mt, path)
	}
	return b.String()
}

// ───────────────────────── 流程（假宿主） ─────────────────────────

func writeLocalFile(t *testing.T, dir, name string, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPushViaRzWithHashEvidence(t *testing.T) {
	h := newFakeHost("trz", "base64", "tar", "md5sum")
	src := writeLocalFile(t, t.TempDir(), "app.conf", "listen 80\n")
	out := PushPath(h, src, "/opt/app", "")
	if !out.OK {
		t.Fatalf("应该成功：%s", out.Message)
	}
	if out.Method != "rz" {
		t.Fatalf("有 rz 就该走 rz，得到 %q", out.Method)
	}
	if len(h.rzDone) != 1 {
		t.Fatalf("rz 上传没被调用：%v", h.rzDone)
	}
	if !strings.Contains(out.Message, "逐字节一致") || !strings.Contains(out.Message, "app.conf") {
		t.Fatalf("结果里应该带回读证据：%s", out.Message)
	}
	if !strings.Contains(out.Message, "不要再用 ls / md5sum 自己验一遍") {
		t.Fatalf("应该明确告诉 AI 不用自己再验一遍：%s", out.Message)
	}
	if _, ok := h.files["/opt/app/app.conf"]; !ok {
		t.Fatalf("文件没落到远端目标目录：%v", h.files)
	}
}

func TestPushWithoutRzFallsBackToBase64(t *testing.T) {
	h := newFakeHost("base64", "md5sum")
	content := strings.Repeat("x=1\n", 5000)
	src := writeLocalFile(t, t.TempDir(), "big.conf", content)
	out := PushPath(h, src, "/opt/app", "")
	if !out.OK {
		t.Fatalf("应该走 base64 成功：%s", out.Message)
	}
	if out.Method != "base64" {
		t.Fatalf("没有 rz 应该降级 base64，得到 %q", out.Method)
	}
	if h.files["/opt/app/big.conf"] != content {
		t.Fatal("base64 传上去的内容不一致")
	}
	// 降级要说明白「为什么降级」——措辞会随传输模式变（trzsz 模式下是
	// 「目标机上没有 trzsz（trz/tsz）」），所以查的是含义，不是某一句话
	if !strings.Contains(out.Message, "降级") || !strings.Contains(out.Message, "没有") {
		t.Fatalf("降级要说明白：%s", out.Message)
	}
}

func TestPushRefusesWhenNoChannelAvailable(t *testing.T) {
	h := newFakeHost() // 什么都没有
	src := writeLocalFile(t, t.TempDir(), "a.txt", "hi")
	out := PushPath(h, src, "", "")
	if out.OK {
		t.Fatal("两条通道都没有时必须失败")
	}
	if !strings.Contains(out.Message, "既没有") || !strings.Contains(out.Message, "lrzsz") {
		t.Fatalf("失败原因要说清并给出办法：%s", out.Message)
	}
}

func TestPushBadRemoteDirFails(t *testing.T) {
	h := newFakeHost("trz", "md5sum")
	src := writeLocalFile(t, t.TempDir(), "a.txt", "hi")
	out := PushPath(h, src, "/no/such/dir", "")
	if out.OK || !strings.Contains(out.Message, "进不去") {
		t.Fatalf("远端目录进不去必须失败且说清楚：%s", out.Message)
	}
}

func TestPushSkipModeDoesNotUpload(t *testing.T) {
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "skip")
	h := newFakeHost("trz", "md5sum")
	h.files["/opt/app/a.txt"] = "旧内容"
	src := writeLocalFile(t, t.TempDir(), "a.txt", "新内容")
	out := PushPath(h, src, "/opt/app", "")
	if out.OK || !out.Skipped {
		t.Fatalf("skip 模式应该跳过：%+v", out)
	}
	if len(h.rzDone) != 0 {
		t.Fatal("跳过时不该真的上传")
	}
	if h.files["/opt/app/a.txt"] != "旧内容" {
		t.Fatal("跳过时不该动远端文件")
	}
	if !strings.Contains(out.Message, "跳过了，没有传") {
		t.Fatalf("跳过要明说：%s", out.Message)
	}
}

func TestPushOverwriteModeReplaces(t *testing.T) {
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "overwrite")
	h := newFakeHost("trz", "md5sum")
	h.files["/opt/app/a.txt"] = "旧内容"
	src := writeLocalFile(t, t.TempDir(), "a.txt", "新内容")
	out := PushPath(h, src, "/opt/app", "")
	if !out.OK {
		t.Fatalf("overwrite 模式应该成功：%s", out.Message)
	}
	if h.files["/opt/app/a.txt"] != "新内容" {
		t.Fatal("overwrite 模式应该覆盖远端文件")
	}
}

func TestPushRenameModeUsesRzE(t *testing.T) {
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "rename")
	h := newFakeHost("trz", "md5sum")
	h.files["/opt/app/a.txt"] = "旧内容"
	src := writeLocalFile(t, t.TempDir(), "a.txt", "新内容")
	out := PushPath(h, src, "/opt/app", "")
	if !out.OK {
		t.Fatalf("rename 模式应该成功：%s", out.Message)
	}
	if len(h.rzDone) != 1 || !strings.HasPrefix(h.rzDone[0], "rename ") {
		t.Fatalf("rename 模式应该用 rz -E 那条路：%v", h.rzDone)
	}
	if h.files["/opt/app/a.txt"] != "旧内容" {
		t.Fatal("改名模式不能覆盖原文件")
	}
	if _, ok := h.files["/opt/app/a.txt.1"]; !ok {
		t.Fatalf("应该多出一个改名后的文件：%v", h.files)
	}
}

func TestPushDirectoryPacksAndExtracts(t *testing.T) {
	h := newFakeHost("tar", "base64", "md5sum")
	dir := t.TempDir()
	sub := filepath.Join(dir, "conf")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLocalFile(t, sub, "a.conf", "a=1\n")
	out := PushPath(h, sub, "/opt/app", "")
	if !out.OK {
		t.Fatalf("目录上传应该成功：%s", out.Message)
	}
	if out.Method != "tar+base64" {
		t.Fatalf("没有 rz 的目录应该走 tar+base64，得到 %q", out.Method)
	}
	if !strings.Contains(out.Message, "目录已上传并解开") {
		t.Fatalf("文案该说清是目录：%s", out.Message)
	}
	// 临时 tar 包应该被删掉（本地不留垃圾）
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "bastion-") {
			t.Fatalf("临时 tar 包没清理：%s", e.Name())
		}
	}
}

func TestPullViaSz(t *testing.T) {
	h := newFakeHost("tsz", "md5sum")
	h.files["/var/log/app.log"] = "日志内容\n"
	dir := t.TempDir()
	out := PullPath(h, "/var/log/app.log", dir, "")
	if !out.OK || out.Method != "sz" {
		t.Fatalf("有 sz 应该走 sz：%+v", out)
	}
	got, err := os.ReadFile(out.LocalPath)
	if err != nil || string(got) != "日志内容\n" {
		t.Fatalf("落盘内容不对：%v %q", err, got)
	}
}

func TestPullWithoutSzFallsBackToBase64(t *testing.T) {
	h := newFakeHost("base64", "md5sum")
	content := strings.Repeat("line\n", 3000)
	h.files["/etc/app.conf"] = content
	dir := t.TempDir()
	out := PullPath(h, "/etc/app.conf", dir, "")
	if !out.OK || out.Method != "base64" {
		t.Fatalf("没有 sz 应该降级 base64：%+v", out)
	}
	got, err := os.ReadFile(out.LocalPath)
	if err != nil || string(got) != content {
		t.Fatalf("base64 拉回来的内容不一致（长度 %d）", len(got))
	}
	// 降级要说明白「为什么降级」——措辞会随传输模式变（trzsz 模式下是
	// 「目标机上没有 trzsz（trz/tsz）」），所以查的是含义，不是某一句话
	if !strings.Contains(out.Message, "降级") || !strings.Contains(out.Message, "没有") {
		t.Fatalf("降级要说明白：%s", out.Message)
	}
}

func TestPullMissingFileFailsClearly(t *testing.T) {
	h := newFakeHost("base64", "md5sum")
	out := PullPath(h, "/nope/missing.txt", t.TempDir(), "")
	if out.OK {
		t.Fatal("远端没有这个文件必须失败")
	}
	if !strings.Contains(out.Message, "不存在或当前账号读不到") {
		t.Fatalf("失败原因要说清：%s", out.Message)
	}
}

func TestProbeCapabilitiesCached(t *testing.T) {
	h := newFakeHost("base64", "md5sum")
	clearCapabilities("")
	c1 := probeCapabilities(h, false)
	if !c1.Base64 || c1.Rz {
		t.Fatalf("探测结果不对：%+v", c1)
	}
	// 第二次不该再跑命令：把 caps 改掉，仍然返回缓存值
	h.caps["trz"] = true
	if c2 := probeCapabilities(h, false); c2.Rz {
		t.Fatal("应该命中缓存")
	}
	if c3 := probeCapabilities(h, true); !c3.Rz {
		t.Fatal("refresh=true 时应该重新探测")
	}
}

func TestProbeCapabilitiesEmptyOutputMeansNone(t *testing.T) {
	h := &fakeHost{name: "silent", caps: map[string]bool{"rz": true}, files: map[string]string{}, dirs: map[string]bool{}}
	// 一个什么都不回应的宿主（比如会话不在 shell 上）：Run 返回空
	silent := &silentHost{}
	clearCapabilities("")
	caps := probeCapabilities(silent, false)
	if caps.Rz || caps.Base64 {
		t.Fatalf("拿不到输出时应该一律当作没有：%+v", caps)
	}
	if caps.Note == "" {
		t.Fatal("要带上原因，否则日志里只有「全是无」，看不出是没装还是没跑成")
	}
	_ = h
}

type silentHost struct{}

func (silentHost) Name() string                              { return "silent" }
func (silentHost) Run(string) (string, *int)                 { return "", nil }
func (silentHost) UploadRz(string, string) error             { return fmt.Errorf("no") }
func (silentHost) DownloadSz(string, string) (string, error) { return "", fmt.Errorf("no") }
func (silentHost) Log(string)                                {}

func TestPackDirLocalTar(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "srcdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLocalFile(t, sub, "x.txt", "x")
	tarPath, errMsg := packDir(sub)
	if errMsg != "" {
		t.Skipf("本机没有可用的 tar（%s）—— 跳过（正式环境 Windows 10+ 自带 tar.exe）", errMsg)
	}
	defer func() { _ = os.Remove(tarPath) }()
	info, err := os.Stat(tarPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("打出来的包不对：%v", err)
	}
	if !strings.HasSuffix(tarPath, ".tar.gz") {
		t.Fatalf("包名不对：%s", tarPath)
	}
	// 解出来应该能看到里面的文件
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if data, err := execCommand("tar", "-xzf", tarPath, "-C", out); err != nil {
		t.Fatalf("本地 tar 解包失败：%v %s", err, data)
	}
	if _, err := os.Stat(filepath.Join(out, "srcdir", "x.txt")); err != nil {
		t.Fatalf("解开后没看到文件：%v", err)
	}
}

func TestOverwriteModeDefaultIsSkip(t *testing.T) {
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "")
	if got := overwriteMode(); got != "skip" {
		t.Fatalf("默认必须是 skip（覆盖掉别人正在用的文件是不可逆的），得到 %q", got)
	}
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "乱写")
	if got := overwriteMode(); got != "skip" {
		t.Fatalf("非法值应退回 skip，得到 %q", got)
	}
	t.Setenv("BASTIONSHELL_UPLOAD_OVERWRITE", "OVERWRITE")
	if got := overwriteMode(); got != "overwrite" {
		t.Fatalf("应该大小写不敏感，得到 %q", got)
	}
}

// fakeHostSeq 让每个假宿主有唯一名字：能力探测是按会话名缓存的，
// 名字一样会让上一个用例的探测结果串到下一个用例里
var fakeHostSeq int64

func execCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// 探测必须**按当前传输模式**看命令：zmodem 模式看远端的 rz/sz，trzsz 模式看 trz/tsz。
// 探错的后果很实在：目标机装了 lrzsz 但桌面版跑在 trzsz 模式时，会说"有 rz 通道"，
// 而过滤器根本不认 ZMODEM —— 看起来有通道、传起来传不动。
func TestProbeIsModeAware(t *testing.T) {
	onlyLrzsz := newFakeHost("rz", "sz", "base64", "md5sum")
	clearCapabilities("")
	caps := probeCapabilities(onlyLrzsz, false)
	if caps.Rz || caps.Sz {
		t.Fatalf("trzsz 模式下不该把远端的 rz/sz 当成可用通道：%+v", caps)
	}
	if !caps.Base64 {
		t.Fatal("base64 仍然是可用的降级通道")
	}
	if !strings.Contains(caps.Channel, "trzsz") {
		t.Fatalf("能力里要写清这次用的是哪套协议：%q", caps.Channel)
	}

	onlyTrzsz := newFakeHost("trz", "tsz", "base64", "md5sum")
	clearCapabilities("")
	if c := probeCapabilities(onlyTrzsz, false); !c.Rz || !c.Sz {
		t.Fatalf("trzsz 模式下 trz/tsz 才是通道：%+v", c)
	}
}

// ───────────────────────── 下载落盘判定 ─────────────────────────

func TestChooseDownloaded(t *testing.T) {
	dir := t.TempDir()
	base := "app.conf"
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 1) 之前没有 → 新出现原名文件
	before := dirNameSet(dir)
	write(base, "新内容")
	got := chooseDownloaded(dir, base, before, dirNameSet(dir), -1, time.Time{})
	if got != filepath.Join(dir, base) {
		t.Fatalf("新出现的原名文件应该被选中，得到 %q", got)
	}

	// 2) 原来就有 → 本地 rz -E 改名成 name.0：必须选中 name.0
	before = dirNameSet(dir)
	write(base+".0", "第二次")
	got = chooseDownloaded(dir, base, before, dirNameSet(dir), 10, time.Now())
	if got != filepath.Join(dir, base+".0") {
		t.Fatalf("改名后的文件必须被选中（ZMODEM 保留 mtime，按 mtime 挑会挑错），得到 %q", got)
	}

	// 3) 多个改名文件 → 取序号最大的
	before = dirNameSet(dir)
	write(base+".1", "第三次")
	write(base+".2", "第四次")
	got = chooseDownloaded(dir, base, before, dirNameSet(dir), 10, time.Now())
	if got != filepath.Join(dir, base+".2") {
		t.Fatalf("应该取序号最大的那个，得到 %q", got)
	}

	// 4) 什么都没新增、原名文件也没变 → 没下来
	before = dirNameSet(dir)
	st, _ := os.Stat(filepath.Join(dir, base))
	got = chooseDownloaded(dir, base, before, dirNameSet(dir), st.Size(), st.ModTime())
	if got != "" {
		t.Fatalf("没新增也没更新时应该返回空（= 没下来），得到 %q", got)
	}

	// 5) 无关的新文件不该被误选
	before = dirNameSet(dir)
	write("别的东西.txt", "x")
	got = chooseDownloaded(dir, base, before, dirNameSet(dir), st.Size(), st.ModTime())
	if got != "" {
		t.Fatalf("同目录里别的新文件不该被当成下载结果，得到 %q", got)
	}
}

func TestIsVersionedName(t *testing.T) {
	if !isVersionedName("a.txt.0", "a.txt") || !isVersionedName("a.txt.12", "a.txt") {
		t.Fatal("name.N 应该算改名后的文件")
	}
	if isVersionedName("a.txt.bak", "a.txt") || isVersionedName("a.txt2", "a.txt") || isVersionedName("b.txt.0", "a.txt") {
		t.Fatal("不是 rz -E 那种改名的不该被认成下载结果")
	}
}

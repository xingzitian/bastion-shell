package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 共享规则这一层的测试盯三件事：
//  1. **读得懂** JSONC（中文注释、尾随逗号）—— 用户手写的文件必须能被解析；
//  2. **写回不吃注释** —— 这是「两个实现读写同一份」能不能成立的底线；
//  3. 文件坏了**绝不覆盖**，只用内置默认继续跑。

func withSharedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	return dir
}

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStripJSONCRemovesCommentsAndTrailingCommas(t *testing.T) {
	in := "{\n  // 这是注释\n  \"a\": 1, /* 块注释 */\n  \"b\": \"x, // 不是注释\",\n  \"c\": [1, 2,],\n}"
	var got map[string]any
	if err := parseJSONC(in, &got); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got["b"] != "x, // 不是注释" {
		t.Fatalf("字符串里的 // 被当成注释吃掉了：%v", got["b"])
	}
	if len(got["c"].([]any)) != 2 {
		t.Fatalf("尾随逗号没处理干净：%v", got["c"])
	}
	if got["a"].(float64) != 1 {
		t.Fatalf("a 不对：%v", got["a"])
	}
}

func TestStripJSONCHandlesEscapedQuotesInStrings(t *testing.T) {
	in := `{"a": "引号 \" 和 // 都不是注释", "b": 2}`
	var got map[string]any
	if err := parseJSONC(in, &got); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got["b"].(float64) != 2 {
		t.Fatalf("转义引号把解析带偏了：%v", got)
	}
	if !strings.Contains(got["a"].(string), "//") {
		t.Fatalf("字符串内容被改动：%v", got["a"])
	}
}

func TestFindJSONCValue(t *testing.T) {
	text := `{
  // 头注释
  "privilege": "ask",
  "profiles": {
    "生产": { "privilege": "sudo", "notes": ["a"] }
  }
}`
	if n, ok := findJSONCValue(text, "privilege"); !ok || text[n.start:n.end] != `"ask"` {
		t.Fatalf("定位顶层标量失败：%v %q", ok, text[n.start:n.end])
	}
	n, ok := findJSONCValue(text, "profiles", "生产", "privilege")
	if !ok || text[n.start:n.end] != `"sudo"` {
		t.Fatalf("定位嵌套标量失败：%v %q", ok, text[n.start:n.end])
	}
	if _, ok := findJSONCValue(text, "profiles", "不存在"); ok {
		t.Fatal("不存在的键不该被找到")
	}
}

func TestJSONCSetValueKeepsComments(t *testing.T) {
	text := habitsHeader + "\n" + `{
  // 提权方式：见上面说明
  "privilege": "ask",
  "notes": []
}`
	next, ok := jsoncSetValue(text, `"sudo"`, "privilege")
	if !ok {
		t.Fatal("设置顶层标量失败")
	}
	if !strings.Contains(next, "// 提权方式：见上面说明") {
		t.Fatalf("注释被吃掉了：\n%s", next)
	}
	if !strings.Contains(next, `"privilege": "sudo"`) {
		t.Fatalf("值没改成：\n%s", next)
	}
	var probe map[string]any
	if err := parseJSONC(next, &probe); err != nil {
		t.Fatalf("改完之后解析不过：%v\n%s", err, next)
	}
}

func TestJSONCSetValueCreatesMissingNesting(t *testing.T) {
	// 路径中间缺对象时要能补出来（例如首次给某个档案记提权方式）
	text := `{
  "privilege": "ask",
  "profiles": {}
}`
	next, ok := jsoncSetValue(text, `"sudo"`, "profiles", "生产", "privilege")
	if !ok {
		t.Fatal("补嵌套失败")
	}
	var probe struct {
		Profiles map[string]map[string]any `json:"profiles"`
	}
	if err := parseJSONC(next, &probe); err != nil {
		t.Fatalf("改完之后解析不过：%v\n%s", err, next)
	}
	if probe.Profiles["生产"]["privilege"] != "sudo" {
		t.Fatalf("嵌套值没写对：%s", next)
	}
	// profiles 整个缺了也要能补
	text2 := `{"privilege": "ask"}`
	next2, ok := jsoncSetValue(text2, `"sudo"`, "profiles", "生产", "privilege")
	if !ok {
		t.Fatal("补 profiles 失败")
	}
	if err := parseJSONC(next2, &probe); err != nil {
		t.Fatalf("改完之后解析不过：%v\n%s", err, next2)
	}
	if probe.Profiles["生产"]["privilege"] != "sudo" {
		t.Fatalf("嵌套值没写对：%s", next2)
	}
}

func TestNestedJSONCArrayItemAppends(t *testing.T) {
	text := `{"notes": ["已有"], "profiles": {}}`
	next, ok := nestedJSONCArrayItem(text, `"新增"`, "notes")
	if !ok {
		t.Fatal("追加数组项失败")
	}
	var probe struct {
		Notes []string `json:"notes"`
	}
	if err := parseJSONC(next, &probe); err != nil {
		t.Fatalf("解析失败：%v\n%s", err, next)
	}
	if len(probe.Notes) != 2 || probe.Notes[1] != "新增" {
		t.Fatalf("追加结果不对：%v", probe.Notes)
	}
	// 空数组
	next2, ok := nestedJSONCArrayItem(`{"notes": []}`, `"第一项"`, "notes")
	if !ok {
		t.Fatal("空数组追加失败")
	}
	if err := parseJSONC(next2, &probe); err != nil || len(probe.Notes) != 1 {
		t.Fatalf("空数组追加结果不对：%v / %v", err, next2)
	}
}

// ───────────────────────── 习惯 ─────────────────────────

const habitsSample = `// BastionShell 个人习惯
{
  // 全局提权方式
  "privilege": "ask",
  "workdir": "/opt/app",
  "notes": ["这台机器用 systemctl 重启服务"],
  "profiles": {
    "生产": { "privilege": "sudo", "notes": ["不要动 nginx.conf"] }
  }
}`

func TestReadHabitsKeepsCommentsAndFields(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, habitsFile, habitsSample)

	h := readHabits()
	if resolvePrivilege(h, "生产") != privSudo {
		t.Fatalf("档案级提权方式没读到：%v", resolvePrivilege(h, "生产"))
	}
	if resolvePrivilege(h, "别的档案") != privAsk {
		t.Fatalf("未记录的档案应该落到全局 ask：%v", resolvePrivilege(h, "别的档案"))
	}
	if resolveWorkdir(h, "") != "/opt/app" {
		t.Fatalf("全局工作目录没读到：%q", resolveWorkdir(h, ""))
	}
	text := habitsForAI(h, "生产")
	for _, want := range []string{"sudo", "不要动 nginx.conf", "systemctl"} {
		if !strings.Contains(text, want) {
			t.Errorf("给 AI 的习惯文本缺少 %q：\n%s", want, text)
		}
	}
}

func TestReadHabitsMissingFileIsFine(t *testing.T) {
	withSharedDir(t)
	h := readHabits()
	if resolvePrivilege(h, "") != privAsk {
		t.Fatalf("没有文件时应该是 ask：%v", resolvePrivilege(h, ""))
	}
}

func TestReadHabitsBrokenFileDoesNotPanicOrOverwrite(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, habitsFile, "{ 这不是 JSON")
	before, _ := os.ReadFile(filepath.Join(dir, habitsFile))

	h := readHabits()
	if resolvePrivilege(h, "") != privAsk {
		t.Fatalf("坏文件应该退回默认：%v", resolvePrivilege(h, ""))
	}
	after, _ := os.ReadFile(filepath.Join(dir, habitsFile))
	if string(before) != string(after) {
		t.Fatal("坏文件被改写了 —— 绝不能动用户的文件")
	}
}

func TestAppendHabitKeepsHeaderComments(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, habitsFile, habitsSample)

	ok, msg := appendHabit("", "这台机器 sudo 免密")
	if !ok {
		t.Fatalf("追加习惯失败：%s", msg)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, habitsFile))
	text := string(raw)
	if !strings.Contains(text, "// BastionShell 个人习惯") || !strings.Contains(text, "// 全局提权方式") {
		t.Fatalf("注释被吃掉了：\n%s", text)
	}
	h := readHabits()
	found := false
	for _, n := range h.Notes {
		if n == "这台机器 sudo 免密" {
			found = true
		}
	}
	if !found {
		t.Fatalf("新习惯没落盘：%v", h.Notes)
	}

	// 重复内容不重复写
	if ok, _ := appendHabit("", "这台机器 sudo 免密"); ok {
		t.Fatal("重复的习惯不该再写一遍")
	}
}

func TestAppendHabitCreatesFileWhenMissing(t *testing.T) {
	dir := withSharedDir(t)
	ok, msg := appendHabit("生产", "先 cd /opt/app")
	if !ok {
		t.Fatalf("文件不存在时应该能建出来：%s", msg)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, habitsFile))
	if !strings.Contains(string(raw), "两个版本读同一份") {
		t.Fatalf("新建文件应该带上说明头：\n%s", raw)
	}
	h := readHabits()
	if len(h.Profiles["生产"].Notes) != 1 {
		t.Fatalf("档案级习惯没写对：%+v", h.Profiles)
	}
}

func TestSetHabitsPrivilegeGlobalAndPerProfile(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, habitsFile, habitsSample)

	if ok, msg := setHabitsPrivilege("", privSudoI); !ok {
		t.Fatalf("设置全局提权失败：%s", msg)
	}
	if ok, msg := setHabitsPrivilege("新档案", privNone); !ok {
		t.Fatalf("新档案提权失败：%s", msg)
	}
	h := readHabits()
	if h.Privilege != privSudoI {
		t.Fatalf("全局提权没写成：%v", h.Privilege)
	}
	if resolvePrivilege(h, "新档案") != privNone {
		t.Fatalf("新档案提权没写成：%v", resolvePrivilege(h, "新档案"))
	}
	if resolvePrivilege(h, "生产") != privSudo {
		t.Fatalf("别的档案被改坏了：%v", resolvePrivilege(h, "生产"))
	}
	if ok, _ := setHabitsPrivilege("", privilegeMode("乱写")); ok {
		t.Fatal("非法提权方式不该被写进去")
	}
}

func TestHabitsCachePicksUpFileChanges(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, habitsFile, habitsSample)
	if resolvePrivilege(currentHabits(), "生产") != privSudo {
		t.Fatal("第一次读失败")
	}
	// 手改文件（模拟用户在编辑器里保存）→ 缓存必须失效
	mustWrite(t, dir, habitsFile, `{"privilege":"none","notes":[],"profiles":{}}`)
	if resolvePrivilege(currentHabits(), "生产") != privNone {
		t.Fatal("mtime 变了但缓存没失效 —— 用户手改文件后不该要求重启程序")
	}
}

// ───────────────────────── 高危规则 ─────────────────────────

func TestBuiltinDangerRulesHitAndMiss(t *testing.T) {
	rules := buildDangerRules(dangerOverride{})
	hit := []string{
		"rm -rf /",
		"rm -rf /*",
		"rm -rf ~",
		"sudo reboot",
		"shutdown -h now",
		"mkfs.ext4 /dev/sdb1",
		"dd if=/dev/zero of=/dev/sda bs=1M",
		"echo x > /dev/sda",
		"chmod 777 /",
		"chown root /",
		"mv /etc /tmp/etc",
		"truncate -s 0 /dev/sda",
		"shred /dev/sda",
		"killall5",
		":(){ :|: & };:",
	}
	for _, c := range hit {
		if len(findDangerous(c, rules)) == 0 {
			t.Errorf("这条应该被判高危但没判：%q", c)
		}
	}
	// 宁缺毋滥：这些是正常运维动作，**一条都不该报**
	miss := []string{
		"rm -rf /tmp/x",
		"rm -f /var/log/app.log",
		"ls -l /",
		"chmod 644 /tmp/a",
		"chown deploy /tmp/a",
		"mv /opt/app/old /opt/app/new",
		"df -h",
		"systemctl restart nginx",
		"echo hello > /tmp/x",
		"cat /etc/hosts",
	}
	for _, c := range miss {
		if hits := findDangerous(c, rules); len(hits) > 0 {
			t.Errorf("正常命令被误报成高危：%q → %v", c, hits)
		}
	}
}

func TestDangerMatchedTextHasNoBoundaryNoise(t *testing.T) {
	// 移植时把前 R 瞻换成了捕获组：命中的那段文本不能带上边界字符
	rules := buildDangerRules(dangerOverride{})
	hits := findDangerous("rm -rf / && echo done", rules)
	if len(hits) == 0 {
		t.Fatal("应该命中 rm-root")
	}
	if strings.ContainsAny(hits[0].Matched, "&;|") || strings.HasSuffix(hits[0].Matched, " ") {
		t.Fatalf("命中文本带上了边界字符：%q", hits[0].Matched)
	}
}

func TestDangerOverrideDisableAndExtra(t *testing.T) {
	// 关掉内置的一条
	no := false
	o := dangerOverride{UseBuiltin: &no}
	if len(findDangerous("rm -rf /", buildDangerRules(o))) != 0 {
		t.Fatal("useBuiltin=false 时不该还有内置规则")
	}
	o2 := dangerRulesFrom(t, `{"disabled":["reboot"]}`)
	if len(findDangerous("reboot", buildDangerRules(o2))) != 0 {
		t.Fatal("disabled 里的规则没被关掉")
	}
	if len(findDangerous("rm -rf /", buildDangerRules(o2))) == 0 {
		t.Fatal("只该关掉指定的那条，别的要留着")
	}
	// 追加自定义
	o3 := dangerRulesFrom(t, `{"extra":[{"id":"no-drop-db","why":"禁止删库","pattern":"drop\\s+database"}]}`)
	hits := findDangerous("mysql -e 'DROP DATABASE prod'", buildDangerRules(o3))
	if len(hits) == 0 || hits[0].ID != "no-drop-db" || hits[0].Why != "禁止删库" {
		t.Fatalf("自定义规则没生效：%v", hits)
	}
	// 用户写错正则：跳过它自己，不连累其它
	o4 := dangerRulesFrom(t, `{"extra":[{"pattern":"([未闭合"}]}`)
	if len(findDangerous("rm -rf /", buildDangerRules(o4))) == 0 {
		t.Fatal("坏正则不该让整张表失效")
	}
}

func dangerRulesFrom(t *testing.T, overrideJSON string) dangerOverride {
	t.Helper()
	var o dangerOverride
	if err := json.Unmarshal([]byte(overrideJSON), &o); err != nil {
		t.Fatalf("测试用的覆盖配置写错了：%v", err)
	}
	return o
}

func TestDangerRulesFileIsShared(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, dangerRulesFile, `{
  // 我们公司内部禁忌：不许删业务库
  "extra": [{"id":"no-drop-db","why":"禁止删库","pattern":"drop\\s+database"}]
}`)
	hits := checkDanger("DROP DATABASE prod")
	if len(hits) == 0 {
		t.Fatal("共享文件里的自定义规则没生效")
	}
	if !strings.Contains(dangerRefusal(hits), "禁止删库") {
		t.Fatalf("拒绝理由里应该写清为什么：%s", dangerRefusal(hits))
	}
}

// ───────────────────────── 菜单规则（共享） ─────────────────────────

func TestMenuHintsSharedFileCanDisableAndExtend(t *testing.T) {
	dir := withSharedDir(t)
	// 内置默认：真机原文能认出来
	if !detectPrompt(realHostMenu, hintHostPrompt) {
		t.Fatal("内置规则应该认得真机主菜单")
	}
	// × 关掉「进行搜索」这条内置规则，另外加一条自己家的提示语
	mustWrite(t, dir, menuHintsFile, `{
  // 我们那家堡垒机的写法不一样
  "disabled": [{"key": "hostPrompt", "pattern": "进行搜索"}],
  "extra": [{"key": "hostPrompt", "pattern": "请选择要登录的主机"}]
}`)
	if detectPrompt("进行搜索", hintHostPrompt) {
		t.Fatal("被 disabled 关掉的内置规则不该再生效")
	}
	if !detectPrompt("请选择要登录的主机", hintHostPrompt) {
		t.Fatal("extra 里的自定义规则没生效")
	}
	// 别的内置规则不受影响
	if !detectPrompt("3) 输入 p 进行显示您有权限的资产.", hintHostPrompt) {
		t.Fatal("只该关掉指定那条，别的内置规则要留着")
	}
	// ⚠️ 关掉一条不等于这一屏就认不出来了：真机主菜单那行**同时命中多条**内置规则，
	// 关掉其中一条它照样是菜单。这是设计如此（宁可多认几条也不漏），
	// 但文档里要说清楚，免得有人以为「关了没生效是 bug」。
	if !detectPrompt(realHostMenu, hintHostPrompt) {
		t.Fatal("真机主菜单还有其他内置规则命中，应该仍然是菜单")
	}
}

func TestMenuHintsUseBuiltinFalse(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, menuHintsFile, `{"useBuiltin": false, "extra": [{"key":"shellPrompt","pattern":"^>>>"}]}`)
	if detectPrompt(realShell, hintShellPrompt) {
		t.Fatal("useBuiltin=false 时不该还有内置规则")
	}
	if !detectPrompt(">>>", hintShellPrompt) {
		t.Fatal("extra 规则没生效")
	}
}

func TestMenuHintsBadRegexDoesNotBreakOthers(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, menuHintsFile, `{"extra":[{"key":"hostPrompt","pattern":"([未闭合"},{"key":"hostPrompt","pattern":"我家堡垒机"}]}`)
	if !detectPrompt("我家堡垒机", hintHostPrompt) {
		t.Fatal("一条坏正则不该让整组规则失效")
	}
	if !detectPrompt(realShell, hintShellPrompt) {
		t.Fatal("内置规则不该受影响")
	}
}

func TestSharedRulesSummary(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, habitsFile, habitsSample)
	summary := sharedRulesSummary()
	if !strings.Contains(summary, dir) {
		t.Fatalf("自检没报出共享目录：%s", summary)
	}
	if !strings.Contains(summary, habitsFile+"：有") {
		t.Fatalf("自检没认出 habits.jsonc 存在：%s", summary)
	}
	if !strings.Contains(summary, dangerRulesFile+"：没有") {
		t.Fatalf("自检没说清哪些文件没有：%s", summary)
	}
}

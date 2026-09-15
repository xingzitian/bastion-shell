package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 共享规则：**两个实现读同一份文件**。
//
// 这是 docs/two-flavors.md 里「规则抽成共享数据」那一步的落地。三类东西本质都是数据：
//
//	~/.bastionshell/habits.jsonc      个人习惯（提权方式 / 常用目录 / 口头约定）—— 两边都读写
//	~/.bastionshell/dangerRules.jsonc 高危命令规则（内置可关、可加自己的）
//	~/.bastionshell/menuHints.jsonc   堡垒机菜单识别规则（新增）
//
// ⚠️ 目录是 **`~/.bastionshell`**（扩展侧一直在这儿），**不是**桌面版自己的
// `%AppData%\bastionshell`（档案/转发/mcp.json 在那儿）。共享的东西必须放同一个地方，
// 否则「共享」就成了空话。这一条写在这里，免得以后有人「顺手统一一下目录」把共享搞坏。
//
// 读取策略：按 mtime 缓存（用户手改文件后不用重启程序），解析失败**绝不覆盖**原文件 ——
// 只用内置默认继续跑，并在日志里说清楚哪个文件坏了。

// sharedConfigDir 共享规则目录（与 VS Code 扩展一致）
func sharedConfigDir() string {
	if d := strings.TrimSpace(os.Getenv("BASTIONSHELL_SHARED_DIR")); d != "" {
		_ = os.MkdirAll(d, 0o700)
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.TempDir()
	}
	dir := filepath.Join(home, ".bastionshell")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func sharedFilePath(name string) string { return filepath.Join(sharedConfigDir(), name) }

// ───────────────────────── 个人习惯（habits.jsonc） ─────────────────────────

type privilegeMode string

const (
	privNone  privilegeMode = "none"
	privSudo  privilegeMode = "sudo"
	privSudoI privilegeMode = "sudo-i"
	privAsk   privilegeMode = "ask"
)

type profileHabit struct {
	Privilege privilegeMode `json:"privilege,omitempty"`
	Workdir   string        `json:"workdir,omitempty"`
	Notes     []string      `json:"notes,omitempty"`
}

type habits struct {
	Privilege privilegeMode           `json:"privilege,omitempty"`
	Workdir   string                  `json:"workdir,omitempty"`
	Notes     []string                `json:"notes,omitempty"`
	Profiles  map[string]profileHabit `json:"profiles,omitempty"`
}

const habitsFile = "habits.jsonc"

// habitsHeader 新建文件时写进去的中文说明（与扩展侧同一份，用户两边看到的一致）
const habitsHeader = `// ============================================================
// BastionShell 个人习惯（**两个版本读同一份**：VS Code 扩展 / 桌面版）
// ------------------------------------------------------------
// 这里记录「你自己怎么干活」，好让 AI 不用每次都问同样的问题。
// 文件可以直接手改（保存即生效）；AI 在你确认后也会往里追加，
// 追加只改目标字段，本文件里手写的中文注释不会被冲掉。
// ------------------------------------------------------------
// 字段说明：
//   privilege  提权方式，四选一：
//                "none"    这台机器不用提权，直接跑普通命令
//                "sudo"    账号 sudo 免密，直接写 sudo <命令> 即可（不用 sudo -i）
//                "sudo-i"  必须先 sudo -i 进交互式 root（要人工输密码）
//                "ask"     每次都先问你（默认）
//   workdir    常用工作目录（绝对路径，AI 会先 cd 过去）
//   notes      习惯条目，一句话一条，AI 会读它来迁就你的用法
//
//   profiles   上面三项按「档案名」分别覆盖全局设置
// ------------------------------------------------------------
// ============================================================`

func isPrivilegeMode(v string) bool {
	switch privilegeMode(v) {
	case privNone, privSudo, privSudoI, privAsk:
		return true
	}
	return false
}

// normalizeHabits 把读到的东西收敛成干净的习惯：坏字段忽略，好字段照用
func normalizeHabits(raw habits) habits {
	out := habits{Notes: []string{}, Profiles: map[string]profileHabit{}}
	if isPrivilegeMode(string(raw.Privilege)) {
		out.Privilege = raw.Privilege
	}
	if strings.TrimSpace(raw.Workdir) != "" {
		out.Workdir = strings.TrimSpace(raw.Workdir)
	}
	out.Notes = cleanStrings(raw.Notes)
	for name, p := range raw.Profiles {
		item := profileHabit{}
		if isPrivilegeMode(string(p.Privilege)) {
			item.Privilege = p.Privilege
		}
		if strings.TrimSpace(p.Workdir) != "" {
			item.Workdir = strings.TrimSpace(p.Workdir)
		}
		item.Notes = cleanStrings(p.Notes)
		out.Profiles[name] = item
	}
	return out
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, x := range in {
		if t := strings.TrimSpace(x); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// readHabits 读个人习惯（文件不存在返回空习惯，**不创建文件** ——
// 桌面版是次要实现，不该在用户还没用扩展时就往共享目录里塞文件）
func readHabits() habits {
	text, found, err := readJSONCText(sharedFilePath(habitsFile))
	if err != nil || !found {
		return normalizeHabits(habits{})
	}
	var raw habits
	if err := parseJSONC(text, &raw); err != nil {
		log.Printf("习惯文件解析失败（按默认值继续，原文件未动）：%v", err)
		return normalizeHabits(habits{})
	}
	return normalizeHabits(raw)
}

// resolvePrivilege 某个档案最终生效的提权方式（档案 > 全局 > ask）
func resolvePrivilege(h habits, profileName string) privilegeMode {
	key := strings.TrimSpace(profileName)
	if key != "" {
		if p, ok := h.Profiles[key]; ok && isPrivilegeMode(string(p.Privilege)) {
			return p.Privilege
		}
	}
	if isPrivilegeMode(string(h.Privilege)) {
		return h.Privilege
	}
	return privAsk
}

func resolveWorkdir(h habits, profileName string) string {
	key := strings.TrimSpace(profileName)
	if key != "" {
		if p, ok := h.Profiles[key]; ok && p.Workdir != "" {
			return p.Workdir
		}
	}
	return h.Workdir
}

// privilegeText 提权方式对应的可执行说明（给 AI 看的）
func privilegeText(m privilegeMode) string {
	switch m {
	case privNone:
		return "none（这台机器不需要提权，直接跑普通命令，不要加 sudo）"
	case privSudo:
		return "sudo（账号 sudo 免密，直接写 `sudo <命令>`，不要用 sudo -i）"
	case privSudoI:
		return "sudo-i（必须先 `sudo -i` 进交互式 root，且需要人工输密码）"
	default:
		return "ask（还没记录，特权命令前先问用户一次，问完立刻记下来）"
	}
}

// habitsForAI 拼成一段紧凑文本给 AI 读（和扩展侧同一套字段语义）
func habitsForAI(h habits, profileName string) string {
	key := strings.TrimSpace(profileName)
	mode := resolvePrivilege(h, key)
	lines := []string{"提权习惯：" + privilegeText(mode)}
	if wd := resolveWorkdir(h, key); wd != "" {
		lines = append(lines, "常用工作目录："+wd)
	}
	notes := append([]string{}, h.Notes...)
	if key != "" {
		notes = append(notes, h.Profiles[key].Notes...)
	}
	if len(notes) > 0 {
		parts := make([]string, 0, len(notes))
		for _, n := range notes {
			parts = append(parts, "  - "+n)
		}
		lines = append(lines, "个人习惯：\n"+strings.Join(parts, "\n"))
	}
	if mode == privAsk && len(notes) == 0 {
		lines = append(lines,
			"（这个档案还没有任何记录。按用户实际做法执行过一次后，用 bastion_habits 记下来，下次就不用再问了。）")
	}
	return strings.Join(lines, "\n")
}

// editHabits 就地改 habits.jsonc（只动目标节点，注释保留）；改完校验，坏了不写。
// 返回 false 表示没写成（调用方要把这件事说出来，不能假装成功）。
func editHabits(apply func(text string) (string, bool)) (bool, string) {
	path := sharedFilePath(habitsFile)
	text, found, err := readJSONCText(path)
	if err != nil {
		return false, fmt.Sprintf("读习惯文件失败：%v", err)
	}
	if !found {
		text = habitsHeader + "\n" + defaultHabitsJSON + "\n"
	}
	next, ok := apply(text)
	if !ok {
		return false, "没找到要改的字段（文件结构可能被手改过）"
	}
	if next == text {
		return false, ""
	}
	if !validJSONC(next) {
		return false, "改完之后解析不过，已放弃本次写入（原文件未动）"
	}
	if err := writeJSONCTextAtomic(path, next); err != nil {
		return false, fmt.Sprintf("写习惯文件失败：%v", err)
	}
	return true, ""
}

const defaultHabitsJSON = `{
  "privilege": "ask",
  "workdir": "",
  "notes": [],
  "profiles": {}
}`

// appendHabit 追加一条习惯（自动去重）。profile 为空 = 写全局。
func appendHabit(profile, note string) (bool, string) {
	note = strings.TrimSpace(note)
	if note == "" {
		return false, "习惯内容不能为空"
	}
	key := strings.TrimSpace(profile)
	h := readHabits()
	existing := h.Notes
	path := []string{"notes"}
	if key != "" {
		existing = h.Profiles[key].Notes
		path = []string{"profiles", key, "notes"}
	}
	for _, x := range existing {
		if x == note {
			return false, "这条习惯已经记过了，没有重复写入"
		}
	}
	lit, _ := json.Marshal(note)
	full := append(append([]string{}, path...), fmt.Sprintf("%d", len(existing)))
	ok, msg := editHabits(func(text string) (string, bool) {
		// 数组项路径：先追加，再兜住「数组还不存在」的情况（用 setValue 补出来）
		if next, ok := nestedJSONCArrayItem(text, string(lit), full...); ok {
			return next, true
		}
		return jsoncSetValue(text, "["+string(lit)+"]", path...)
	})
	return ok, msg
}

// setPrivilege 设置提权方式。profile 为空 = 写全局。
func setHabitsPrivilege(profile string, mode privilegeMode) (bool, string) {
	if !isPrivilegeMode(string(mode)) {
		return false, "提权方式只能是 none / sudo / sudo-i / ask"
	}
	key := strings.TrimSpace(profile)
	path := []string{"privilege"}
	if key != "" {
		path = []string{"profiles", key, "privilege"}
	}
	ok, msg := editHabits(func(text string) (string, bool) {
		return jsoncSetValue(text, `"`+string(mode)+`"`, path...)
	})
	return ok, msg
}

// ───────────────────────── 高危命令规则（dangerRules.jsonc） ─────────────────────────

type dangerOverride struct {
	UseBuiltin *bool    `json:"useBuiltin"`
	Disabled   []string `json:"disabled"`
	Extra      []struct {
		ID      string `json:"id"`
		Why     string `json:"why"`
		Pattern string `json:"pattern"`
	} `json:"extra"`
}

const dangerRulesFile = "dangerRules.jsonc"

func readDangerOverride() dangerOverride {
	text, found, err := readJSONCText(sharedFilePath(dangerRulesFile))
	if err != nil || !found {
		return dangerOverride{}
	}
	var raw dangerOverride
	if err := parseJSONC(text, &raw); err != nil {
		log.Printf("高危规则文件解析失败（按内置规则继续，原文件未动）：%v", err)
		return dangerOverride{}
	}
	return raw
}

// ───────────────────────── 菜单识别规则（menuHints.jsonc） ─────────────────────────

type menuHintsOverride struct {
	UseBuiltin *bool `json:"useBuiltin"`
	Disabled   []struct {
		Key     string `json:"key"`
		Pattern string `json:"pattern"`
	} `json:"disabled"`
	Extra []struct {
		Key     string `json:"key"`
		Pattern string `json:"pattern"`
	} `json:"extra"`
}

const menuHintsFile = "menuHints.jsonc"

func readMenuHintsOverride() menuHintsOverride {
	text, found, err := readJSONCText(sharedFilePath(menuHintsFile))
	if err != nil || !found {
		return menuHintsOverride{}
	}
	var raw menuHintsOverride
	if err := parseJSONC(text, &raw); err != nil {
		log.Printf("菜单规则文件解析失败（按内置规则继续，原文件未动）：%v", err)
		return menuHintsOverride{}
	}
	return raw
}

// menuHintsTemplate 首次生成（或提示用户创建）时用的模板。
// 语义和 dangerRules.jsonc 一致：**内置的照用，这里只做「关掉某条」和「追加自己的」** ——
// 不做「填了就整组替换」，那样以后内置规则改进了也用不上。
const menuHintsTemplate = `{
  // 是否使用内置规则（默认 true）。改成 false 就只用下面 extra 里的规则
  "useBuiltin": true,

  // 关掉误报的内置规则：key 是组名，pattern 是内置规则的**原文**（一字不差照抄）
  // 组名取值：hostPrompt / hostPromptLoose / userPrompt / assetPrompt / shellPrompt
  "disabled": [],

  // 追加自己的规则（比如你们那家堡垒机的提示语内置没覆盖）
  // 例子：{"key": "hostPrompt", "pattern": "请选择要登录的主机"}
  "extra": []
}`

// ───────────────────────── 带缓存的读取 ─────────────────────────

// rulesCache 按 mtime 缓存共享规则：用户手改文件后不用重启程序，
// 但也不是每条命令都去读盘解析一遍。
type rulesCacheEntry[T any] struct {
	modTime time.Time
	size    int64
	value   T
	loaded  bool
}

var rulesCache = struct {
	sync.Mutex
	habits rulesCacheEntry[habits]
	danger rulesCacheEntry[dangerOverride]
	menu   rulesCacheEntry[menuHintsOverride]
}{}

func cacheStamp(path string) (time.Time, int64, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0, false
	}
	return st.ModTime(), st.Size(), true
}

// currentHabits 读个人习惯（带 mtime 缓存）
func currentHabits() habits {
	path := sharedFilePath(habitsFile)
	mt, size, ok := cacheStamp(path)
	rulesCache.Lock()
	defer rulesCache.Unlock()
	if rulesCache.habits.loaded && ok && rulesCache.habits.modTime.Equal(mt) && rulesCache.habits.size == size {
		return rulesCache.habits.value
	}
	v := readHabits()
	rulesCache.habits = rulesCacheEntry[habits]{modTime: mt, size: size, value: v, loaded: true}
	return v
}

func currentDangerOverride() dangerOverride {
	path := sharedFilePath(dangerRulesFile)
	mt, size, ok := cacheStamp(path)
	rulesCache.Lock()
	defer rulesCache.Unlock()
	if rulesCache.danger.loaded && ok && rulesCache.danger.modTime.Equal(mt) && rulesCache.danger.size == size {
		return rulesCache.danger.value
	}
	v := readDangerOverride()
	rulesCache.danger = rulesCacheEntry[dangerOverride]{modTime: mt, size: size, value: v, loaded: true}
	return v
}

func currentMenuHintsOverride() menuHintsOverride {
	path := sharedFilePath(menuHintsFile)
	mt, size, ok := cacheStamp(path)
	rulesCache.Lock()
	defer rulesCache.Unlock()
	if rulesCache.menu.loaded && ok && rulesCache.menu.modTime.Equal(mt) && rulesCache.menu.size == size {
		return rulesCache.menu.value
	}
	v := readMenuHintsOverride()
	rulesCache.menu = rulesCacheEntry[menuHintsOverride]{modTime: mt, size: size, value: v, loaded: true}
	return v
}

// sharedRulesSummary 自检/排障用：这些文件现在到底读到了没有
func sharedRulesSummary() string {
	dir := sharedConfigDir()
	lines := []string{fmt.Sprintf("共享规则目录：%s", dir)}
	for _, name := range []string{habitsFile, dangerRulesFile, menuHintsFile} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil {
			lines = append(lines, fmt.Sprintf("- %s：有（%d 字节，%s 最后修改）", name, st.Size(), st.ModTime().Format("2006-01-02 15:04")))
		} else {
			lines = append(lines, fmt.Sprintf("- %s：没有（用内置默认）", name))
		}
	}
	h := currentHabits()
	lines = append(lines, "全局提权习惯："+string(resolvePrivilege(h, "")))
	return strings.Join(lines, "\n")
}

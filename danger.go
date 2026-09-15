package main

import (
	"regexp"
	"strconv"
	"strings"
)

// 高危命令识别 —— 移植自 VS Code 扩展的 src/danger.ts，逐条对齐。
//
// 两条设计原则（照抄扩展侧，别改）：
//
// 1. **不要把这条黑名单当安全边界。** 它挡不住有心人（`rm -rf $HOME` 换个写法就绕过了），
//    它挡的是手滑和 AI 幻觉。真正的边界是「人看一眼再点确认」。
// 2. **宁缺毋滥。** 误报会让人很快学会无脑点「仍然执行」，那这条防线就废了。
//    所以规则要锚定到「几乎不可能是本意」的写法上 ——
//    例如 `rm -rf /tmp/x` 不该报（正常的清理），`rm -rf /` 才报。
//
// ⚠️ 移植时的一处**必须的改写**：扩展侧那几条规则结尾用了**前瞻** `(?=\s|$|;|\|)`，
// 而 Go 用的是 RE2 引擎，**不支持前瞻**。这里把前瞻改成捕获组 `(\s|$|;|\|)`，
// 并在取「命中的那段文本」时把捕获组那一段去掉（`boundaryGroup`），
// 效果与前瞻一致。改的时候别只改正则忘了这个标记，否则原因里会多出一个空格/分号。

type dangerRule struct {
	ID  string
	Why string
	Re  *regexp.Regexp
	// boundaryGroup 结尾那个捕获组只是用来替代前瞻，命中文本要把它去掉
	boundaryGroup bool
}

type dangerHit struct {
	ID      string
	Why     string
	Matched string
}

// builtinDangerRules 内置规则表。每条都用边界收尾，避免命中正常路径。
var builtinDangerRules = []dangerRule{
	{
		ID:  "rm-root",
		Why: "删除根目录或家目录",
		// 只报「目标是根/家目录本身」：/ 、/* 、~ 、~/* 、$HOME
		// `rm -rf /tmp/x` 不会命中（`/` 后面跟的是 t，不是边界）
		Re:            regexp.MustCompile(`(?i)\brm\s+(?:-[a-zA-Z-]+\s+)*(?:/|/\*|~|~/\*|\$HOME|\$\{HOME\})(\s|$|;|\|)`),
		boundaryGroup: true,
	},
	{
		ID:  "reboot",
		Why: "重启或关机",
		Re:  regexp.MustCompile(`(?i)\b(?:reboot|shutdown|poweroff|halt|init\s+0|telinit\s+0)\b`),
	},
	{
		ID:  "mkfs",
		Why: "格式化文件系统",
		Re:  regexp.MustCompile(`(?i)\bmkfs(?:\.[a-z0-9]+)?\b|\bwipefs\b`),
	},
	{
		ID:  "dd-dev",
		Why: "向块设备直接写数据",
		Re:  regexp.MustCompile(`(?i)\bdd\b[^\n|;]*?\bof=/dev/(?:sd|nvme|hd|vd|mmcblk|disk)`),
	},
	{
		ID:  "redirect-dev",
		Why: "覆写磁盘设备",
		Re:  regexp.MustCompile(`(?i)>{1,2}\s*/dev/(?:sd|nvme|hd|vd|mmcblk)`),
	},
	{
		ID:            "chmod-root",
		Why:           "修改根目录权限",
		Re:            regexp.MustCompile(`(?i)\bchmod\s+(?:-[a-zA-Z-]+\s+)*(?:[0-7]{3,4}|[ugoa]*[+-][rwxXst]+)\s+/(\s|$|;)`),
		boundaryGroup: true,
	},
	{
		ID:            "chown-root",
		Why:           "修改根目录属主",
		Re:            regexp.MustCompile(`(?i)\bchown\s+(?:-[a-zA-Z-]+\s+)*\S+\s+/(\s|$|;)`),
		boundaryGroup: true,
	},
	{
		ID:  "fork-bomb",
		Why: "fork 炸弹（瞬间耗尽进程数）",
		Re:  regexp.MustCompile(`:\s*\(\s*\)\s*\{[^}]*\}\s*;?\s*:`),
	},
	{
		ID:            "mv-sysdir",
		Why:           "移动系统关键目录",
		Re:            regexp.MustCompile(`(?i)\bmv\s+(?:-[a-zA-Z-]+\s+)*/(?:etc|boot|usr|var|lib|lib64|bin|sbin|opt)(\s|$|;)`),
		boundaryGroup: true,
	},
	{
		ID:  "truncate-dev",
		Why: "截断设备文件",
		Re:  regexp.MustCompile(`(?i)\btruncate\s+[^\n|;]*/dev/`),
	},
	{
		ID:  "shred-dev",
		Why: "擦除设备数据",
		Re:  regexp.MustCompile(`(?i)\bshred\b[^\n|;]*/dev/`),
	},
	{
		ID:  "kill-all",
		Why: "终止所有进程",
		Re:  regexp.MustCompile(`(?i)\bkillall5\b`),
	},
}

// buildDangerRules 算出最终生效的规则表（内置 + 共享文件里的关掉/追加）。
// 纯函数，坏正则只跳过它自己，不让整张表失效。
func buildDangerRules(o dangerOverride) []dangerRule {
	useBuiltin := o.UseBuiltin == nil || *o.UseBuiltin
	disabled := map[string]bool{}
	for _, id := range o.Disabled {
		disabled[strings.TrimSpace(id)] = true
	}
	rules := make([]dangerRule, 0, len(builtinDangerRules)+len(o.Extra))
	if useBuiltin {
		for _, r := range builtinDangerRules {
			if disabled[r.ID] {
				continue
			}
			rules = append(rules, r)
		}
	}
	for i, e := range o.Extra {
		pattern := strings.TrimSpace(e.Pattern)
		if pattern == "" {
			continue
		}
		// 统一不区分大小写，省得每人都记得写 flag（和扩展侧一样）
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			continue // 用户写错正则是常事，跳过这一条即可
		}
		id := strings.TrimSpace(e.ID)
		if id == "" {
			id = "custom-" + strconv.Itoa(i+1)
		}
		why := strings.TrimSpace(e.Why)
		if why == "" {
			why = "自定义高危规则"
		}
		rules = append(rules, dangerRule{ID: id, Why: why, Re: re})
	}
	return rules
}

// findDangerous 找出命令里命中的高危规则（可能多条）
func findDangerous(command string, rules []dangerRule) []dangerHit {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	var hits []dangerHit
	for _, r := range rules {
		loc := r.Re.FindStringSubmatchIndex(command)
		if loc == nil {
			continue
		}
		end := loc[1]
		if r.boundaryGroup && loc[2] >= 0 {
			end = loc[2] // 去掉替代前瞻的那个捕获组
		}
		hits = append(hits, dangerHit{ID: r.ID, Why: r.Why, Matched: strings.TrimSpace(command[loc[0]:end])})
	}
	return hits
}

// checkDanger 用当前共享规则检查一条命令（exec / 部署注入都走它）
func checkDanger(command string) []dangerHit {
	return findDangerous(command, buildDangerRules(currentDangerOverride()))
}

// describeDanger 拼成人能看的原因列表
func describeDanger(hits []dangerHit) string {
	if len(hits) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var lines []string
	for _, h := range hits {
		key := h.ID + "|" + h.Matched
		if seen[key] {
			continue
		}
		seen[key] = true
		lines = append(lines, "· "+h.Why+" —— 命中 `"+h.Matched+"`")
	}
	return strings.Join(lines, "\n")
}

// dangerRefusal 命中高危规则时回给 AI 的话。
//
// 刻意**不给**「换个写法绕过」的暗示：这条防线的价值在于让 AI 停下来找人，
// 而不是让它想办法把命令发出去。
func dangerRefusal(hits []dangerHit) string {
	return "⛔ 命令被高危规则拦下，**没有发出去**：\n" + describeDanger(hits) +
		"\n\n这条规则挡的是手滑和误判，不是安全边界。如果你确实要做这件事，" +
		"请把「要做什么、为什么」告诉用户，由用户在终端窗口里自己执行 —— 不要换写法绕过它。\n" +
		"（规则可改：`" + sharedFilePath(dangerRulesFile) + "` 里能关掉内置项、也能加自己那套。）"
}

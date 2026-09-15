package main

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// 堡垒机菜单导航的「提示文本」识别。
//
// 这一层同样是从 VS Code 扩展（src/menu.ts）搬过来的：内置的每一条正则都是
// **实测过的真实菜单原文**，不是猜的。认不出来就退回「未知」（照旧执行），
// 认错才会出事 —— 所以规则刻意写窄。
//
// 桌面版目前只读内置默认，不读配置文件；扩展侧那套可配置的 menuHints 设置
// 属于「规则抽成共享 JSON」那一步（见 docs/two-flavors.md 第六节）。

type menuHintKey string

const (
	hintHostPrompt      menuHintKey = "hostPrompt"
	hintHostPromptLoose menuHintKey = "hostPromptLoose"
	hintUserPrompt      menuHintKey = "userPrompt"
	hintAssetPrompt     menuHintKey = "assetPrompt"
	hintShellPrompt     menuHintKey = "shellPrompt"
)

// menuHintSources 内置默认。写法刻意「具体」：
// 宁可认不出来退回固定等待，也不要靠模糊模式抢跑 —— 抢跑比慢更糟。
var menuHintSources = map[menuHintKey][]string{
	hintHostPrompt: {
		// 某厂商堡垒机实测（原文见扩展侧 test/menu.test.ts）：
		//   「1) 输入 部分IP，主机名，备注 进行搜索登录(如果唯一).」
		`进行搜索`,
		// 注意「输入」和「IP/主机名」之间可能夹着东西，别把距离卡太死
		`(?:请输入|请选择|输入|选择)[^\n]{0,40}(地址|IP|主机名|主机|资产|设备|节点|编号|数据库|kubernetes)`,
		`(?:搜索|查找)[^\n]{0,16}(资产|主机|设备)`,
		`(?:资产|主机|设备|节点)[^\n]{0,10}[:：>]\s*$`,
		// 英文界面：这台堡垒机支持中/英/日切换，所以同一套菜单可能是英文
		`(?:please\s+)?(?:input|enter|select|search)[^\n]{0,30}(?:host|ip|asset|node|device|database)`,
	},
	hintHostPromptLoose: {
		// 纯提示符。某厂商的是 `Opt>`，**而且会重复打印成 `Opt> Opt>`** ——
		// 所以不要锚定行尾（第一版写成 ^\s*opt>\s*$ 就栽在这，永远匹配不上）。
		`^\s*opt>`,
		// 新加坡那台是 `[Host]>`
		`^\s*\[host\]>`,
	},
	hintUserPrompt: {
		// 某厂商堡垒机实测：二级菜单是一张账号表 + 提示 + `ID>` 提示符
		//   「提示：输入资产[test-node-10.0.0.10(10.0.0.10)]的账号ID」
		// 方括号里是资产名，长度不定 → 间隔放宽到 80
		`输入[^\n]{0,80}(账号|账户|用户)\s*ID`,
		`^\s*id>`,
		// 账号表表头（很具体，不会误伤）
		`名称[^\n]{0,20}用户名`,
		`(?:请)?(?:选择|输入)[^\n]{0,20}(用户|账号|账户|登录用户)`,
		`(?:登录)?(?:用户|账号|账户)[^\n]{0,10}[:：>]\s*$`,
		`(?:select|choose|login)[^\n]{0,20}(?:user|account)`,
		`(?:user|account|username)[^\n]{0,20}[:：>]\s*$`,
	},
	hintShellPrompt: {
		`[\w.\-]{1,32}@[\w.\-]{1,64}[:~][^\n]{0,60}[$#]\s*$`,
		`\][$#]\s*$`,
		// bash-4.2$ 这类「不以 user@host 开头」的提示符：要求 $/# 前面紧挨着的是
		// 字母数字或 )/]，这样 `#######` 这种装饰性分隔行不会被误判成提示符
		`(?:^|\n)[^\n#]{0,40}[A-Za-z0-9)\]][$#]\s*$`,
		`(?:^|\n)\s*[#$]\s*$`,
	},
	hintAssetPrompt: {
		// ⚠️ 这一组必须写得**窄**：主菜单里也常出现「请输入资产名称/资产编号」，
		// 一旦被当成资产列表，主菜单那一步就会走错分支。所以只认带 ID 的写法。
		// 实测原文：提示：输入资产ID直接登录，二级搜索使用 // + 字段，如：//192
		`资产\s*ID\s*直接登录`,
		`(?:输入|请选择|选择)[^\n]{0,8}资产\s*ID`,
		// 资产表页脚（账号表没有这个）
		`总数量\s*[:：]\s*\d+`,
		`(?:input|enter|select)[^\n]{0,12}asset\s*id`,
	},
}

// 编译一次就够：这些正则在会话生命周期里被反复用到。
// 但**内置默认可以被共享文件覆盖**（`~/.bastionshell/menuHints.jsonc`，
// 见 shared_rules.go），所以按文件 mtime 缓存编译结果 —— 用户手改规则后
// 不用重启程序，也不是每条命令都重新编译 30 条正则。
var menuReCache = struct {
	sync.Mutex
	modTime time.Time
	size    int64
	loaded  bool
	value   map[menuHintKey][]*regexp.Regexp
}{}

// currentMenuHintRegexes 当前生效的菜单识别规则（内置 ± 共享文件）
func currentMenuHintRegexes() map[menuHintKey][]*regexp.Regexp {
	path := sharedFilePath(menuHintsFile)
	mt, size, hasFile := cacheStamp(path)
	menuReCache.Lock()
	defer menuReCache.Unlock()
	if menuReCache.loaded && hasFile && menuReCache.modTime.Equal(mt) && menuReCache.size == size {
		return menuReCache.value
	}
	menuReCache.value = compileMenuHints(readMenuHintsOverride())
	menuReCache.modTime, menuReCache.size, menuReCache.loaded = mt, size, true
	return menuReCache.value
}

// compileMenuHints 内置 + 共享文件算出生效规则。
//
// 共享文件的语义和 dangerRules.jsonc 一致：**内置照用，只能「关掉某条」和「追加」** ——
// 不做「填了就整组替换内置」，那样以后内置规则改进了这边也用不上。
// 关掉内置项要同时写对 key 和 pattern（pattern 一字不差照抄内置原文），避免误关。
func compileMenuHints(o menuHintsOverride) map[menuHintKey][]*regexp.Regexp {
	useBuiltin := o.UseBuiltin == nil || *o.UseBuiltin
	disabled := map[string]bool{}
	for _, d := range o.Disabled {
		disabled[strings.TrimSpace(d.Key)+"\x00"+strings.TrimSpace(d.Pattern)] = true
	}
	out := map[menuHintKey][]*regexp.Regexp{}
	add := func(key menuHintKey, pattern string) {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return
		}
		// 对应扩展侧的 new RegExp(src, 'im')
		re, err := regexp.Compile("(?im)" + pattern)
		if err != nil {
			// 用户写错正则是常事，跳过这一条即可，不要连累其它
			return
		}
		out[key] = append(out[key], re)
	}
	if useBuiltin {
		for key, srcs := range menuHintSources {
			for _, src := range srcs {
				if disabled[string(key)+"\x00"+src] {
					continue
				}
				add(key, src)
			}
		}
	}
	for _, e := range o.Extra {
		key := menuHintKey(strings.TrimSpace(e.Key))
		if _, known := menuHintSources[key]; !known {
			continue
		}
		add(key, e.Pattern)
	}
	return out
}

// detectPrompt 一段输出里是否出现了某一类提示
func detectPrompt(text string, key menuHintKey) bool {
	plain := stripAnsi(text)
	for _, re := range currentMenuHintRegexes()[key] {
		if re.MatchString(plain) {
			return true
		}
	}
	return false
}

// ---- 资产列表（一个 IP 搜出多条） ----

// assetCandidate 资产表里的一行
type assetCandidate struct {
	ID       string
	Name     string
	Address  string
	Platform string
	Org      string
	Note     string
}

var (
	reAssetIDCol = regexp.MustCompile(`(?i)^(id|编号|序号|资产\s*id|asset\s*id)$`)
	// 账号表的标志列：有它就不是资产表（两张表都有「ID + 名称」，靠这一列区分）
	reAccountCol = regexp.MustCompile(`(?i)^(用户名|账号|账户|用户|user|username|account)$`)
	reAssetCols  = []struct {
		key string
		re  *regexp.Regexp
	}{
		{"name", regexp.MustCompile(`(?i)^(名称|名字|资产名|name)$`)},
		{"address", regexp.MustCompile(`(?i)^(地址|ip|ip\s*地址|主机|主机名|address|host)$`)},
		{"platform", regexp.MustCompile(`(?i)^(平台|系统|操作系统|类型|platform|os|type)$`)},
		{"org", regexp.MustCompile(`(?i)^(组织|部门|分组|org|dept)$`)},
		{"note", regexp.MustCompile(`(?i)^(备注|说明|描述|note|comment)$`)},
	}
	reCellSplit  = regexp.MustCompile(`\s{2,}`)
	reNumericRow = regexp.MustCompile(`^\d+$`)
)

// splitCells 把一行表格拆成单元格。
// 优先按 `|` 拆（实测那家堡垒机就是这么画的）；没有竖线时按「2 个以上空格」拆。
func splitCells(line string) []string {
	if strings.Contains(line, "|") {
		raw := strings.Split(line, "|")
		out := make([]string, 0, len(raw))
		for _, c := range raw {
			out = append(out, strings.TrimSpace(c))
		}
		// `| a | b |` 这种首尾竖线会多出空单元格，去掉才不会整体错位
		if len(out) > 1 && out[0] == "" {
			out = out[1:]
		}
		if len(out) > 1 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		return out
	}
	parts := reCellSplit.Split(strings.TrimSpace(line), -1)
	out := make([]string, 0, len(parts))
	for _, c := range parts {
		out = append(out, strings.TrimSpace(c))
	}
	return out
}

// parseAssetTable 从一屏文字里解析资产表。
//
// 形状（实测原文）：
//
//	ID | 名称                      | 地址          | 平台    | 组织     | 备注
//	-----+---------------------------+---------------+---------+----------+------
//	  1  | 172.20.30.143             | 172.20.30.143 | Linux   | 默认组织 |
//	  2  | 研发网域172.20.30.143     | 172.20.30.143 | Gateway | 默认组织 |
//	页码：1，每页行数：23，总页数：1，总数量：2
//	提示：输入资产ID直接登录，二级搜索使用 // + 字段，如：//192 上一页：b 下一页：n
//
// **靠表结构判定，不靠提示语**：必须同时有 ID 列、名称列，以及「地址/平台」列，
// 且**不能有用户名/账号列** —— 后者是账号表的标志。这样同一台机器上
// 「选资产」和「选账号」两张表不会被认成同一件事。
func parseAssetTable(screen string) []assetCandidate {
	plain := stripAnsi(screen)
	lines := strings.Split(plain, "\n")

	headerIdx := -1
	idIdx := -1
	colIdx := map[string]int{}

	for i, line := range lines {
		cells := splitCells(line)
		if len(cells) < 3 {
			continue
		}
		idAt := -1
		for j, c := range cells {
			if reAssetIDCol.MatchString(c) {
				idAt = j
				break
			}
		}
		if idAt < 0 {
			continue
		}
		hasName, hasAssetish, hasAccount := false, false, false
		for _, c := range cells {
			if reAccountCol.MatchString(c) {
				hasAccount = true
			}
			for _, col := range reAssetCols {
				if !col.re.MatchString(c) {
					continue
				}
				if col.key == "name" {
					hasName = true
				} else {
					hasAssetish = true
				}
			}
		}
		if !hasName || !hasAssetish || hasAccount {
			continue
		}

		headerIdx = i
		idIdx = idAt
		for _, col := range reAssetCols {
			for j, c := range cells {
				if col.re.MatchString(c) {
					colIdx[col.key] = j
					break
				}
			}
		}
		break
	}
	if headerIdx < 0 {
		return nil
	}

	rows := make([]assetCandidate, 0, 8)
	for i := headerIdx + 1; i < len(lines); i++ {
		cells := splitCells(lines[i])
		id := ""
		if idIdx >= 0 && idIdx < len(cells) {
			id = strings.TrimSpace(cells[idIdx])
		}
		if !reNumericRow.MatchString(id) {
			// 分隔线（-----+-----）、页脚、空行都跳过；一旦已经开始收行，遇到非数据行就收工，
			// 免得把后面别的表（比如账号表）的行也吃进来
			if len(rows) > 0 {
				break
			}
			continue
		}
		get := func(key string) string {
			at, ok := colIdx[key]
			if !ok || at >= len(cells) {
				return ""
			}
			return strings.TrimSpace(cells[at])
		}
		rows = append(rows, assetCandidate{
			ID:       id,
			Name:     get("name"),
			Address:  get("address"),
			Platform: get("platform"),
			Org:      get("org"),
			Note:     get("note"),
		})
	}
	return rows
}

// looksLikeAssetList 这一屏是不是「要你选资产」。
// 两个信号取或：① 提示语命中；② 解析出了资产表。
// 两个都要，是因为两边都可能缺：有的机型提示语不一样，有的屏被截断只剩表格。
func looksLikeAssetList(screen string) bool {
	if detectPrompt(screen, hintAssetPrompt) {
		return true
	}
	return len(parseAssetTable(screen)) > 0
}

// ---- 会话状态 ----

// screenState 会话现在处于什么状态（给 AI 判断「能不能直接执行命令」用）
type screenState string

const (
	stateShell   screenState = "shell"
	stateMenu    screenState = "menu"
	stateUnknown screenState = "unknown"
)

// classifySessionScreen 从屏幕最后几行的原文判断会话状态。
//
// 「这条会话已经落到 shell」在文本上差别很大、在后果上差别更大 ——
// 往菜单里发命令，菜单会把命令当成它的输入吃掉（或者误触某个选项）。
//
// 判定上**偏保守**：只有在「明确认出是菜单」且「明确不是 shell」时才报 menu；
// 认不出来一律 unknown（照旧执行），免得自定 PS1 的正常会话被误拦。
func classifySessionScreen(tail string) screenState {
	if strings.TrimSpace(tail) == "" {
		return stateUnknown
	}
	// 只看**最后几行**：菜单提示一定在最底下，翻滚缓冲里那些旧内容不算
	lines := nonEmptyLines(tail)
	bottom := strings.Join(lastN(lines, 6), "\n")
	if detectPrompt(bottom, hintShellPrompt) {
		return stateShell
	}
	menuLike := detectPrompt(bottom, hintAssetPrompt) ||
		detectPrompt(bottom, hintUserPrompt) ||
		detectPrompt(bottom, hintHostPrompt) ||
		looksLikeAssetList(bottom) ||
		// 弱特征（裸提示符 `Opt>` / `[Host]>`）**只认最后两行**。
		//
		// 为什么必须带上它（2026-09-14 真机教训）：堡垒机菜单正文（那句「进行搜索」）
		// 会随公告一起滚上去，屏幕最底下往往只剩 `Opt> Opt>` —— 只认强特征的话就判成
		// 「未知」，于是 exec 的拦截不生效，**AI 的命令真的被敲进了菜单里**。
		// 只认最后两行 + 这两个很窄的模式（`^\s*opt>` / `^\s*\[host\]>`），
		// 所以不会把自定 PS1 的正常 shell 误判成菜单。
		detectPrompt(strings.Join(lastN(lines, 2), "\n"), hintHostPromptLoose)
	if menuLike {
		return stateMenu
	}
	return stateUnknown
}

// handoffHint 会话是人和 AI 共用的同一条 —— 这句话要跟着工具结果一起给模型看
const handoffHint = "（会话是人和 AI **共用**的同一条：用户也可以在终端里自己操作 —— 选资产、选账号、输密码、输动态码都行；" +
	"做完告诉 AI 一声，它会用 bastion_listSessions 看会话、再用 bastion_exec 接着干，不需要重新认证。）"

// menuStuckMessage 「这条会话停在菜单上」时给 AI 的话（不要发命令，先说清怎么办）
func menuStuckMessage(screenTail string) string {
	lines := []string{
		"⚠️ 这条会话现在停在**堡垒机菜单**上（还没落到目标机的 shell），所以命令没有发出去 ——",
		"发出去只会被菜单当成菜单输入吃掉，甚至误触某个选项。",
		"请让用户在终端里手动走完这一步（选资产 / 选账号 / 输密码 / 输动态码都可以），完成后你再用",
		"bastion_listSessions（会标出每条会话的状态）+ bastion_exec 在**同一条会话**上接着干。",
		handoffHint,
	}
	tail := strings.Join(lastN(nonEmptyLines(screenTail), 8), "\n")
	if tail != "" {
		lines = append(lines, "—— 屏幕最后几行 ——\n"+tail)
	}
	return strings.Join(lines, "\n")
}

// sessionStateLabel 会话状态 → 给人/AI 看的一句话
func sessionStateLabel(state screenState) string {
	switch state {
	case stateShell:
		return "✅ 在 shell 里（可以直接执行命令）"
	case stateMenu:
		return "⚠️ 还停在堡垒机菜单上（需要人在终端里走完这一步）"
	default:
		return "状态未知（可以直接试）"
	}
}

// screenTailLines 屏幕最后几行（菜单识别的输入）
const screenTailLines = 12

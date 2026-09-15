package main

import (
	"errors"
	"fmt"
	"strings"
)

// MCP 工具层：把会话能力暴露成 MCP 工具。
//
// 这一层**不碰 ssh、不碰登记表** —— 能力是注入进来的（`mcpSessionAPI`）。
// 这么设计有和扩展侧一样的两个硬理由：
//
//  1. **能测**：可以在普通 Go 测试里起真实的 HTTP 端点、发真实的 JSON-RPC，
//     不需要开窗口、不需要真会话；
//  2. **不会跑偏**：工具名的语义和 VS Code 扩展（bastion-vscode/src/mcpTools.ts）
//     保持一字不差 —— 文档、提示词、用户心智都只有一套名字。
//
// ⚠️ 桌面版目前实现 8 个工具（见 mcpToolDefs）。`bastion_connect` 依赖
// 「菜单自动导航」，还没有 —— **不注册**它，免得模型看到 tools/list 里有、
// 调了却拿到一句"没实现"。

// mcpSessionAPI 工具层需要的能力
type mcpSessionAPI interface {
	ListSessions() (string, error)
	Health() (string, error)
	ListProfiles() (string, error)
	Tail(terminal string, lines int) (string, error)
	Exec(command, terminal string) (string, error)
	Habits(action, profile, habit, privilege string) (string, error)
	Push(localPath, remoteDir, terminal string) (string, error)
	Pull(remotePath, localDir, terminal string) (string, error)
}

// mcpToolDef 一个 MCP 工具的元数据（`tools/list` 直接返回它）
type mcpToolDef struct {
	Name  string
	Title string
	// Description 给模型看的说明：什么时候用、要传什么。这段文字决定了模型会不会用对
	Description string
	// InputSchema JSON Schema（入参）
	InputSchema map[string]any
	// ReadOnly 只读工具：MCP 客户端据此**不弹确认框**（`readOnlyHint`）。
	// 有副作用的工具绝不能标成只读。
	ReadOnly bool
}

// mcpToolResult 一次工具调用的结果
type mcpToolResult struct {
	Text    string
	IsError bool
}

// errUnknownTool 调用了不存在的工具（HTTP 层会翻译成 JSON-RPC -32602）
var errUnknownTool = errors.New("未知的工具")

const mcpSessionsDesc = "列出当前所有 BastionShell 堡垒机会话，并标出每条会话现在能不能直接执行命令" +
	"（✅ 在 shell 里 / ⚠️ 还停在堡垒机菜单上）。" +
	"要在这台机器上执行命令前先看一眼：多会话时要用终端名指定目标，避免把命令发到错的机器上。"

const mcpProfilesDesc = "列出连接档案（档案名、账号@主机、端口）。" +
	"第一次操作某台机器、或用户只说了「生产那台」时，先用它确认档案名。"

const mcpTailDesc = "读某条会话**屏幕上最后几行**（默认 40 行，最多 200）。用途：\n" +
	"- 命令看起来没反应、输出不完整、或者你不确定远端现在是什么状态时，**看一眼屏幕**再决定下一步；\n" +
	"- 用户在终端里手动操作完（过了菜单、输了密码、选了目标机）之后，用它确认走到了哪一步；\n" +
	"- 它返回的是屏幕上真正渲染出来的文字（去掉颜色码），比你手里的输出拼接更接近现状。"

const mcpExecDesc = "在一个**已经认证好**的 BastionShell 会话上执行 shell 命令，并返回命令输出。\n" +
	"这是操作远端服务器的主要手段。注意：\n" +
	"- 命令和输出会**实时显示在用户的终端窗口里**（用户全程看得见，也能随时接手敲键盘）；\n" +
	"- 会话是复用的，不需要也不应该尝试输入密码/动态码 —— MFA 由用户自己在终端里完成；\n" +
	"- **会话是人和 AI 共用的**：需要人工操作时（MFA、选资产、选账号、输密码、过菜单），" +
	"让用户在终端里做完，然后你直接在**同一条会话**上继续执行命令，不需要重新连接；\n" +
	"- 如果会话还停在菜单上，这条工具**不会把命令发出去**（发出去会被菜单吃掉），而是告诉你去让用户先走完那一步；\n" +
	"- 交互式命令（`sudo -i`、`su`、`top`、`vi`）拿不到可靠的结束标记，输出会不完整，尽量不要用。\n" +
	"- 返回结果末尾会带**退出码**（`[退出码 0：命令成功]`）；没等到结束标记时会明确写出来，" +
	"那种情况下输出不完整，别当成正常结果。"

const mcpHabitsDesc = "读写「个人习惯」：这个用户在目标机上怎么干活（提权方式 none/sudo/sudo-i/ask、常用目录、口头约定）。" +
	"action=read 看当前习惯；action=remember 记一条（habit 参数）；" +
	"action=setPrivilege 改提权习惯（privilege 参数）。\n" +
	"习惯文件（habits.jsonc）**VS Code 扩展和桌面版读的是同一份**，所以在这里记的东西两边都认。\n" +
	"⚠️ **什么时候才该写**（写进去会长期影响以后所有会话，所以宁可多问一次）：\n" +
	"- 用户**明确说了**做法（例如「这台机器 sudo 免密」）→ 立刻记；\n" +
	"- 只是你自己**第一次**观察到某个做法 → **先别写**；等同一个做法再遇到一次（累计 2~3 次观察）再记；\n" +
	"- 记录与实际**不符**（习惯写的是免密 sudo，实际弹了密码）→ **立刻更正**，并顺手 remember 记下真实做法；\n" +
	"- 只是猜的、或只见过一次 → **不写**。错误记忆比没有记忆更糟。"

const mcpPushDesc = "把一个**本机文件或目录**传到远端。通道**自动选**，并把用了哪条通道说清楚：\n" +
	"- 目标机装了 lrzsz → 走 `rz`（和用户在终端里敲 rz 是同一套实现）；\n" +
	"- 没装 → **自动降级 base64 分块**（不用装任何东西，慢但通用；超过 4MB 会直接拒绝并说明原因）；\n" +
	"- **目录** → 先在本机 `tar -czf` 打包 → 传 → 远端 `tar -xzf` 解开 → 删掉临时包。\n" +
	"`remoteDir` 会先 `cd` 过去（进不去会明确报错，不会偷偷传到别处）；不传就传到会话当前目录。\n" +
	"远端已有同名文件时按当前覆盖方式处理（默认 **skip：跳过并告诉你**，不会覆盖）。\n" +
	"结果里会带**传完回读远端的证据**（哈希/大小/路径，以及文件能不能读）—— " +
	"这条就是证据，**不要再用 ls / md5sum 自己验一遍**。"

const mcpPullDesc = "把一个**远端文件**拉回本机（`sz`；目标机没装 lrzsz 就自动降级成远端 base64 + 本地解码）。\n" +
	"拉回来的文件默认落在本机的下载目录（`~/Downloads/BastionShell`），这样你（AI）可以直接用自己的文件工具读它；" +
	"想放别处就传 `localDir`。"

const mcpHealthDesc = "BastionShell MCP 端点自检：返回端点地址/端口、程序版本、工具个数、每条会话的状态、" +
	"共享规则文件的读取情况。" +
	"**当其它 bastion_* 工具调用失败时先调它**：\n" +
	"- 它能正常返回 → 说明端点、鉴权、协议都是好的，刚才那次多半是偶发 → **直接重试原来那次调用**；\n" +
	"- 它自己调用也失败 → 端点没在跑 → 明确告诉用户：重启 BastionShell（关掉窗口再打开）。\n" +
	"不要因为一次工具调用失败就让用户手工去敲命令 —— 先重试、再自检，然后把准确的原因告诉用户。"

var mcpToolDefs = []mcpToolDef{
	{
		Name:        "bastion_listSessions",
		Title:       "列出堡垒机会话",
		Description: mcpSessionsDesc,
		ReadOnly:    true,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	},
	{
		Name:        "bastion_health",
		Title:       "端点自检",
		Description: mcpHealthDesc,
		ReadOnly:    true,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	},
	{
		Name:        "bastion_tail",
		Title:       "读会话屏幕",
		Description: mcpTailDesc,
		ReadOnly:    true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"terminal": map[string]any{"type": "string", "description": "会话终端名（如 deploy@10.0.0.10）；不填则用当前活动/最近会话"},
				"lines":    map[string]any{"type": "number", "description": "要读多少行（默认 40，最多 200）"},
			},
			"additionalProperties": false,
		},
	},
	{
		Name:        "bastion_listProfiles",
		Title:       "列出连接档案",
		Description: mcpProfilesDesc,
		ReadOnly:    true,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	},
	{
		Name:        "bastion_exec",
		Title:       "在远端执行命令",
		Description: mcpExecDesc,
		ReadOnly:    false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "要在远端执行的 shell 命令（单条命令；需要多条时用 &&）"},
				"terminal": map[string]any{
					"type":        "string",
					"description": "目标会话的终端名（如 deploy@10.0.0.10）。不填则用当前活动/最近使用的会话。多会话时先调 bastion_listSessions",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "bastion_push",
		Title:       "上传文件到远端",
		Description: mcpPushDesc,
		ReadOnly:    false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"localPath": map[string]any{"type": "string", "description": "本机文件或目录路径（建议绝对路径）"},
				"remoteDir": map[string]any{"type": "string", "description": "远端目标目录（不填 = 会话当前目录）；会先 cd 过去再传"},
				"terminal":  map[string]any{"type": "string", "description": "会话终端名；不填则用当前活动/最近会话"},
			},
			"required":             []string{"localPath"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "bastion_pull",
		Title:       "从远端下载文件",
		Description: mcpPullDesc,
		ReadOnly:    false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"remotePath": map[string]any{"type": "string", "description": "远端文件路径"},
				"localDir":   map[string]any{"type": "string", "description": "本机落盘目录；默认本机下载目录（~/Downloads/BastionShell）"},
				"terminal":   map[string]any{"type": "string", "description": "会话终端名；不填则用当前活动/最近会话"},
			},
			"required":             []string{"remotePath"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "bastion_habits",
		Title:       "个人习惯（提权方式等）",
		Description: mcpHabitsDesc,
		ReadOnly:    false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"read", "remember", "setPrivilege"}, "description": "要做的动作"},
				"profile":   map[string]any{"type": "string", "description": "档案名；不填＝全局习惯"},
				"habit":     map[string]any{"type": "string", "description": "action=remember 时要记下来的那句话"},
				"privilege": map[string]any{"type": "string", "enum": []string{"none", "sudo", "sudo-i", "ask"}, "description": "action=setPrivilege 时的提权方式"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	},
}

// toMcpTool 内部工具定义 → MCP `tools/list` 里的一条
func toMcpTool(def mcpToolDef) map[string]any {
	return map[string]any{
		"name":        def.Name,
		"title":       def.Title,
		"description": def.Description,
		"inputSchema": def.InputSchema,
		"annotations": map[string]any{"title": def.Title, "readOnlyHint": def.ReadOnly},
	}
}

// mcpToolList tools/list 的返回体（测试与「查看工具」共用）
func mcpToolList() []map[string]any {
	out := make([]map[string]any, 0, len(mcpToolDefs))
	for _, d := range mcpToolDefs {
		out = append(out, toMcpTool(d))
	}
	return out
}

// mcpDispatch 工具名 → 结果。
//
// 分发本身很薄，但它把所有工具的**参数缺省与错误处理**收在一处：
// 参数缺失、能力抛异常，都变成给模型看得懂的文本，而不是让整个调用炸掉。
func mcpDispatch(api mcpSessionAPI, name string, args map[string]any) (mcpToolResult, error) {
	str := func(key string) string {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	call := func(text string, err error) (mcpToolResult, error) {
		if err != nil {
			return mcpToolResult{Text: "工具执行失败：" + err.Error(), IsError: true}, nil
		}
		return mcpToolResult{Text: text}, nil
	}
	switch name {
	case "bastion_listSessions":
		return call(api.ListSessions())
	case "bastion_health":
		return call(api.Health())
	case "bastion_listProfiles":
		return call(api.ListProfiles())
	case "bastion_tail":
		lines := 0
		if v, ok := args["lines"]; ok {
			switch n := v.(type) {
			case float64:
				lines = int(n)
			case int:
				lines = n
			}
		}
		return call(api.Tail(str("terminal"), lines))
	case "bastion_exec":
		command := str("command")
		if len(command) == 0 || strings.TrimSpace(command) == "" {
			return mcpToolResult{Text: "错误：bastion_exec 需要 command 参数（要执行的命令）", IsError: true}, nil
		}
		return call(api.Exec(command, str("terminal")))
	case "bastion_push":
		localPath := str("localPath")
		if strings.TrimSpace(localPath) == "" {
			return mcpToolResult{Text: "错误：bastion_push 需要 localPath（本机文件路径）", IsError: true}, nil
		}
		return call(api.Push(localPath, str("remoteDir"), str("terminal")))
	case "bastion_pull":
		remotePath := str("remotePath")
		if strings.TrimSpace(remotePath) == "" {
			return mcpToolResult{Text: "错误：bastion_pull 需要 remotePath（远端文件路径）", IsError: true}, nil
		}
		return call(api.Pull(remotePath, str("localDir"), str("terminal")))
	case "bastion_habits":
		action := str("action")
		if strings.TrimSpace(action) == "" {
			action = "read"
		}
		return call(api.Habits(action, str("profile"), str("habit"), str("privilege")))
	default:
		return mcpToolResult{}, fmt.Errorf("%w：%s", errUnknownTool, name)
	}
}

// mcpInstructions `initialize` 返回里带的一段「使用说明」。模型看得到它，
// 所以把最容易出错的三件事写在这里：先看会话、MFA 是人的事、菜单上别发命令。
const mcpInstructions = "BastionShell 提供的是**用户已经手动认证过**的堡垒机会话（密码 + MFA + 选目标机都由人完成）。\n" +
	"典型流程：bastion_listSessions 看有哪些会话 → bastion_tail 看一眼屏幕 → bastion_exec 执行命令。\n" +
	"不要尝试输入密码、动态码，也不要试图重新登录：MFA 只能由用户在终端里手动完成，需要时请让用户操作。\n" +
	"**会话是人和 AI 共用的同一条**：用户随时可以自己敲（输动态码、选资产、过菜单、改一条命令都行），" +
	"做完之后你直接在同一条会话上接着干 —— 不需要重新认证，也不需要重新连接。" +
	"所以遇到「需要人来一下」的情况（要选资产、要选账号、要输密码、屏幕认不出来），" +
	"正确做法是**把情况说清楚并让用户操作**，而不是猜一个数字发进去、也不是断言目标机不存在。\n" +
	"**这里是桌面版（Wails 原生窗口），目前提供 8 个工具**：bastion_listSessions / bastion_health / " +
	"bastion_tail / bastion_listProfiles / bastion_exec / bastion_habits / bastion_push / bastion_pull。\n" +
	"bastion_connect（AI 自己连一台新机器）**只有 VS Code 扩展版有** —— " +
	"不要假设它存在；调用前用 tools/list 确认，需要它时请告诉用户在扩展版里做。\n" +
	"**连新目标机只能由人来做**：在 BastionShell 里连接堡垒机、在终端里输入目标机 IP 过菜单 —— " +
	"桌面版还没有菜单自动导航，AI 不能代替这一步。\n" +
	"**高危命令会被拒**：命中规则（删根、格式化、写块设备等）的命令**不会发出去**，" +
	"这时请把结论告诉用户、由他自己执行，不要换写法绕过。\n" +
	"**个人习惯**（habits.jsonc）是 VS Code 扩展和桌面版**共读同一份**：用户明确说了做法就记，" +
	"只是第一次观察到就先别写，记录与实际不符就立刻更正。错误记忆比没有记忆更糟。\n" +
	"**工具调用失败时按这个顺序处理，不要直接放弃**：\n" +
	"1. 先**原样重试一次** —— 程序刚启动或刚重连时会出现一次瞬时失败；\n" +
	"2. 还失败就调 **bastion_health**：它能返回 → 端点是好的，回到第 1 步重试或换个工具；\n" +
	"3. 连 bastion_health 都调不动 → 端点没在跑，请**明确告诉用户**重启 BastionShell，然后再试；\n" +
	"4. 无论哪种情况，都**不要**因为一次失败就转去让用户手工执行命令 —— 那会把「一个可修的小问题」变成「用户自己干活」。"

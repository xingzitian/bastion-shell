package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// 会话能力的实现层：MCP 工具层（mcp_tools.go）注入的就是它。
//
// 对应 VS Code 扩展的 aiSessionApi.ts —— 把「找会话 / 拦菜单 / 执行 / 拼提示」
// 收在一处，工具层只负责参数与文案。这么分的好处：工具层可以在测试里换成假实现
// （不需要真 SSH），而这里的行为能单独对着真机会话测。

type desktopSessionAPI struct{}

// ListSessions bastion_listSessions
func (desktopSessionAPI) ListSessions() (string, error) {
	sessions := listSessions()
	labels := sessionLabels()
	rows := make([]string, 0, len(sessions))
	for _, s := range sessions {
		line := fmt.Sprintf("- %s：%s", labels[s.id], sessionStateLabel(s.screenState()))
		// 目标机的提示符：同一个堡垒机上开了多条时，名字都一样，
		// 只有屏幕上那行提示符认得出来「这条到底在哪台机器上」
		if p := s.lastPromptLine(); p != "" {
			line += fmt.Sprintf("（屏幕最后一行：%s）", p)
		}
		rows = append(rows, line)
	}
	note := endpointNote()
	if len(rows) == 0 {
		return "没有活动堡垒机会话" + note +
			"\n\n请让用户在 BastionShell 窗口里连接一台机器，并在终端里走到目标机的 shell" +
			"（桌面版还没有菜单智能识别，这一步只能人工走），之后 AI 就能在**同一条会话**上执行命令。", nil
	}
	return fmt.Sprintf("当前堡垒机会话（%d 个）：\n%s\n\n", len(rows), strings.Join(rows, "\n")) +
		"提示：这些都是**人和 AI 共用**的会话 —— 需要人工操作（MFA、选目标机、输密码、过菜单）时，" +
		"请让用户在窗口里做完，然后你直接在这些会话上继续执行命令，不需要重新连接或认证。" +
		note, nil
}

// Health bastion_health：端点自检。
//
// 它的价值不在信息量，而在**能把「端点坏了」和「这次偶发」分开** ——
// AI 拿到一次工具失败时最容易做的就是放弃、或者转头让用户手工干活。
func (desktopSessionAPI) Health() (string, error) {
	url, port, fallback, ok := mcpEndpointInfo()
	sessions := listSessions()
	var b strings.Builder
	b.WriteString("BastionShell MCP 端点自检（**桌面版**）：\n")
	if ok {
		fmt.Fprintf(&b, "- 端点：%s\n", url)
		fmt.Fprintf(&b, "- 端口：%d\n", port)
		if fallback {
			b.WriteString("- ⚠️ 默认端口被占用，已退到随机端口（本机可能还开着另一个 BastionShell 或 VS Code 扩展）\n")
		}
	} else {
		b.WriteString("- 端点：**没有在跑**（这正好解释了刚才那次工具调用失败）\n")
	}
	fmt.Fprintf(&b, "- 程序版本：%s\n", version)
	fmt.Fprintf(&b, "- 工具个数：%d（桌面版只有这几个：%s）\n", len(mcpToolDefs), toolNameList())
	b.WriteString(sharedRulesSummary() + "\n")
	if len(sessions) == 0 {
		b.WriteString("- 会话：0 条（请让用户在 BastionShell 里连一台机器）\n")
	} else {
		labels := sessionLabels()
		fmt.Fprintf(&b, "- 会话：%d 条\n", len(sessions))
		for _, s := range sessions {
			fmt.Fprintf(&b, "  - %s：%s\n", labels[s.id], sessionStateLabel(s.screenState()))
		}
	}
	b.WriteString("\n结论：端点、鉴权、协议都是好的 —— 刚才那次工具调用失败多半是偶发，**直接重试原来那次调用**；" +
		"如果连这条自检都调不到，请告诉用户重启 BastionShell（关掉窗口再打开）。")
	return b.String(), nil
}

// ListProfiles bastion_listProfiles
func (desktopSessionAPI) ListProfiles() (string, error) {
	store := newProfileStore()
	store.load()
	profiles := store.snapshot()
	if len(profiles) == 0 {
		return "没有连接档案，请先在 BastionShell 窗口里创建（侧边栏「连接档案」）", nil
	}
	rows := make([]string, 0, len(profiles))
	for _, p := range profiles {
		port := p.Port
		if port == 0 {
			port = 22
		}
		rows = append(rows, fmt.Sprintf("- %s（%s@%s:%d）", p.Name, p.Username, p.Host, port))
	}
	return fmt.Sprintf("可用的连接档案（%d 个）：\n%s\n\n注意：档案里是**堡垒机**（或直连主机）的地址。"+
		"桌面版的档案没有「直连/堡垒机」标记，也没有记录提权习惯；"+
		"登录目标机之后要靠 bastion_tail 看提示符确认到底落在哪台机器上。",
		len(rows), strings.Join(rows, "\n")), nil
}

// Tail bastion_tail：某条会话屏幕上最后几行
func (desktopSessionAPI) Tail(terminal string, lines int) (string, error) {
	s, errMsg := lookupSession(terminal)
	if errMsg != "" {
		return errMsg, nil
	}
	n := lines
	if n <= 0 {
		n = 40
	}
	if n > 200 {
		n = 200
	}
	state := s.screenState()
	tail := strings.TrimSpace(s.screenTail(n))
	labels := sessionLabels()
	head := fmt.Sprintf("会话 %s（%s）屏幕最后 %d 行：", labels[s.id], sessionStateLabel(state), n)
	if tail == "" {
		return head + "\n（屏幕是空的）", nil
	}
	return head + "\n" + tail, nil
}

// Exec bastion_exec：在会话上执行命令。
//
// 顺序很重要，一步都不能省：
//  1. 找会话（找不到就明说，别猜一条）
//  2. **菜单拦截**：停在菜单上的会话发命令会被菜单吃掉，甚至误触选项
//  3. **高危命令拦截**：命中规则就**不发**，把原因说清楚（规则来自共享文件）
//  4. 执行 + 退出码备注（没有它，AI 只能靠读输出猜命令成没成）
//  5. sudo 密码提示单独说清楚（这是唯一需要人接手的常见情况）
func (desktopSessionAPI) Exec(command, terminal string) (string, error) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return "错误：bastion_exec 需要 command 参数（要执行的命令）", nil
	}
	s, errMsg := lookupSession(terminal)
	if errMsg != "" {
		return errMsg, nil
	}
	if guard := menuGuard(s); guard != "" {
		return guard, nil
	}
	if hits := checkDanger(cmd); len(hits) > 0 {
		return dangerRefusal(hits), nil
	}

	labels := sessionLabels()
	// 提权习惯决定一件事：能不能给 `sudo <命令>` 加哨兵。
	// 免密才敢加 —— 否则一旦弹密码提示，命令就卡在那儿只能等静止兜底。
	profileName := profileNameForSession(s)
	privilege := resolvePrivilege(currentHabits(), profileName)

	res, err := s.exec(cmd, execOpts{AllowPlainSudo: privilege == privSudo})
	if err != nil {
		return fmt.Sprintf("执行失败（会话 %s）：%v", labels[s.id], err), nil
	}
	out := res.Output + exitCodeNote(res)
	if looksLikePasswordPrompt(cmd, res.Output) {
		out += "\n\n[⚠️ 需要密码] 这条命令正在等用户输入密码。请让用户到 BastionShell 窗口里手动输入完成认证" +
			"（会话是同一条，不需要重新连接）；确认认证完成后，再用 bastion_exec 继续执行后续命令。"
		if privilege == privSudo {
			out += fmt.Sprintf("\n[习惯与实际不符] 习惯文件里「%s」记的是 sudo（免密），但实际弹了密码提示。"+
				"等用户输完密码完成认证后，请用 bastion_habits 把该档案的 privilege 改成 sudo-i，"+
				"并用 remember 记下这台机器真实的提权做法。", orGlobal(profileName))
		} else if privilege == privAsk {
			out += fmt.Sprintf("\n[还没记录习惯]「%s」没有提权习惯记录。等用户这次认证完，"+
				"请用 bastion_habits 的 setPrivilege / remember 把结论记下来，以后就不用再问了。", orGlobal(profileName))
		}
	}
	return out, nil
}

// Push bastion_push：把本机文件（或目录）传到远端。
//
// 走的是**传输标准工具**（transfer.go）：能力探测 → 选通道（rz / base64 降级 / 目录 tar 打包）
// → 传完回读校验。这里只做三件事：本地路径先查、找会话、把结果翻成人话。
func (desktopSessionAPI) Push(localPath, remoteDir, terminal string) (string, error) {
	localPath = strings.TrimSpace(localPath)
	if localPath == "" {
		return "错误：bastion_push 需要 localPath（本机文件或目录路径，建议绝对路径）", nil
	}
	// 本地路径先查：这类错误和会话无关，先报出来更好定位（目录是允许的，会打包再传）
	if _, err := os.Stat(localPath); err != nil {
		return "错误：本机找不到这个路径：" + localPath, nil
	}
	s, errMsg := lookupSession(terminal)
	if errMsg != "" {
		return errMsg, nil
	}
	if guard := menuGuard(s); guard != "" {
		return guard, nil
	}
	out := PushPath(newSessionTransferHost(s, sessionLabels()[s.id]), localPath, remoteDir, "")
	return out.Message, nil
}

// Pull bastion_pull：把远端文件拉回本机。
//
// 默认落在**程序的下载目录**（`~/Downloads/BastionShell`）—— 和界面上点下载的落点一致，
// 用户和 AI 找同一个地方。
func (desktopSessionAPI) Pull(remotePath, localDir, terminal string) (string, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return "错误：bastion_pull 需要 remotePath（远端文件路径）", nil
	}
	s, errMsg := lookupSession(terminal)
	if errMsg != "" {
		return errMsg, nil
	}
	if guard := menuGuard(s); guard != "" {
		return guard, nil
	}
	dir := strings.TrimSpace(localDir)
	if dir == "" {
		dir = defaultDownloadDir()
	}
	out := PullPath(newSessionTransferHost(s, sessionLabels()[s.id]), remotePath, dir, "")
	return out.Message, nil
}

// Habits bastion_habits：读写「个人习惯」。
//
// 习惯文件是**两个实现读同一份**的（`~/.bastionshell/habits.jsonc`），
// 所以这里写进去的东西 VS Code 扩展那边也认；写入走「只改目标节点」的方式，
// 用户手写的中文注释不会被冲掉。
func (desktopSessionAPI) Habits(action, profile, habit, privilege string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "read":
		key := strings.TrimSpace(profile)
		return fmt.Sprintf("当前个人习惯%s：\n%s\n\n（文件：%s —— VS Code 扩展与桌面版读同一份；"+
			"直接手改这个文件也行，保存即生效。）", scopeLabel(key), habitsForAI(currentHabits(), key),
			sharedFilePath(habitsFile)), nil

	case "remember":
		note := strings.TrimSpace(habit)
		if note == "" {
			return "错误：action=remember 需要 habit 参数（要记下来的那句话）", nil
		}
		ok, msg := appendHabit(profile, note)
		if !ok {
			if msg == "" {
				msg = "内容与已有记录重复"
			}
			return "没记下来：" + msg, nil
		}
		return fmt.Sprintf("已记录习惯%s：%s", scopeLabel(profile), note), nil

	case "setprivilege":
		mode := privilegeMode(strings.ToLower(strings.TrimSpace(privilege)))
		if !isPrivilegeMode(string(mode)) {
			return "错误：action=setPrivilege 需要 privilege 参数，取值 none / sudo / sudo-i / ask", nil
		}
		ok, msg := setHabitsPrivilege(profile, mode)
		if !ok {
			if msg == "" {
				msg = "没有发生变化"
			}
			return "没写成：" + msg, nil
		}
		return fmt.Sprintf("已把%s的提权习惯设为 %s —— %s", scopeLabel(profile), mode, privilegeText(mode)), nil

	default:
		return "错误：action 只能是 read / remember / setPrivilege", nil
	}
}

// profileNameForSession 把会话映射到「档案名」。
//
// 桌面版的会话只知道自己连的是 用户@主机，而习惯、`bastion_listProfiles`
// 里的提权习惯都是**按档案名**索引的 —— 所以拿 主机+账号 去档案表里对一次。
// 对不上就返回空串（= 用全局习惯），不要瞎猜一个档案名。
func profileNameForSession(s *bastionSession) string {
	store := newProfileStore()
	store.load()
	for _, p := range store.snapshot() {
		if p.Host != s.host {
			continue
		}
		if p.Username != "" && s.user != "" && p.Username != s.user {
			continue
		}
		return p.Name
	}
	return ""
}

func scopeLabel(profile string) string {
	if strings.TrimSpace(profile) == "" {
		return "（全局）"
	}
	return fmt.Sprintf("（档案 %s）", strings.TrimSpace(profile))
}

func orGlobal(profile string) string {
	if strings.TrimSpace(profile) == "" {
		return "全局"
	}
	return strings.TrimSpace(profile)
}

// toolNameList 「工具个数」那句里的名单
func toolNameList() string {
	names := make([]string, 0, len(mcpToolDefs))
	for _, d := range mcpToolDefs {
		names = append(names, d.Name)
	}
	return strings.Join(names, " / ")
}

// endpointNote 把端点写进返回里：AI 报「工具出错」时，这一行能立刻分清是
// 「端点没连上（根本没返回）」还是「连上了但工具里出错（返回里会有这行）」
func endpointNote() string {
	url, _, fallback, ok := mcpEndpointInfo()
	if !ok {
		return ""
	}
	note := fmt.Sprintf("\n（本次调用来自 MCP 端点 %s", url)
	if fallback {
		note += "（注意：默认端口被占用，已退到随机端口）"
	}
	return note + "）"
}

// rePasswordPrompt 密码提示：只扫**最后一个非空行**（提示符就在那）。
// 关键词写宽一点 —— 有些机器的 sudo 提示是本地化文案或者 `Passphrase:`。
var rePasswordPrompt = regexp.MustCompile(`(?i)(password|passphrase|密码|口令)\s*[:：]?\s*$`)

// looksLikePasswordPrompt 判断一条 sudo/su 命令是否停在密码交互（等待用户输密码）。
func looksLikePasswordPrompt(command, output string) bool {
	if !reSudoSu.MatchString(strings.TrimSpace(command)) {
		return false
	}
	plain := stripAnsi(output)
	if len(plain) > 400 {
		plain = plain[len(plain)-400:]
	}
	plain = tailText(plain, 4)
	if strings.Contains(strings.ToLower(plain), "[sudo]") {
		return true
	}
	lines := nonEmptyLines(plain)
	if len(lines) == 0 {
		return false
	}
	return rePasswordPrompt.MatchString(lines[len(lines)-1])
}

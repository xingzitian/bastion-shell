package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 共享连接档案的**端到端**验证。
//
// 单元测试（profile_test.go）验的是存储本身；这里验的是"它在真实路径上真的接通了吗"：
//   - 桌面版的 AI 出口（MCP 的 bastion_listProfiles）看得到共享档案；
//   - 一条**真会话**能对上共享档案里的名字，进而用到按档案名记的个人习惯（提权方式）——
//     也就是 profiles.jsonc 与 habits.jsonc 这两个共享文件真的串起来了。

// sharedProfileFor 写一条指向靶机的共享档案（名字随便取，用来验"档案名 → 会话 → 习惯"这条链）
func sharedProfileFor(t *testing.T, name, host string, port int, user string) {
	t.Helper()
	store := newProfileStore()
	store.load()
	if err := store.upsert(Profile{
		Name: name, Host: host, Port: port, Username: user,
		AuthMethod: "password", Mode: "bastion",
	}); err != nil {
		t.Fatalf("写入共享档案失败：%v", err)
	}
}

// TestE2EProfileListedThroughMcpEndpoint 共享档案要能从 MCP 端点被 AI 看到（真 HTTP + 真 JSON-RPC）
func TestE2EProfileListedThroughMcpEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	t.Setenv("BASTIONSHELL_LEGACY_PROFILES", "")
	sharedProfileFor(t, "共享档案-给AI看的", "10.9.9.9", 2222, "deploy")

	handle, err := startMcpHTTP(newMcpServer(desktopSessionAPI{}), newMcpToken(), "127.0.0.1", 0, nil)
	if err != nil {
		t.Fatalf("起 MCP 端点失败：%v", err)
	}
	defer handle.Close()

	status, body := mcpPost(t, handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bastion_listProfiles","arguments":{}}}`)
	if status != 200 {
		t.Fatalf("状态码 %d：%v", status, body)
	}
	text := mcpText(body)
	if !strings.Contains(text, "共享档案-给AI看的") {
		t.Fatalf("MCP 的 listProfiles 应该能看到共享档案：%q", text)
	}
	if !strings.Contains(text, "deploy@10.9.9.9:2222") {
		t.Fatalf("档案的账号@主机:端口 应该一并返回：%q", text)
	}
	// 共享文件是唯一真源：删掉之后 MCP 侧也要立刻看不到
	store := newProfileStore()
	store.load()
	if err := store.remove("共享档案-给AI看的"); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	_, body = mcpPost(t, handle,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bastion_listProfiles","arguments":{}}}`)
	if strings.Contains(mcpText(body), "共享档案-给AI看的") {
		t.Fatalf("删掉之后不该还列出来：%q", mcpText(body))
	}
}

// TestE2EProfileDrivesHabitsOnRealSession 真会话 + 共享档案 + 共享习惯三者串起来
func TestE2EProfileDrivesHabitsOnRealSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONSHELL_SHARED_DIR", dir)
	t.Setenv("BASTIONSHELL_LEGACY_PROFILES", "")

	s, _ := e2eSession(t) // 需要靶机；没有就整组跳过
	host, port := s.host, s.port
	if host == "" {
		t.Fatalf("会话没记下主机名：%+v", s)
	}

	// ① 共享档案里写一条**指向这条会话**的档案（主机/账号要和会话对得上）
	const profileName = "靶机-共享档案"
	sharedProfileFor(t, profileName, host, port, s.user)

	// ② 会话应该能对到这条档案（桌面版的习惯是按档案名索引的）
	if got := profileNameForSession(s); got != profileName {
		t.Fatalf("会话没对上共享档案里的名字（档案名→习惯这条链断了）：得到 %q，想要 %q", got, profileName)
	}

	// ③ 给这个档案记一条提权习惯，验证 exec 的行为真的跟着变
	if _, err := (desktopSessionAPI{}).Habits("setPrivilege", profileName, "", "sudo"); err != nil {
		t.Fatalf("记习惯失败：%v", err)
	}
	priv := resolvePrivilege(currentHabits(), profileName)
	if priv != privSudo {
		t.Fatalf("按档案名读提权习惯失败：得到 %q", priv)
	}
	// 免密 sudo 才敢给 `sudo <命令>` 加哨兵 —— 这是习惯真正影响行为的地方
	if !isMarkerSafe("sudo systemctl restart nginx", priv == privSudo) {
		t.Fatal("记成免密 sudo 之后，sudo 命令应该能用哨兵判定")
	}
	// 两个共享文件都在同一个共享目录里（这是"两个客户端读同一份"的前提）
	for _, name := range []string{profileSharedFile, habitsFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("共享文件 %s 不在共享目录里：%v", name, err)
		}
	}

	// ④ 真在这条会话上跑一条命令，确认档案/习惯这套没把会话弄坏
	out, err := (desktopSessionAPI{}).Exec("echo profile-habit-ok", s.name())
	if err != nil || !strings.Contains(out, "profile-habit-ok") {
		t.Fatalf("exec 出问题：%v / %q", err, out)
	}
}

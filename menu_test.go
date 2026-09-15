package main

import (
	"strings"
	"testing"
)

// 下面这些屏幕原文**原样复制自 VS Code 扩展的 src/test/menu.test.ts**
// （用户真机贴回来的，IP / 组织名已脱敏，结构一字未改）。
// 桌面版重写一遍识别逻辑时，必须让同一批原文得到同样的结论。

// realHostMenu 某厂商堡垒机主菜单（含公告和重复的 Opt> 提示符）
const realHostMenu = "\t\t张三,  堡垒机-示例生产堡垒机\n" +
	"\t1) 输入 部分IP，主机名，备注 进行搜索登录(如果唯一).\n" +
	"\t2) 输入 / + IP，主机名，备注 进行搜索，如：/192.168.\n" +
	"\t3) 输入 p 进行显示您有权限的资产.\n" +
	"\t4) 输入 g 进行显示您有权限的节点.\n" +
	"\t5) 输入 h 进行显示您有权限的主机.\n" +
	"\t6) 输入 d 进行显示您有权限的数据库.\n" +
	"\t7) 输入 k 进行显示您有权限的Kubernetes.\n" +
	"\t8) 输入 r 进行刷新最新的机器和节点信息.\n" +
	"\t11) 输入 q 进行退出.\n" +
	"公告：示例生产堡垒机使用注意事项\n" +
	"1、堡垒机域名已更新，请使用新地址登录(bastion.example.com)。\n" +
	"Opt> Opt>"

// realUserMenu 输完 IP 后的**选用户菜单**（账号表 + 提示 + ID> 提示符）
const realUserMenu = "Opt> Opt> 10.0.0.10\n" +
	"  ID    | 名称                                    | 用户名\n" +
	"--------+-----------------------------------------+----------\n" +
	"  1     | deploy                                  | deploy\n" +
	"  2     | appuser                                 | appuser\n" +
	"提示：输入资产[test-node-10.0.0.10(10.0.0.10)]的账号ID\n" +
	"返回：B/b\n" +
	"ID>"

// realAssetList 【回归】一屏资产表（一个 IP 匹配到多条资产）
const realAssetList = "ID | 名称                      | 地址          | 平台     | 组织     | 备注\n" +
	"-----+---------------------------+---------------+----------+----------+------\n" +
	"  1  | 172.20.30.143             | 172.20.30.143 | Linux    | 默认组织 |\n" +
	"  2  | 研发网域172.20.30.143     | 172.20.30.143 | Gateway  | 默认组织 |\n" +
	"页码：1，每页行数：23，总页数：1，总数量：2\n" +
	"提示：输入资产ID直接登录，二级搜索使用 // + 字段，如：//192 上一页：b 下一页：n\n" +
	"搜索：172.20.30.143"

const realShell = "Last login: Mon Sep 14 10:00:00 2026 from 10.0.0.1\n" +
	"[deploy@test-node-10.0.0.10 ~]$ ls\n" +
	"a.txt  b.txt\n" +
	"[deploy@test-node-10.0.0.10 ~]$"

func TestDetectPromptOnRealScreens(t *testing.T) {
	if !detectPrompt(realHostMenu, hintHostPrompt) {
		t.Fatal("主菜单正文应该命中强特征 hostPrompt")
	}
	if !detectPrompt(realHostMenu, hintHostPromptLoose) {
		t.Fatal("`Opt> Opt>` 应该命中弱特征 hostPromptLoose")
	}
	// ⚠️ 这条是弱特征存在的理由：输完 IP 的回显 `Opt> Opt> 10.0.0.10`
	// 若参与「又回到输 IP」的判断，堡垒机只要重画一次提示符就会被误判成「IP 被拒绝」。
	echo := "Opt> Opt> 10.0.0.10"
	if detectPrompt(echo, hintHostPrompt) {
		t.Fatal("强特征不该被回显骗到")
	}
	if !detectPrompt(realUserMenu, hintUserPrompt) {
		t.Fatal("选用户菜单应该命中 userPrompt")
	}
	if !detectPrompt(realAssetList, hintAssetPrompt) {
		t.Fatal("资产表应该命中 assetPrompt")
	}
	if !detectPrompt(realShell, hintShellPrompt) {
		t.Fatal("目标机提示符应该命中 shellPrompt")
	}
}

func TestParseAssetTableFromRealScreen(t *testing.T) {
	rows := parseAssetTable(realAssetList)
	if len(rows) != 2 {
		t.Fatalf("应该解析出 2 条资产，得到 %d 条：%+v", len(rows), rows)
	}
	if rows[0].ID != "1" || rows[0].Platform != "Linux" {
		t.Fatalf("第 1 条解析错了：%+v", rows[0])
	}
	if rows[1].ID != "2" || !strings.Contains(rows[1].Name, "172.20.30.143") || rows[1].Platform != "Gateway" {
		t.Fatalf("第 2 条解析错了：%+v", rows[1])
	}
	if rows[1].Org != "默认组织" {
		t.Fatalf("组织列没对上：%+v", rows[1])
	}
}

func TestParseAssetTableRejectsAccountTable(t *testing.T) {
	// 账号表也有「ID + 名称」，必须靠**有用户名/账号列**把它排除掉 ——
	// 认错的代价是把用户序号当资产 ID 发出去（登到错的机器上）。
	if rows := parseAssetTable(realUserMenu); len(rows) != 0 {
		t.Fatalf("账号表不该被当成资产表，得到 %+v", rows)
	}
}

func TestLooksLikeAssetList(t *testing.T) {
	if !looksLikeAssetList(realAssetList) {
		t.Fatal("资产表应该判成资产列表")
	}
	if looksLikeAssetList(realShell) {
		t.Fatal("普通 shell 屏幕不该判成资产列表")
	}
}

func TestClassifySessionScreen(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want screenState
	}{
		{"主菜单（含公告，末行只剩 Opt> Opt>）", realHostMenu, stateMenu},
		{"选用户菜单", realUserMenu, stateMenu},
		{"资产表", realAssetList, stateMenu},
		{"已落到 shell", realShell, stateShell},
		{"空屏", "", stateUnknown},
		{"认不出来就照旧执行", "starting ssh session...", stateUnknown},
	}
	for _, c := range cases {
		if got := classifySessionScreen(c.tail); got != c.want {
			t.Errorf("%s：classifySessionScreen = %q，想要 %q", c.name, got, c.want)
		}
	}
}

func TestClassifyUsesOnlyBottomOfScreen(t *testing.T) {
	// 翻滚缓冲里还留着旧菜单，但屏幕上早就回到 shell 了 ——
	// 只看最后几行，才不会被旧内容误判成「还在菜单上」。
	tail := realHostMenu + "\n" + realShell
	if got := classifySessionScreen(tail); got != stateShell {
		t.Fatalf("旧菜单不该压过当前屏幕，得到 %q", got)
	}
}

func TestMenuStuckMessageTellsHumanToTakeOver(t *testing.T) {
	msg := menuStuckMessage(realHostMenu)
	for _, want := range []string{"停在", "没有发出去", "bastion_listSessions", "屏幕最后几行"} {
		if !strings.Contains(msg, want) {
			t.Errorf("菜单拦截文案缺少 %q：\n%s", want, msg)
		}
	}
}

func TestSessionStateLabel(t *testing.T) {
	if !strings.Contains(sessionStateLabel(stateShell), "✅") {
		t.Fatal("shell 状态应带 ✅")
	}
	if !strings.Contains(sessionStateLabel(stateMenu), "⚠️") {
		t.Fatal("menu 状态应带 ⚠️")
	}
	if !strings.Contains(sessionStateLabel(stateUnknown), "未知") {
		t.Fatal("unknown 状态应说明是未知")
	}
}

package main

// 真机验证：真的 spawn 一个 CLI 宿主并拿到网页终端地址。
//
// 默认跳过（要连真服务器、要 Node）。手动跑：
//
//	BASTIONSHELL_CLI_MANUAL=1 \
//	BASTIONSHELL_CLI_JS="/mnt/c/.../bastion-vscode/out/cli/main.js" \
//	go test -run TestCliShellManual -v .
//
// 它验证的是 B 阶段最关键的一条链路：**桌面能起 CLI 并读到 /term 地址**。

import (
	"os"
	"strings"
	"testing"
)

func TestCliShellManual(t *testing.T) {
	if os.Getenv("BASTIONSHELL_CLI_MANUAL") != "1" {
		t.Skip("默认跳过（需要 Node + 真档案）；设 BASTIONSHELL_CLI_MANUAL=1 才跑")
	}
	profile := os.Getenv("BASTIONSHELL_CLI_PROFILE")
	if profile == "" {
		profile = "xzt"
	}
	stopCliShell()
	defer stopCliShell()

	url, err := startCliShell([]string{profile})
	if err != nil {
		t.Fatalf("起 CLI 薄壳失败：%v", err)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(url, "/term?token=") {
		t.Fatalf("返回的地址不像网页终端：%s", url)
	}
	t.Logf("网页终端：%s", url)
}

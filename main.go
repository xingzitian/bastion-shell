package main

import (
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// initLog 把后端日志写到用户配置目录下的 bastionshell.log（GUI 程序 stderr 会丢）。
func initLog() {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "bastionshell")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "bastionshell.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}
}

func main() {
	initLog()
	// 启动壳后端（只剩 DPAPI 存密码、文件对话框、拉起 core 这类能力，供前端与 core 调用）；
	// 统一核心：18090 让给「核心界面后端」（core），Go 退到 18091 只处理还没迁走的接口。
	go func() {
		if err := serveBackend(goBackendPort()); err != nil {
			log.Printf("Go 后端服务异常: %v", err)
		}
	}()
	// 统一核心：MCP 出口（默认 39311 + Bearer token）现在由 core 的界面后端提供，
	// 端点文件 mcp.json 仍由这里的 startUiBackend 写（子进程写 %APPDATA% 可能被拒）。
	// 界面后端（18090）同样由 core 提供（隐藏拉起、界面一字不改）。
	//
	// 起不来就**如实报错**，不再退回 Go 自己那套：Go 的 /ws 与终端能力在 ⑤ 里已经删了，
	// 「退回」只会让界面连上一个没有 /ws 的后端 —— 看起来像界面坏了，其实是退路早就没了。
	if err := startUiBackend(18090, goBackendPort(), 39311, mcpJsonPath()); err != nil {
		log.Printf("核心界面后端起不来（界面将无法连接；壳后端 %d 仍在）：%v", goBackendPort(), err)
	}

	// headless：只跑后端 + MCP，不开窗口。
	// 用途：① 没有桌面环境时验证 HTTP/薄壳路由；② 将来桌面壳崩溃时的排障入口。
	// 开关：BASTIONSHELL_HEADLESS=1
	if os.Getenv("BASTIONSHELL_HEADLESS") == "1" {
		log.Printf("headless 模式：核心后端 18090 + Go 后端 %d + MCP，不开窗口（Ctrl-C 退出）", goBackendPort())
		select {}
	}
	app := NewApp()
	// 退出时把 CLI 子进程收掉（别留孤儿 node）：⚡ 那个终端 + 核心界面后端各一个
	defer stopCliShell()
	defer stopCoreShell()
	err := wails.Run(&options.App{
		Title:            "BastionShell",
		Width:            1200,
		Height:           800,
		MinWidth:         900,
		MinHeight:        600,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Printf("启动失败: %v", err)
	}
}

// goBackendPort Go 后端的端口（默认 18091）：18090 让给核心界面后端。
func goBackendPort() int {
	if v := strings.TrimSpace(os.Getenv("BASTIONSHELL_GO_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 18091
}
// mcpJsonPath 桌面版的 MCP 端点文件（沿用原路径，客户端配置不用改）。
func mcpJsonPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "bastionshell", "mcp.json")
}
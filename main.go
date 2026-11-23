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
	// Windows 上解压内嵌 lrzsz，启用 zmodem 文件传输
	initLrzsz()
	// 启动后端（SSH / 复用 / 转发 / 文件传输 的 HTTP+WS 服务，供前端 shim 连接）
	// 统一核心：18090 让给「核心界面后端」（core），Go 退到 18091 只处理还没迁走的接口。
	go func() {
		if err := serveBackend(goBackendPort()); err != nil {
			log.Printf("Go 后端服务异常: %v", err)
		}
	}()
	// 启动 MCP 出口：让外部的 AI 客户端能在**用户已经手动登录好的会话**上执行命令。
	// 独立端口（默认 39311）+ Bearer token，细节见 mcp_register.go。
	// 统一核心：界面后端（18090）由 core 提供（隐藏拉起、界面一字不改）；
	// 起不来就退回 Go 自己那套后端，保证界面还能用。
	if err := startUiBackend(18090, goBackendPort(), 39311, mcpJsonPath()); err != nil {
		log.Printf("核心界面后端起不来（退回 Go 后端，界面仍可用）：%v", err)
		go func() {
			if err2 := serveBackend(18090); err2 != nil {
				log.Printf("回退后端也起不来: %v", err2)
			}
		}()
	}

	// headless：只跑后端 + MCP，不开窗口。
	// 用途：① 没有桌面环境时验证 HTTP/薄壳路由；② 将来桌面壳崩溃时的排障入口。
	// 开关：BASTIONSHELL_HEADLESS=1
	if os.Getenv("BASTIONSHELL_HEADLESS") == "1" {
		log.Printf("headless 模式：核心后端 18090 + Go 后端 %d + MCP，不开窗口（Ctrl-C 退出）", goBackendPort())
		select {}
	}
	app := NewApp()
	// 退出时把 CLI 薄壳子进程收掉（别留孤儿 node）
	defer stopCliShell()
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
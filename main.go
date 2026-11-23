package main

import (
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"

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
	go func() {
		if err := serveBackend(18090); err != nil {
			log.Printf("后端服务异常: %v", err)
		}
	}()
	// 启动 MCP 出口：让外部的 AI 客户端能在**用户已经手动登录好的会话**上执行命令。
	// 独立端口（默认 39311）+ Bearer token，细节见 mcp_register.go。
	go func() {
		if err := startMcpServer(); err != nil {
			log.Printf("MCP 端点未能启动: %v（AI 功能不可用，其它功能不受影响）", err)
		}
	}()

	// headless：只跑后端 + MCP，不开窗口。
	// 用途：① 没有桌面环境时验证 HTTP/薄壳路由；② 将来桌面壳崩溃时的排障入口。
	// 开关：BASTIONSHELL_HEADLESS=1
	if os.Getenv("BASTIONSHELL_HEADLESS") == "1" {
		log.Printf("headless 模式：后端 %d + MCP 已启动，不开窗口（Ctrl-C 退出）", 18090)
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

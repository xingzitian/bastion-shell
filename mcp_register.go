package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MCP 端点的生命周期与配置。
//
// 对应 VS Code 扩展的 mcpRegister.ts。两处差异（都是桌面版形态决定的）：
//   - token 和端口存在**程序配置目录**（%AppData%\bastionshell\mcp.json），
//     和扩展的 ~/.bastionshell/mcp.json 不是一个文件 —— 桌面版自己的
//     profiles.json 也在这儿，保持同一套约定；
//   - MCP 监听**独立端口**（默认 39311），不挂在 18090 那个内部后端上：
//     那个后端是 CORS `*` 且不鉴权（只暴露档案/转发/上传），而 MCP 能执行任意命令，
//     绝不能共用同一条不设防的通道。

// defaultMcpPort 默认 MCP 端口。和 VS Code 扩展取同一个值：两个程序同时开着时
// 端口会冲突 → 后启动的那个自动退到随机端口，并在日志里说明。
const defaultMcpPort = 39311

type mcpConfig struct {
	Token      string `json:"token"`
	Port       int    `json:"port"`
	ActualPort int    `json:"actualPort"`
	URL        string `json:"url"`
	UpdatedAt  string `json:"updatedAt"`
	Note       string `json:"note,omitempty"`
}

// bastionConfigDir 程序配置目录（%AppData%\bastionshell / ~/.config/bastionshell）。
// BASTIONSHELL_CONFIG_DIR 可以改写它 —— 测试用它避免碰用户真实配置，
// 也给「绿色版/便携」留了口子。
func bastionConfigDir() string {
	if d := strings.TrimSpace(os.Getenv("BASTIONSHELL_CONFIG_DIR")); d != "" {
		_ = os.MkdirAll(d, 0o700)
		return d
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "bastionshell")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func mcpConfigPath() string {
	return filepath.Join(bastionConfigDir(), "mcp.json")
}

// loadOrCreateMcpConfig 读配置；没有（或坏了）就生成一份新的 token。
// token 是**长期**的：每次启动换 token 会让已经配置好的 AI 客户端失效。
func loadOrCreateMcpConfig() mcpConfig {
	path := mcpConfigPath()
	if data, err := os.ReadFile(path); err == nil {
		var c mcpConfig
		if json.Unmarshal(data, &c) == nil && len(c.Token) >= 16 {
			if c.Port <= 0 {
				c.Port = defaultMcpPort
			}
			return c
		}
	}
	c := mcpConfig{Token: newMcpToken(), Port: defaultMcpPort}
	_ = saveMcpConfig(c)
	return c
}

func saveMcpConfig(c mcpConfig) error {
	c.UpdatedAt = time.Now().Format(time.RFC3339)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600：token 等价于「能在这些服务器上执行任意命令」的凭证
	return os.WriteFile(mcpConfigPath(), data, 0o600)
}

// mcpDisabled 环境开关：BASTIONSHELL_MCP=0 / false / off 时不启动端点
func mcpDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BASTIONSHELL_MCP"))) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

var mcpState = struct {
	sync.Mutex
	handle *mcpHTTPHandle
	cfg    mcpConfig
	err    error
}{}

// startMcpServer 启动 MCP 端点（幂等：已经跑着就直接返回）
func startMcpServer() error {
	mcpState.Lock()
	defer mcpState.Unlock()
	if mcpState.handle != nil {
		return nil
	}
	if mcpDisabled() {
		log.Printf("MCP 端点已按 BASTIONSHELL_MCP 关闭")
		return nil
	}
	cfg := loadOrCreateMcpConfig()
	handle, err := startMcpHTTP(newMcpServer(desktopSessionAPI{}), cfg.Token, "127.0.0.1", cfg.Port, log.Printf)
	if err != nil {
		mcpState.err = err
		log.Printf("MCP 端点启动失败: %v", err)
		return err
	}
	cfg.ActualPort = handle.port
	cfg.URL = handle.url
	if handle.portFallback {
		cfg.Note = "默认端口被占用，已退到随机端口（本机可能还开着另一个 BastionShell 或 VS Code 扩展）"
	} else {
		cfg.Note = ""
	}
	_ = saveMcpConfig(cfg)

	mcpState.handle = handle
	mcpState.cfg = cfg
	mcpState.err = nil
	log.Printf("MCP 端点已启动: %s（token 见 %s）", handle.url, mcpConfigPath())
	return nil
}

func stopMcpServer() {
	mcpState.Lock()
	handle := mcpState.handle
	mcpState.handle = nil
	mcpState.Unlock()
	if handle != nil {
		handle.Close()
	}
}

// mcpEndpointInfo 端点信息（给 listSessions / health 用）
func mcpEndpointInfo() (url string, port int, fallback bool, ok bool) {
	mcpState.Lock()
	defer mcpState.Unlock()
	if mcpState.handle == nil {
		return "", 0, false, false
	}
	return mcpState.cfg.URL, mcpState.cfg.ActualPort, mcpState.cfg.Port != mcpState.cfg.ActualPort, true
}

// registerMcpInfoRoute 把端点信息挂到内部后端（前端设置页可以显示"AI 从哪里连进来"）。
//
// ⚠️ **故意不返回 token**：这个后端是 CORS `*` 且完全不鉴权的（只暴露档案/转发/上传），
// 而 token 等价于「能在这些服务器上执行任意命令」的凭证 —— 交给任意网页就是引狼入室。
// token 只在 mcp.json（0600）和程序日志里。
func registerMcpInfoRoute(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp-info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		url, port, fallback, ok := mcpEndpointInfo()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled":      ok,
			"url":          url,
			"port":         port,
			"portFallback": fallback,
			"configPath":   mcpConfigPath(),
			"toolCount":    len(mcpToolDefs),
			"hint":         "token 不在这里返回：见 configPath 指向的文件",
		})
	})
}

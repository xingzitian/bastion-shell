package main

// CLI 薄壳入口（B 阶段，第一步）。
//
// 目标：桌面不再自己跑 SSH/MCP，而是 spawn 一个 **CLI 宿主进程**
// （`node out/cli/main.js serve <档案>`），窗口里显示它提供的网页终端。
//
// 为什么先只做 HTTP 路由、不碰前端：这样"薄壳"能被一个浏览器直接验证
// （打开 http://127.0.0.1:18090/api/shell/cli?profile=xzt 就跳到真终端），
// 前端接不接、什么时候接，都不影响这条链路先跑通。
//
// CLI 的路径从环境变量 BASTIONSHELL_CLI_JS 来（默认找不到就报错并说明怎么设）——
// 桌面是独立 exe，不能假设仓库目录还在。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// termURLPattern 从 CLI 的 stderr 里抓网页终端地址。
// CLI 打的是一行：`[bastion] 浏览器打开：http://127.0.0.1:52341/term?token=…`
// termURLPattern 只认 `浏览器打开：` 那一行。
//
// 为什么不能只匹配 `/term?token=`：CLI 先打 `网页终端：<url>`（termServer 自己打的），
// 再打 `端点：{…}`，最后才是 `浏览器打开：<url>`。只看 URL 形状的话，第一行就把
// urlCh 填满了 —— select 从 urlCh 返回，宿主就不再写端点文件（2026-11-23 真机踩到）。
var termURLPattern = regexp.MustCompile(`浏览器打开：(http://127\.0\.0\.1:\d+/term\?token=[A-Za-z0-9_-]+)`)

// endpointPattern 抓 CLI 打的机器可读端点行：`[bastion] 端点：{"url":…}`
var endpointPattern = regexp.MustCompile(`端点：(\{[^\n]*\})`)

// cliEndpoint CLI 报告的事实（宿主据此自己写端点文件）
type cliEndpoint struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	TermURL   string `json:"termUrl"`
	Port      int    `json:"port"`
	ActualPort int   `json:"actualPort"`
}

// extractTermURL 从一段日志里取网页终端地址（纯函数，便于单测）。
func extractTermURL(logText string) string {
	m := termURLPattern.FindStringSubmatch(logText)
	if len(m) < 2 {
		return ""
	}
	return m[1] // 只要 URL 本体（前缀是给正则定位用的）
}

// extractEndpoint 从日志里取 CLI 报告的端点 JSON（纯函数）。
func extractEndpoint(logText string) *cliEndpoint {
	m := endpointPattern.FindStringSubmatch(logText)
	if len(m) < 2 {
		return nil
	}
	var ep cliEndpoint
	if err := json.Unmarshal([]byte(m[1]), &ep); err != nil || ep.URL == "" {
		return nil
	}
	return &ep
}

type cliShell struct {
	mu   sync.Mutex
	proc *exec.Cmd
	url  string
	prof string
}

var theCliShell cliShell

// cliScriptPath 找 CLI 入口（main.js）。
func cliScriptPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("BASTIONSHELL_CLI_JS")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("BASTIONSHELL_CLI_JS 指向的文件不存在：%s", p)
		}
		return p, nil
	}
	// 兜底：exe 同目录下的 bastion-cli/main.js（便携部署可以这么摆）
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "bastion-cli", "main.js")
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf(
		"没找到 CLI 入口。请设置环境变量 BASTIONSHELL_CLI_JS 指向 <仓库>/out/cli/main.js " +
			"（或在 exe 同目录放 bastion-cli/main.js）")
}

// legacyMcpFile 指向桌面版原来那份 mcp.json —— 让 CLI 接管旧身份，客户端配置不用改。
// BASTIONSHELL_CLI_MCP_FILE 可覆盖（便携部署 / 测试用，避免动真实配置）。
func legacyMcpFile() string {
	if p := strings.TrimSpace(os.Getenv("BASTIONSHELL_CLI_MCP_FILE")); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "bastionshell", "mcp.json")
}

// cliMcpPort 默认 39311（桌面版原来的端口）；BASTIONSHELL_CLI_MCP_PORT 可覆盖。
func cliMcpPort() string {
	if p := strings.TrimSpace(os.Getenv("BASTIONSHELL_CLI_MCP_PORT")); p != "" {
		return p
	}
	return "39311"
}

// startCliShell 起 CLI 宿主并等到网页终端地址（最多 40s）。
func startCliShell(profile string) (string, error) {
	theCliShell.mu.Lock()
	defer theCliShell.mu.Unlock()

	if theCliShell.proc != nil && theCliShell.url != "" && theCliShell.prof == profile {
		return theCliShell.url, nil // 同档案复用，不重复起
	}
	stopCliShellLocked()

	js, err := cliScriptPath()
	if err != nil {
		return "", err
	}
	args := []string{js, "serve", profile, "--print-endpoint"}
	if mf := legacyMcpFile(); mf != "" {
		args = append(args, "--mcp-file", mf, "--port", cliMcpPort())
	}
	cmd := exec.Command("node", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("起 node 失败：%w（需要 Node ≥ 22 在 PATH 里）", err)
	}

	urlCh := make(chan string, 1)
	epCh := make(chan *cliEndpoint, 1)
	exitedCh := make(chan struct{}, 1)
	var mu sync.Mutex
	var tail strings.Builder // CLI 的 stderr 尾巴：出错时给人看

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				mu.Lock()
				tail.Write(buf[:n])
				acc := tail.String()
				if len(acc) > 8192 {
					// 只留尾巴，别把内存吃满
					acc = acc[len(acc)-8192:]
					tail.Reset()
					tail.WriteString(acc)
				}
				mu.Unlock()
				// 端点行**优先且一次性**：它同时带 url/token/termUrl。
				// ⚠️ 不能"两个 channel 都塞" —— select 在多路就绪时是随机选，
				// 真机上因此经常走 urlCh，宿主就不会写端点文件（2026-11-23 实测）。
				if os.Getenv("BASTIONSHELL_CLI_DEBUG") == "1" && strings.Contains(acc, "端点") {
					log.Printf("[cli-debug] 拿到含端点字的输出：%q", truncateForLog(acc, 400))
					if ep := extractEndpoint(acc); ep != nil {
						log.Printf("[cli-debug] 解析成功：url=%s termUrl=%s", ep.URL, ep.TermURL)
					} else {
						log.Printf("[cli-debug] 解析失败（正则没匹配或 JSON 坏了）")
					}
				}
				if ep := extractEndpoint(acc); ep != nil && ep.TermURL != "" {
					select {
					case epCh <- ep:
						acc = "" // 已经交出去，别再让 URL 分支抢
					default:
					}
					continue
				}
				if u := extractTermURL(acc); u != "" {
					select {
					case urlCh <- u:
					default:
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		select {
		case exitedCh <- struct{}{}:
		default:
		}
	}()

	stderrTail := func() string {
		mu.Lock()
		defer mu.Unlock()
		s := tail.String()
		if len(s) > 600 {
			s = s[len(s)-600:]
		}
		return strings.TrimSpace(s)
	}

	select {
	case ep := <-epCh:
		// 宿主自己写端点文件（子进程写会 EPERM）；token 沿用，老客户端不用改配置
		writeLegacyEndpoint(ep)
		u := ep.TermURL
		if u == "" {
			u = ep.URL
		}
		theCliShell.proc = cmd
		theCliShell.url = u
		theCliShell.prof = profile
		return u, nil
	case u := <-urlCh:
		theCliShell.proc = cmd
		theCliShell.url = u
		theCliShell.prof = profile
		return u, nil
	case <-exitedCh:
		return "", fmt.Errorf(
			"CLI 进程提前退出（没给出网页终端地址）。\n命令：%s\nnode：%s\nstderr 尾巴：\n%s",
			strings.Join(cmd.Args, " "), cmd.Path, stderrTail())
	case <-time.After(40 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("等 CLI 的网页终端地址超时（40s）。stderr 尾巴：\n%s", stderrTail())
	}
}

func stopCliShellLocked() {
	if theCliShell.proc != nil && theCliShell.proc.Process != nil {
		_ = theCliShell.proc.Process.Kill()
	}
	theCliShell.proc = nil
	theCliShell.url = ""
	theCliShell.prof = ""
	_ = io.Discard // 保持 import（调试时改成 os.Stderr）
}

// stopCliShell 停掉薄壳进程（关窗口/应用退出时调）。
func stopCliShell() {
	theCliShell.mu.Lock()
	defer theCliShell.mu.Unlock()
	stopCliShellLocked()
}

// registerCliShellRoutes 挂两条路由：
//
//	GET  /api/shell/cli?profile=xzt   → 302 跳到网页终端（浏览器里可直接验证）
//	POST /api/shell/cli/stop          → 停掉薄壳进程
func registerCliShellRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/shell/cli", func(w http.ResponseWriter, r *http.Request) {
		profile := r.URL.Query().Get("profile")
		if profile == "" {
			http.Error(w, "需要 ?profile=<档案名>", http.StatusBadRequest)
			return
		}
		url, err := startCliShell(profile)
		if err != nil {
			w.Header().Set("content-type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway) // 502：壳子起不来，别让客户端以为是 200
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
	})

	mux.HandleFunc("/api/shell/cli/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
			return
		}
		stopCliShell()
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

// writeLegacyEndpoint 由**宿主**写 mcp.json（旧桌面版 schema），token 用 CLI 报告的那个。
//
// 为什么不是 CLI 自己写：桌面 exe spawn 出来的 CLI 写这个文件会 EPERM（父进程上下文差异，
// 2026-11-23 实测）。宿主写自己的配置目录一直没问题，所以由宿主落盘。
func writeLegacyEndpoint(ep *cliEndpoint) {
	file := legacyMcpFile()
	log.Printf("[cli] writeLegacyEndpoint: file=%q token=%s…", file, shortToken(ep.Token))
	if file == "" {
		log.Printf("[cli] 端点文件路径为空（BASTIONSHELL_CLI_MCP_FILE 未设、UserConfigDir 也取不到）")
		return
	}
	payload := map[string]any{
		"url":        ep.URL,
		"port":       ep.Port,
		"actualPort": ep.ActualPort,
		"token":      ep.Token,
		"updatedAt":  time.Now().Format("2006/01/02 15:04:05"),
		"host":       "cli",
	}
	if ep.Port != 0 && ep.ActualPort != 0 && ep.Port != ep.ActualPort {
		payload["note"] = "默认端口被占用，已退到随机端口"
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		log.Printf("写端点文件失败（建目录）：%v", err)
		return
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		log.Printf("写端点文件失败：%v", err)
		return
	}
	log.Printf("端点文件已更新（宿主写入）：%s（token 前缀 %s…）", file, shortToken(ep.Token))
}

func shortToken(t string) string {
	if len(t) <= 8 {
		return t
	}
	return t[:8]
}
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
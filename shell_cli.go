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
	"strconv"
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
	mu       sync.Mutex
	proc     *exec.Cmd
	url      string
	prof     string
	endpoint *cliEndpoint
	writeErr string
}

var theCliShell cliShell

// cliScriptCandidates 列出所有"CLI 入口可能在哪"的地方（纯函数，便于单测）。
//
// 为什么要一串候选：桌面是独立 exe，用户不会去设环境变量。
// 开发时 exe 在 `bastion-wails/build/bin/`，往上 3 层就是 `shell_tools/`，
// 旁边就是 `bastion-vscode/out/cli/main.js` —— 这个布局应该开箱即用。
func cliScriptCandidates(exeDir string) []string {
	return []string{
		filepath.Join(exeDir, "bastion-cli", "main.js"),                     // 便携部署（旧布局）
		filepath.Join(exeDir, "bastion-cli", "out", "cli", "main.js"),       // 便携部署（npm 包布局，见 tools/make-portable-cli.mjs）
		filepath.Join(exeDir, "..", "bastion-cli", "main.js"),               // 上一级
		filepath.Join(exeDir, "..", "bastion-cli", "out", "cli", "main.js"),
		filepath.Join(exeDir, "..", "..", "bastion-vscode", "out", "cli", "main.js"),
		filepath.Join(exeDir, "..", "..", "..", "bastion-vscode", "out", "cli", "main.js"),
		filepath.Join(exeDir, "..", "..", "..", "..", "bastion-vscode", "out", "cli", "main.js"),
	}
}

// cliScriptPath 找 CLI 入口（main.js）。
//
// 顺序：环境变量 → 候选路径。全都没有时，报错要**列出试过哪些地方** ——
// 否则用户只会看到"没找到 CLI"，然后来问我们。
func cliScriptPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("BASTIONSHELL_CLI_JS")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("BASTIONSHELL_CLI_JS 指向的文件不存在：%s", p)
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("取不到 exe 路径：%w", err)
	}
	exeDir := filepath.Dir(exe)
	tried := cliScriptCandidates(exeDir)
	for _, cand := range tried {
		if _, err := os.Stat(cand); err == nil {
			log.Printf("找到 CLI 入口：%s", cand)
			return cand, nil
		}
	}
	var b strings.Builder
	b.WriteString("没找到 CLI 入口（main.js）。试过：\n")
	for _, cand := range tried {
		b.WriteString("  - " + cand + "\n")
	}
	b.WriteString("解决：设环境变量 BASTIONSHELL_CLI_JS=<仓库>/out/cli/main.js（先 npm run compile）")
	return "", fmt.Errorf("%s", b.String())
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
func startCliShell(profiles []string, passwords map[string]string) (string, error) {
	theCliShell.mu.Lock()
	defer theCliShell.mu.Unlock()

	key := strings.Join(profiles, ",")
	if theCliShell.proc != nil && theCliShell.url != "" && theCliShell.prof == key {
		return theCliShell.url, nil // 同一组档案复用，不重复起
	}
	stopCliShellLocked()

	js, err := cliScriptPath()
	if err != nil {
		return "", err
	}
	args := append([]string{js, "serve"}, profiles...)
	args = append(args, "--print-endpoint")
	// 桌面被强杀时别留孤儿 node（CLI 每 5s 看一眼父进程）
	args = append(args, "--parent-pid", strconv.Itoa(os.Getpid()))
	if mf := legacyMcpFile(); mf != "" {
		args = append(args, "--mcp-file", mf, "--port", cliMcpPort())
	}
	args = append(args, "--passwords-stdin")
	cmd := exec.Command("node", args...)
	hideWindow(cmd) // 不弹控制台窗口（用户 2026-11-23 报"窗口一闪而过"）
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("起 node 失败：%w（需要 Node ≥ 22 在 PATH 里）", err)
	}
	// 密码走 stdin（不进 argv：进程列表里能看到 argv）
	go func() {
		defer stdin.Close()
		if len(passwords) == 0 {
			return
		}
		b, _ := json.Marshal(passwords)
		_, _ = stdin.Write(b)
	}()

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
		if err := writeLegacyEndpoint(ep); err != nil {
			// 写不进去也要能用：把原因记下来，/api/shell/endpoint 会一并返回
			log.Printf("端点文件没写成（不影响终端）：%v", err)
			theCliShell.writeErr = err.Error()
		} else {
			theCliShell.writeErr = ""
		}
		theCliShell.endpoint = ep
		u := ep.TermURL
		if u == "" {
			u = ep.URL
		}
		theCliShell.proc = cmd
		theCliShell.url = u
		theCliShell.prof = key
		return u, nil
	case u := <-urlCh:
		theCliShell.proc = cmd
		theCliShell.url = u
		theCliShell.prof = key
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

// registerFsProbeRoute 诊断用：从**应用进程**视角，逐个目录试写一个文件。
//
// 为什么需要它：从某些启动上下文（比如代理/沙箱里起 GUI）打开时，应用写
// %APPDATA% 的新文件会被拒，而同一个 shell 里 pwsh 写同一目录却正常 ——
// 这条路由能把"到底哪些目录不可写"一次说清，省得靠猜。
func registerFsProbeRoute(mux *http.ServeMux) {
	mux.HandleFunc("/api/shell/fs-probe", func(w http.ResponseWriter, r *http.Request) {
		type result struct {
			Dir   string `json:"dir"`
			OK    bool   `json:"ok"`
			Error string `json:"error,omitempty"`
		}
		out := []result{}
		add := func(dir string) {
			if dir == "" {
				return
			}
			f := filepath.Join(dir, "bs-fs-probe.txt")
			err := os.MkdirAll(dir, 0o755)
			if err == nil {
				err = os.WriteFile(f, []byte("probe"), 0o644)
			}
			res := result{Dir: dir, OK: err == nil}
			if err != nil {
				res.Error = err.Error()
			} else {
				_ = os.Remove(f)
			}
			out = append(out, res)
		}
		add(os.TempDir())
		if dir, err := os.UserConfigDir(); err == nil {
			add(filepath.Join(dir, "bastionshell"))
		}
		if home, err := os.UserHomeDir(); err == nil {
			add(filepath.Join(home, ".bastionshell"))
		}
		if exe, err := os.Executable(); err == nil {
			add(filepath.Dir(exe))
		}
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// registerCliShellRoutes 挂两条路由：
//
//	GET  /api/shell/cli?profile=xzt         → 302 跳到网页终端（单条）
//	GET  /api/shell/cli?profiles=xzt,xb     → 302 跳到网页终端（会话组，网页里可切换）
//	POST /api/shell/cli/stop                → 停掉薄壳进程
func registerCliShellRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/shell/cli", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var profiles []string
		for _, one := range q["profile"] {
			if s := strings.TrimSpace(one); s != "" {
				profiles = append(profiles, s)
			}
		}
		if csv := strings.TrimSpace(q.Get("profiles")); csv != "" {
			for _, one := range strings.Split(csv, ",") {
				if s := strings.TrimSpace(one); s != "" {
					profiles = append(profiles, s)
				}
			}
		}
		if len(profiles) == 0 {
			http.Error(w, "需要 ?profile=<档案名> 或 ?profiles=a,b,c", http.StatusBadRequest)
			return
		}
		url, err := startCliShell(profiles, nil)
		if err != nil {
			w.Header().Set("content-type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway) // 502：壳子起不来，别让客户端以为是 200
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
	})

	mux.HandleFunc("/api/shell/endpoint", func(w http.ResponseWriter, r *http.Request) {
		theCliShell.mu.Lock()
		ep := theCliShell.endpoint
		werr := theCliShell.writeErr
		url := theCliShell.url
		prof := theCliShell.prof
		childPid := 0
		if theCliShell.proc != nil && theCliShell.proc.Process != nil {
			childPid = theCliShell.proc.Process.Pid
		}
		theCliShell.mu.Unlock()
		w.Header().Set("content-type", "application/json; charset=utf-8")
		if ep == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"running": false, "termUrl": url, "profile": prof, "pid": childPid})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"running":    true,
			"profile":    prof,
			"termUrl":    ep.TermURL,
			"mcpUrl":     ep.URL,
			"token":      ep.Token,
			"port":       ep.Port,
			"actualPort": ep.ActualPort,
			"writeError": werr, // 非空 = 宿主没写成 mcp.json（见 /api/shell/fs-probe）
			"pid":        childPid, // CLI 子进程 pid：前端/测试用它判断有没有重复 spawn
		})
	})

	// POST /api/shell/cli：带密码启动会话组（桌面用；密码只走子进程 stdin，不落盘）
	mux.HandleFunc("/api/shell/cli/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Profiles []struct {
				Name     string `json:"name"`
				Password string `json:"password"`
			} `json:"profiles"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "请求体不是合法 JSON", http.StatusBadRequest)
			return
		}
		var names []string
		passwords := map[string]string{}
		for _, p := range body.Profiles {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				continue
			}
			names = append(names, name)
			if p.Password != "" {
				passwords[name] = p.Password
			}
		}
		if len(names) == 0 {
			http.Error(w, "需要至少一个档案", http.StatusBadRequest)
			return
		}
		url, err := startCliShell(names, passwords)
		w.Header().Set("content-type", "application/json; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": url})
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
func writeLegacyEndpoint(ep *cliEndpoint) error {
	// port 记"请求的端口"、actualPort 记实际端口（0.9.2 的语义：客户端可能写死 39311）
	reqPort := ep.Port
	if uiMcpRequestPort > 0 {
		reqPort = uiMcpRequestPort
	}
	file := legacyMcpFile()
	log.Printf("[cli] writeLegacyEndpoint: file=%q token=%s…", file, shortToken(ep.Token))
	if file == "" {
		return fmt.Errorf("端点文件路径为空（BASTIONSHELL_CLI_MCP_FILE 未设、UserConfigDir 也取不到）")
	}
	payload := map[string]any{
		"url":        ep.URL,
		"port":       reqPort,
		"actualPort": ep.ActualPort,
		"token":      ep.Token,
		"updatedAt":  time.Now().Format("2006/01/02 15:04:05"),
		"host":       "cli",
	}
	if reqPort != 0 && ep.ActualPort != 0 && reqPort != ep.ActualPort {
		payload["note"] = "默认端口被占用，已退到随机端口"
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("建目录失败：%w", err)
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		return fmt.Errorf("写 %s 失败：%w", file, err)
	}
	log.Printf("端点文件已更新（宿主写入）：%s（token 前缀 %s…）", file, shortToken(ep.Token))
	return nil
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
// startUiBackend 拉起「核心界面后端」：node <cli> ui --port 18090 --proxy http://127.0.0.1:<goPort>
//
// 这是"统一核心"的第一步：界面继续用 18090 那套 HTTP/WS 协议，但服务方换成 core；
// 还没迁进 core 的接口（secret/upload/forwards/pick-files/shell）由 core 反向代理给 Go。
// 隐藏窗口、跟着父进程退出（--parent-pid），用户看不到任何命令行。
func startUiBackend(corePort, goPort, mcpPort int, mcpFile string) error {
	js, err := cliScriptPath()
	if err != nil {
		return err
	}
	args := []string{js, "ui", "--port", strconv.Itoa(corePort), "--proxy", "http://127.0.0.1:" + strconv.Itoa(goPort), "--parent-pid", strconv.Itoa(os.Getpid())}
	uiMcpRequestPort = mcpPort
	if mcpPort > 0 {
		args = append(args, "--mcp-port", strconv.Itoa(mcpPort))
	}
	if mcpFile != "" {
		// 不传 --mcp-file：由 Go 解析 --print-endpoint 的端点行并自己写（子进程写可能被拒）
		args = append(args, "--print-endpoint")
	}
	cmd := exec.Command("node", args...)
	hideWindow(cmd)
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("起核心界面后端失败：%w", err)
	}
	go func() {
		buf := make([]byte, 4096)
		var acc strings.Builder
		for {
			n, e := stderr.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				log.Printf("[core-ui] %s", strings.TrimRight(chunk, "\r\n"))
				acc.WriteString(chunk)
				if ep := extractEndpoint(acc.String()); ep != nil {
					if err := writeLegacyEndpoint(ep); err != nil {
						log.Printf("写 mcp.json 失败：%v", err)
					} else {
						log.Printf("mcp.json 已由 Go 写入（端口 %d）", ep.ActualPort)
					}
					acc.Reset()
				}
			}
			if e != nil {
				return
			}
		}
	}()
	theCliShell.mu.Lock()
	theCliShell.proc = cmd // 复用同一个句柄：退出时 stopCliShell 会收掉
	theCliShell.prof = "__ui__"
	theCliShell.mu.Unlock()
	log.Printf("核心界面后端已启动：http://127.0.0.1:%d（代理 → Go %d）", corePort, goPort)
	return nil
}
// uiMcpRequestPort 桌面向 core 请求的 MCP 端口（39311）；写 mcp.json 时用它当 port 字段。
var uiMcpRequestPort int

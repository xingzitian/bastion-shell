package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/trzsz/trzsz-go/trzsz"
	"golang.org/x/crypto/ssh"
)

// defaultDownloadDir 文件下载默认目录：Downloads 下的 BastionShell 子目录。
// 不直接落 Downloads 根目录：Edge/WebView2 的下载管理器监控 Downloads 根目录，
// 文件落根目录会触发它的「下载完成」通知/侧边栏预览，与我们的 rz 捕获打架。
func defaultDownloadDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Downloads", "BastionShell")
	}
	return filepath.Join(os.TempDir(), "bastionshell-download")
}

type wsMsg struct {
	Type     string `json:"type"`
	ConnID   string `json:"connId,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Key      string `json:"key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	MFA      string `json:"mfa,omitempty"`
	Cols     int    `json:"cols,omitempty"`
	Rows     int    `json:"rows,omitempty"`
	Data     string `json:"data,omitempty"`
	Message  string `json:"message,omitempty"`
	// 端口转发用
	ID         string `json:"id,omitempty"`
	LocalHost  string `json:"localHost,omitempty"`
	LocalPort  int    `json:"localPort,omitempty"`
	RemoteHost string `json:"remoteHost,omitempty"`
	RemotePort int    `json:"remotePort,omitempty"`
	// 文件上传用
	Paths []string `json:"paths,omitempty"`
	// 认证交互（MFA）用
	Nonce        string `json:"nonce,omitempty"`
	Name         string `json:"name,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Echo         bool   `json:"echo,omitempty"`
	Value        string `json:"value,omitempty"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // 本机工具，允许同源/跨源
}

// ---- 连接池：connId → SSH 连接 + 引用计数（连接复用核心）----
type connEntry struct {
	client *ssh.Client
	refs   int
}

var connPool = struct {
	sync.Mutex
	m map[string]*connEntry
}{m: map[string]*connEntry{}}

// acquireConn 获取或新建连接：connId 已存在则复用（refs+1），否则用 msg 里的凭据新建
func acquireConn(connID string, m wsMsg) (*ssh.Client, error) {
	connPool.Lock()
	defer connPool.Unlock()
	if connID != "" {
		if e, ok := connPool.m[connID]; ok {
			e.refs++
			log.Printf("[conn %s] 复用连接（当前 %d 个会话）", connID, e.refs)
			return e.client, nil
		}
	}
	client, err := connect(m.Host, m.Port, m.User, m.Password, m.Key, m.Passphrase, m.MFA)
	if err != nil {
		return nil, err
	}
	if connID != "" {
		connPool.m[connID] = &connEntry{client: client, refs: 1}
		log.Printf("[conn %s] 新建连接", connID)
	}
	return client, nil
}

// releaseConn 释放连接：refs-1，归零则关闭连接、停止该连接上的转发并从池移除
func releaseConn(connID string) {
	if connID == "" {
		return
	}
	var shouldClose bool
	var client *ssh.Client
	connPool.Lock()
	if e, ok := connPool.m[connID]; ok {
		e.refs--
		if e.refs <= 0 {
			delete(connPool.m, connID)
			shouldClose = true
			client = e.client
		} else {
			log.Printf("[conn %s] 释放会话（剩余 %d 个）", connID, e.refs)
		}
	}
	connPool.Unlock()
	if shouldClose {
		_ = client.Close()
		stopForwardsOfConn(connID)
		log.Printf("[conn %s] 连接已关闭（会话全部结束）", connID)
	}
}

// serveBackend 启动后端服务：/ws 终端通道 + /api 存取接口。
// 前端静态资源由 Wails 内嵌服务（frontend/dist），不再走这里。
// 加 CORS：前端从 wails:// 源 fetch http://127.0.0.1:18090/api/... 需要跨域许可。
func serveBackend(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleWS)
	registerProfileRoutes(mux, newProfileStore())
	registerForwardRoutes(mux, newForwardStore())
	registerUploadRoutes(mux)
	registerDeployRoutes(mux)
	registerSecretRoutes(mux, newSecretStore())
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("后端服务已启动: http://%s", addr)
	return http.ListenAndServe(addr, corsMiddleware(mux))
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleForwardMsg 处理端口转发消息；返回 true 表示已处理。
// control 通道（连接级）与 session 通道（会话级）共用同一套转发逻辑。
func handleForwardMsg(m wsMsg, connID string, client *ssh.Client, write func(wsMsg)) bool {
	switch m.Type {
	case "forward:start":
		rule := forwardRule{
			ID:         m.ID,
			LocalHost:  m.LocalHost,
			LocalPort:  m.LocalPort,
			RemoteHost: m.RemoteHost,
			RemotePort: m.RemotePort,
		}
		if rule.LocalHost == "" {
			rule.LocalHost = "127.0.0.1"
		}
		if err := startForward(connID, client, rule); err != nil {
			write(wsMsg{Type: "forward:error", ID: rule.ID, Message: err.Error()})
		} else {
			write(wsMsg{Type: "forward:started", ID: rule.ID})
		}
		return true
	case "forward:stop":
		stopForward(m.ID)
		write(wsMsg{Type: "forward:stopped", ID: m.ID})
		return true
	}
	return false
}

// controlChannel 处理 control 通道：复用已有连接或异步交互式建连（MFA），
// 就绪后进入转发循环。连接生命周期由 releaseConn 管理。
func controlChannel(conn *websocket.Conn, first wsMsg, write func(wsMsg)) {
	// 复用已有连接
	connPool.Lock()
	if e, ok := connPool.m[first.ConnID]; ok && first.ConnID != "" {
		e.refs++
		connPool.Unlock()
		defer releaseConn(first.ConnID)
		write(wsMsg{Type: "ready", ConnID: first.ConnID})
		for {
			var m wsMsg
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			handleForwardMsg(m, first.ConnID, e.client, write)
		}
	}
	connPool.Unlock()

	// 新连接：异步交互式 dial（MFA 提示回传，答案从 authCh 来）
	authCh := make(chan wsMsg, 16)
	type dialResult struct {
		client *ssh.Client
		err    error
	}
	dialDone := make(chan dialResult, 1)
	go func() {
		c, err := dialInteractive(first.Host, first.Port, first.User, first.Password, first.Key, first.Passphrase, first.ConnID, write, authCh)
		dialDone <- dialResult{client: c, err: err}
	}()

	msgCh := make(chan wsMsg, 16)
	go func() {
		for {
			var m wsMsg
			if err := conn.ReadJSON(&m); err != nil {
				close(msgCh)
				return
			}
			msgCh <- m
		}
	}()

	for {
		select {
		case r := <-dialDone:
			if r.err != nil {
				write(wsMsg{Type: "error", Message: r.err.Error()})
				return
			}
			connPool.Lock()
			connPool.m[first.ConnID] = &connEntry{client: r.client, refs: 1}
			connPool.Unlock()
			defer releaseConn(first.ConnID)
			write(wsMsg{Type: "ready", ConnID: first.ConnID})
			for {
				m, ok := <-msgCh
				if !ok {
					return
				}
				handleForwardMsg(m, first.ConnID, r.client, write)
			}
		case m, ok := <-msgCh:
			if !ok {
				close(authCh) // WS 关闭 → 让 dial goroutine 结束（不泄漏）
				return
			}
			if m.Type == "auth-answer" {
				authCh <- m
			}
		}
	}
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	write := func(m wsMsg) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(m)
	}

	var first wsMsg
	if err := conn.ReadJSON(&first); err != nil {
		return
	}
	if first.Type != "connect" && first.Type != "control" {
		write(wsMsg{Type: "error", Message: "首条消息必须是 connect 或 control"})
		return
	}

	// control 通道：只持有连接（两段式模型）+ 转发 + 交互式 MFA，不 open shell
	if first.Type == "control" {
		controlChannel(conn, first, write)
		return
	}

	client, err := acquireConn(first.ConnID, first)
	if err != nil {
		write(wsMsg{Type: "error", Message: err.Error()})
		return
	}
	defer releaseConn(first.ConnID)

	rows, cols := first.Rows, first.Cols
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}

	sess, err := client.NewSession()
	if err != nil {
		write(wsMsg{Type: "error", Message: err.Error()})
		return
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		write(wsMsg{Type: "error", Message: err.Error()})
		return
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		write(wsMsg{Type: "error", Message: err.Error()})
		return
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		write(wsMsg{Type: "error", Message: err.Error()})
		return
	}

	// trzsz 过滤器：包在客户端(WS)与服务端(SSH)之间，自动处理 rz/sz 文件传输
	clientIn, stdinPipe := io.Pipe()   // 终端输入：WS → stdinPipe → clientIn → 过滤器 → stdin
	stdoutPipe, clientOut := io.Pipe() // 终端输出：stdout → 过滤器 → clientOut → stdoutPipe → WS
	dragCmd := "trz -y"
	if useZmodem {
		dragCmd = "rz -y"
	}
	tf := trzsz.NewTrzszFilter(clientIn, clientOut, stdin, stdout, trzsz.TrzszOptions{
		TerminalColumns: int32(cols),
		EnableZmodem:    useZmodem, // Windows 上用内嵌 lrzsz 走 zmodem（服务器只需 rz/sz）；Linux 自测用原生 trzsz
	})
	tf.SetDragFileUploadCommand(dragCmd) // 上传：trz -y（原生）或 rz -y（zmodem），覆盖已存在
	downloadDir := defaultDownloadDir()
	_ = os.MkdirAll(downloadDir, 0o755)
	tf.SetDefaultDownloadPath(downloadDir)
	defer tf.Close()

	// 传输完成信号：只在真正传输结束（transferring=false）时才发 upload:done，
	// 避免 UploadFiles 一入队就误报"完成"。
	var uploadPending atomic.Bool
	tf.SetTransferStateCallback(func(transferring bool) {
		if !transferring && uploadPending.Load() {
			uploadPending.Store(false)
			write(wsMsg{Type: "upload:done"})
		}
	})

	if err := sess.Shell(); err != nil {
		write(wsMsg{Type: "error", Message: err.Error()})
		return
	}
	write(wsMsg{Type: "ready", ConnID: first.ConnID})

	// 终端输出：stdoutPipe → ws
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				write(wsMsg{Type: "data", Data: string(buf[:n])})
			}
			if err != nil {
				return
			}
		}
	}()

	// ws → stdin / resize / forward / upload
	for {
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		switch m.Type {
		case "input":
			_, _ = stdinPipe.Write([]byte(m.Data))
		case "upload":
			uploadPending.Store(true)
			go func(paths []string) {
				if err := tf.UploadFiles(paths); err != nil {
					uploadPending.Store(false)
					write(wsMsg{Type: "upload:error", Message: err.Error()})
				}
				// 成功时不再立即发 upload:done；等 transfer state callback(false) 时发
			}(m.Paths)
		case "resize":
			if m.Cols > 0 && m.Rows > 0 {
				_ = sess.WindowChange(m.Rows, m.Cols)
				tf.SetTerminalColumns(int32(m.Cols))
			}
		case "forward:start", "forward:stop":
			handleForwardMsg(m, first.ConnID, client, write)
		}
	}
}

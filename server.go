package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
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
	Type       string `json:"type"`
	ConnID     string `json:"connId,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	Key        string `json:"key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	MFA        string `json:"mfa,omitempty"`
	Cols       int    `json:"cols,omitempty"`
	Rows       int    `json:"rows,omitempty"`
	Data       string `json:"data,omitempty"`
	Message    string `json:"message,omitempty"`
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
	// 会话 id（前端可选传；不传就由后端生成，会话登记表用它当主键）
	SessionID string `json:"sessionId,omitempty"`
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
		// 记下这条连接是谁连的哪里：会话 WS 的 connect 消息里只有 connId，
		// 而会话登记表要用 用户@主机 给会话起名字（否则 AI 只能看到一串 id）。
		rememberConn(connID, connMeta{Host: m.Host, Port: m.Port, User: m.User})
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
		forgetConn(connID)
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
	registerQuickCommandRoutes(mux)
	registerDeployRoutes(mux)
	registerSecretRoutes(mux, newSecretStore())
	registerMcpInfoRoute(mux)
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
			// 交互式建连这条路径也要记元信息（会话登记表靠它给会话起名字）
			rememberConn(first.ConnID, connMeta{Host: first.Host, Port: first.Port, User: first.User})
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

	// 会话本体（pty + shell + trzsz 过滤器 + 输出登记）统一在 newBastionSession 里建：
	// 它同时被 handleWS 和 Go 测试用到，避免「测试跑的路径」和「真机跑的路径」是两份实现。
	var uploadPending atomic.Bool
	sess, err := newBastionSession(sessionOpts{
		ID:     first.SessionID,
		ConnID: first.ConnID,
		Client: client,
		Cols:   first.Cols,
		Rows:   first.Rows,
		Out: func(b []byte) {
			write(wsMsg{Type: "data", Data: string(b)})
		},
	})
	if err != nil {
		write(wsMsg{Type: "error", Message: err.Error()})
		return
	}
	defer sess.close()

	// 传输完成信号：只在真正传输结束（transferring=false）时才发 upload:done，
	// 避免 UploadFiles 一入队就误报"完成"。
	sess.addTransferListener(func(transferring bool) {
		if !transferring && uploadPending.Load() {
			uploadPending.Store(false)
			write(wsMsg{Type: "upload:done"})
		}
	})

	write(wsMsg{Type: "ready", ConnID: first.ConnID})

	// ws → stdin / resize / forward / upload
	for {
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		switch m.Type {
		case "input":
			_ = sess.writeRaw([]byte(m.Data))
		case "upload":
			uploadPending.Store(true)
			go func(paths []string) {
				if err := sess.uploadFiles(paths); err != nil {
					uploadPending.Store(false)
					write(wsMsg{Type: "upload:error", Message: err.Error()})
				}
				// 成功时不再立即发 upload:done；等 transfer state callback(false) 时发
			}(m.Paths)
		case "resize":
			sess.resize(m.Cols, m.Rows)
		case "forward:start", "forward:stop":
			handleForwardMsg(m, first.ConnID, client, write)
		}
	}
}

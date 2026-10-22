package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// MCP 服务端：JSON-RPC 消息处理（纯逻辑）+ Streamable HTTP 传输（Go 标准库）。
//
// 移植自 VS Code 扩展（bastion-vscode/src/mcpServer.ts），**行为逐条对齐** ——
// 那边已经用官方 MCP SDK（v1.30.0）做过 13 项互操作验证，这里不重新发明协议。
//
// 传输上只实现客户端真正会用到的那部分：
//   - `POST /mcp`：JSON-RPC 请求 → 200 + `application/json`
//   - 通知（没有 id）→ 202 + 空 body
//   - `GET /mcp` → 405（规范允许；我们不做服务端推送，所以不开 SSE 长连接）
//   - 鉴权：`Authorization: Bearer <token>`，只监听 127.0.0.1

// latestProtocolVersion 我们实现的 MCP 协议版本；握手时按客户端请求回
const latestProtocolVersion = "2025-06-18"

var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// maxMcpBodyBytes 请求体上限：正常一次工具调用只有几十 KB，超过就是有人在乱发
const maxMcpBodyBytes = 1024 * 1024

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

func rpcOK(id json.RawMessage, result any) *jsonRPCResponse {
	return &jsonRPCResponse{JSONRPC: "2.0", ID: idOrNull(id), Result: result}
}

func rpcFail(id json.RawMessage, code int, message string) *jsonRPCResponse {
	return &jsonRPCResponse{JSONRPC: "2.0", ID: idOrNull(id), Error: &jsonRPCError{Code: code, Message: message}}
}

// rpcFailData 带 data 的错误（-32022 / -32020 需要 supported 列表等附加信息）
func rpcFailData(id json.RawMessage, e map[string]any) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      idOrNull(id),
		Error:   &jsonRPCError{Code: int(e["code"].(int)), Message: e["message"].(string), Data: e["data"]},
	}
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// mcpServer JSON-RPC 处理器（不含传输，能单独测）
type mcpServer struct {
	api          mcpSessionAPI
	serverName   string
	serverVer    string
	instructions string
	defs         []mcpToolDef
}

func newMcpServer(api mcpSessionAPI) *mcpServer {
	return &mcpServer{
		api: api,
		// 名字里**不要放连字符**：VS Code 把工具暴露成 `mcp_<server>_<tool>`，
		// 名字越短越不容易被模型记错，也少一个可能被客户端清洗的字符。
		serverName:   "bastionshell",
		serverVer:    version,
		instructions: mcpInstructions,
		defs:         mcpToolDefs,
	}
}

// handle 处理一条 JSON-RPC 消息；返回 nil 表示这是通知、不需要应答
func (m *mcpServer) handle(msg *jsonRPCRequest, headerVersion string) *jsonRPCResponse {
	isNotification := len(msg.ID) == 0 || string(msg.ID) == "null"
	if strings.TrimSpace(msg.Method) == "" {
		if isNotification {
			return nil
		}
		return rpcFail(msg.ID, -32600, "无效的 JSON-RPC 请求：method 必须是字符串")
	}

	// ── 两个时代 ────────────────────────────────────────────────
	// modern（2026-07-28+）：版本/身份/能力在每个请求的 _meta；legacy：initialize + 不带 _meta。
	// 不带 _meta 的请求按 legacy 处理（老客户端照旧能用）。
	meta := readRequestMeta(msg.Params)
	if meta.Version == "" && headerVersion != "" {
		// 有些客户端只把版本放 HTTP 头（MCP-Protocol-Version），兜底一下
		meta.Version = headerVersion
	}
	modern := eraOf(meta) == "modern"
	tasksCapable := clientSupportsTasks(meta)
	if modern && !isSupportedVersion(meta.Version) {
		e := unsupportedVersionError(meta.Version)
		if isNotification {
			return nil
		}
		return rpcFailData(msg.ID, e)
	}
	if headerVersion != "" && meta.Version != "" && headerVersion != meta.Version {
		e := headerMismatchError(headerVersion, meta.Version)
		if isNotification {
			return nil
		}
		return rpcFailData(msg.ID, e)
	}

	switch msg.Method {
	case "server/discover":
		// 2026-07-28：服务端**必须**实现它
		if isNotification {
			return nil
		}
		return rpcOK(msg.ID, discoverResult(m.serverName, m.serverVer, m.instructions))

	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		// 报了一个我们不支持的版本：别静默降级（和头路径保持一致）
		if p.ProtocolVersion != "" && !isSupportedVersion(p.ProtocolVersion) {
			if isNotification {
				return nil
			}
			return rpcFailData(msg.ID, unsupportedVersionError(p.ProtocolVersion))
		}
		version := latestProtocolVersion
		if isSupportedVersion(p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return rpcOK(msg.ID, map[string]any{
			"protocolVersion": version,
			// listChanged: false —— 工具是固定这几个，不会变，别让客户端等通知
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":   map[string]any{"name": m.serverName, "version": m.serverVer},
			"instructions": m.instructions,
		})

	// 客户端在 initialize 之后发一次，确认握手完成。通知，不需要应答。
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		return nil

	case "ping":
		if isNotification {
			return nil
		}
		return rpcOK(msg.ID, map[string]any{})

	case "tools/list":
		if isNotification {
			return nil
		}
		tools := make([]map[string]any, 0, len(m.defs))
		for _, d := range m.defs {
			tools = append(tools, toMcpTool(d))
		}
		result := map[string]any{"tools": tools}
		withResultType(result, modern)
		withCache(result, modern, listTTLMs)
		return rpcOK(msg.ID, result)

	case "tasks/get":
		{
			if isNotification {
				return nil
			}
			jobID := taskIDOf(msg.Params)
			job := getJob(jobID)
			if job == nil {
				return rpcFail(msg.ID, errInvalidParams, "找不到任务「"+jobID+"」")
			}
			v := job.view(sessionCursor(job.sessionID))
			return rpcOK(msg.ID, taskSnapshot(v, v.Prompt, v.Hint))
		}

	case "tasks/update", "tasks/cancel":
		{
			if isNotification {
				return nil
			}
			jobID := taskIDOf(msg.Params)
			job := getJob(jobID)
			if job == nil {
				return rpcFail(msg.ID, errInvalidParams, "找不到任务「"+jobID+"」")
			}
			if msg.Method == "tasks/cancel" {
				if s := sessionByID(job.sessionID); s != nil {
					if _, err := s.stdin.Write([]byte{0x03}); err == nil {
						job.cancel()
						s.activeJob = nil
					}
				} else {
					job.fail("session_lost")
				}
			}
			// tasks/update：我们的人是在终端里直接输入的，这里只做确认（状态由终端输出推进）
			return rpcOK(msg.ID, map[string]any{"resultType": "complete"})
		}

	case "tools/call":
		var p struct {
			Name           string         `json:"name"`
			Arguments      map[string]any `json:"arguments"`
			InputResponses any            `json:"inputResponses"`
			RequestState   string         `json:"requestState"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if strings.TrimSpace(p.Name) == "" {
			return rpcFail(msg.ID, errInvalidParams, "无效参数：tools/call 需要 name")
		}

		// ── MRTR 第二步：客户端带 inputResponses + requestState 重试 ──
		if p.InputResponses != nil && p.RequestState != "" {
			jobID := decodeRequestState(p.RequestState)
			text, err := m.api.ResumeTask(jobID, p.InputResponses)
			if err != nil {
				return rpcOK(msg.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "续跑失败：" + err.Error()}},
					"isError": true,
				})
			}
			return rpcOK(msg.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
			})
		}

		r, err := mcpDispatch(m.api, p.Name, p.Arguments)
		if err != nil {
			// 工具不存在 → 协议错误；工具内部出错 → 一条 isError 的结果（模型能读到原因）
			return rpcFail(msg.ID, errInvalidParams, err.Error())
		}
		payload := map[string]any{"content": []map[string]any{{"type": "text", "text": r.Text}}}
		if r.IsError {
			payload["isError"] = true
		}

		// ── MRTR：等人工输入 → InputRequiredResult（客户端可带 inputResponses 重试）──
		if modern && r.JobID != "" && r.JobStatus == "input_required" {
			prompt := ""
			if j := getJob(r.JobID); j != nil {
				v := j.view(0)
				prompt = strings.TrimSpace(v.Prompt + "\n" + v.Hint)
			}
			return rpcOK(msg.ID, map[string]any{
				"resultType": "input_required",
				"inputRequests": map[string]any{
					"prompt": map[string]any{
						"method": "elicitation/create",
						"params": map[string]any{
							"mode":            "form",
							"message":         prompt,
							"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
						},
					},
				},
				"requestState": encodeRequestState(r.JobID),
			})
		}

		// ── Tasks 扩展：长任务先给句柄（只有声明了扩展的客户端才给）──
		if tasksCapable && r.JobID != "" && r.JobStatus != "" && r.JobStatus != "completed" && r.JobStatus != "failed" {
			return rpcOK(msg.ID, createTaskResult(r.JobID, r.JobStatus))
		}

		withResultType(payload, modern)
		return rpcOK(msg.ID, payload)

	default:
		// 未实现的方法（resources/*、prompts/*…）：我们没有声明这些能力，直接说不支持
		if isNotification {
			return nil
		}
		return rpcFail(msg.ID, errMethodMissing, "不支持的方法："+msg.Method)
	}
}

// taskIDOf 从 tasks/* 的参数里取 taskId
func taskIDOf(params json.RawMessage) string {
	var p struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.TaskID
}

// sessionCursor 作业所在会话的屏幕游标（cursorMoved 用）
func sessionCursor(sessionID string) int64 {
	if s := sessionByID(sessionID); s != nil {
		return s.cursor.Load()
	}
	return 0
}

// ─────────────────────────── HTTP 传输 ───────────────────────────

// mcpHTTPHandler 构造 MCP 的 HTTP 处理器。
// 单独抽出来是为了测试能用 httptest 起真端点，不必真去绑端口。
func mcpHTTPHandler(srv *mcpServer, token string, logf func(string, ...any)) http.Handler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bare := strings.TrimRight(r.URL.Path, "/")
		if bare == "" {
			bare = "/"
		}
		send := func(status int, body any, extra map[string]string) {
			headers := map[string]string{"Cache-Control": "no-store"}
			var text string
			if body != nil {
				b, err := json.Marshal(body)
				if err != nil {
					status, text = http.StatusInternalServerError, `{"error":"encode failed"}`
				} else {
					text = string(b)
				}
				headers["Content-Type"] = "application/json"
			}
			for k, v := range extra {
				headers[k] = v
			}
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, text)
		}

		if bare != "/mcp" {
			send(http.StatusNotFound, map[string]any{"error": "not found", "hint": "MCP 端点是 POST /mcp"}, nil)
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// 只允许 POST：GET（SSE 流）和 DELETE（结束会话）我们都不支持，按规范回 405
		if r.Method != http.MethodPost {
			send(http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed", "allow": "POST"},
				map[string]string{"Allow": "POST"})
			return
		}
		auth := r.Header.Get("Authorization")
		given := ""
		if strings.HasPrefix(auth, "Bearer ") {
			given = strings.TrimSpace(auth[len("Bearer "):])
		}
		if given == "" || !tokenEquals(given, token) {
			send(http.StatusUnauthorized, map[string]any{
				"error": "unauthorized",
				"hint":  "需要 Authorization: Bearer <token>（token 见程序配置目录下的 mcp.json）",
			}, map[string]string{"WWW-Authenticate": "Bearer"})
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMcpBodyBytes))
		if err != nil {
			send(http.StatusRequestEntityTooLarge, map[string]any{"error": "payload too large"}, nil)
			return
		}
		if len(body) == 0 {
			body = []byte("null")
		}

		var probe any
		if err := json.Unmarshal(body, &probe); err != nil {
			send(http.StatusOK, rpcFail(nil, -32700, "JSON 解析失败"), nil)
			return
		}
		if _, isArr := probe.([]any); isArr {
			send(http.StatusOK, rpcFail(nil, -32600, "不支持批量请求（JSON-RPC batch 已从 MCP 规范移除）"), nil)
			return
		}
		if _, isObj := probe.(map[string]any); !isObj {
			send(http.StatusOK, rpcFail(nil, -32600, "无效请求"), nil)
			return
		}
		var msg jsonRPCRequest
		_ = json.Unmarshal(body, &msg)

		// ── subscriptions/listen：单请求上的长连接 SSE（2026-07-28 用它取代 GET 流）──
		// 先 ack，再把作业状态变化推成 notifications/tasks；客户端断开即结束。
		if msg.Method == "subscriptions/listen" {
			flusher, ok := w.(http.Flusher)
			if !ok {
				send(http.StatusInternalServerError, map[string]any{"error": "SSE 不可用"}, nil)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			writeSSE := func(payload any) {
				b, _ := json.Marshal(payload)
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				flusher.Flush()
			}
			writeSSE(map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/subscriptions/acknowledged",
				"params": map[string]any{
					"subscriptionId": fmt.Sprintf("sub-%d", time.Now().UnixNano()%1_000_000),
					"filters":        map[string]any{"tasks": true},
				},
			})
			ch, unsub := subscribeJobEvents()
			defer unsub()
			ticker := time.NewTicker(25 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					// SSE 注释行保活（客户端会忽略）
					fmt.Fprint(w, ": keep-alive\n\n")
					flusher.Flush()
				case e, ok := <-ch:
					if !ok {
						return
					}
					writeSSE(map[string]any{
						"jsonrpc": "2.0",
						"method":  "notifications/tasks",
						"params": map[string]any{
							"taskId":   e.JobID,
							"status":   e.Status,
							"exitCode": e.ExitCode,
							"reason":   e.Reason,
						},
					})
				}
			}
		}

		headerVersion := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
		reply := srv.handle(&msg, headerVersion)
		if reply == nil {
			// 通知：按规范用 202 且不带 body
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// 现代请求的错误要落到对应状态码（版本不支持/头不一致 → 400，未知方法 → 404）；
		// legacy 一律 200 —— 老客户端会把 400 当"端点不存在"。
		status := http.StatusOK
		if reply.Error != nil {
			isModern := headerVersion != "" || readRequestMeta(msg.Params).Version != ""
			status = httpStatusForError(reply.Error.Code, isModern)
		}
		send(status, reply, nil)
	})
}

// tokenEquals 时间安全比较（不要用 == ：那样能从耗时上猜 token）
func tokenEquals(given, expected string) bool {
	if len(given) != len(expected) || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(expected)) == 1
}

// newMcpToken 生成一个新的访问令牌（32 字节十六进制）
func newMcpToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属于系统级异常；用时间戳兜底总比给个空 token 安全
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

// portInUse 端口上是不是已经有人在听。
//
// 为什么要**先探一次连接**而不是只等绑定失败：Windows 上两个进程绑同一个
// 127.0.0.1:port 不一定会报错，于是「第二个实例」会悄悄和第一个共用端口，
// 客户端连到哪个实例是不确定的 —— 而「AI 操作的是哪个窗口的会话」必须确定。
func portInUse(host string, port int) bool {
	if port == 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 400*time.Millisecond)
	if err != nil {
		return false // 超时/连不上都按「没占」处理：宁可去用固定端口
	}
	_ = conn.Close()
	return true
}

// mcpHTTPHandle 跑起来的 MCP 端点
type mcpHTTPHandle struct {
	url          string
	port         int
	token        string
	portFallback bool
	server       *http.Server
	listener     net.Listener
}

func (h *mcpHTTPHandle) Close() {
	if h == nil {
		return
	}
	if h.server != nil {
		_ = h.server.Close()
		return
	}
	if h.listener != nil {
		_ = h.listener.Close()
	}
}

// startMcpHTTP 起 MCP HTTP 端点。固定端口被占就退到随机端口（多实例时端口是共享资源）。
func startMcpHTTP(srv *mcpServer, token string, host string, port int, logf func(string, ...any)) (*mcpHTTPHandle, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	if logf == nil {
		logf = log.Printf
	}
	fallback := false
	if portInUse(host, port) {
		logf("MCP 端口 %d 已被占用，改用随机端口", port)
		port = 0
		fallback = true
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil && port != 0 {
		// 兜底：探测之后到绑定之间被抢（或探测判断错了）
		logf("MCP 端口 %d 绑定失败（%v），改用随机端口", port, err)
		fallback = true
		ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	if err != nil {
		return nil, err
	}
	actual := ln.Addr().(*net.TCPAddr).Port
	httpSrv := &http.Server{
		Handler:           mcpHTTPHandler(srv, token, logf),
		ReadHeaderTimeout: 10 * time.Second,
	}
	h := &mcpHTTPHandle{
		url:          fmt.Sprintf("http://%s:%d/mcp", host, actual),
		port:         actual,
		token:        token,
		portFallback: fallback,
		server:       httpSrv,
		listener:     ln,
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logf("MCP 端点已停止：%v", err)
		}
	}()
	return h, nil
}

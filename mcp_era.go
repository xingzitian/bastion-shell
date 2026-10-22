package main

// MCP「两个时代」（legacy initialize ↔ modern 2026-07-28）与 Tasks 扩展的**纯逻辑**。
//
// 与扩展侧 src/mcpEra.ts 一一对应：
//   - modern：版本/身份/能力在每个请求的 _meta；结果必须带 resultType；列表结果带 ttlMs+cacheScope；
//     服务端必须实现 server/discover；不支持的版本 -32022（HTTP 400）、头不一致 -32020（400）、未知方法 404。
//   - legacy：initialize 握手，结果不带 resultType；错误保持 200（老客户端会把 400 当"端点不存在"）。
//
// 单独放一个文件是为了能脱离 HTTP/SSH 直接测（见 mcp_era_test.go）。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const modernProtocolVersion = "2026-07-28"

var allProtocolVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const (
	tasksExtension = "io.modelcontextprotocol/tasks"

	metaVersion       = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo    = "io.modelcontextprotocol/clientInfo"
	metaClientCaps    = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo    = "io.modelcontextprotocol/serverInfo"
	metaLogLevel      = "io.modelcontextprotocol/logLevel"
	errUnsupported    = -32022
	errHeaderMismatch = -32020
	errMethodMissing  = -32601
	errInvalidParams  = -32602
)

const (
	listTTLMs  = 5 * 60 * 1000
	taskTTLMs  = 10 * 60 * 1000
	taskPollMs = 1000
)

// requestMeta 从请求 params._meta 里解出来的东西（缺失/类型不对都不报错）
type requestMeta struct {
	Version    string
	ClientName string
	ClientVer  string
	ClientCaps map[string]any
	LogLevel   string
}

func readRequestMeta(params json.RawMessage) requestMeta {
	if len(params) == 0 {
		return requestMeta{}
	}
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Meta == nil {
		return requestMeta{}
	}
	m := requestMeta{}
	if v, ok := p.Meta[metaVersion].(string); ok {
		m.Version = v
	}
	if ci, ok := p.Meta[metaClientInfo].(map[string]any); ok {
		if v, ok := ci["name"].(string); ok {
			m.ClientName = v
		}
		if v, ok := ci["version"].(string); ok {
			m.ClientVer = v
		}
	}
	if caps, ok := p.Meta[metaClientCaps].(map[string]any); ok {
		m.ClientCaps = caps
	}
	if v, ok := p.Meta[metaLogLevel].(string); ok {
		m.LogLevel = v
	}
	return m
}

// eraOf：带 _meta 版本 → modern；不带 → legacy（老客户端照旧能用）
func eraOf(m requestMeta) string {
	if m.Version != "" {
		return "modern"
	}
	return "legacy"
}

func isSupportedVersion(v string) bool {
	for _, s := range allProtocolVersions {
		if s == v {
			return true
		}
	}
	return false
}

// clientSupportsTasks：只看客户端有没有声明扩展，不绑协议代际
func clientSupportsTasks(m requestMeta) bool {
	if m.ClientCaps == nil {
		return false
	}
	ext, ok := m.ClientCaps["extensions"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = ext[tasksExtension]
	return ok
}

func unsupportedVersionError(requested string) map[string]any {
	return map[string]any{
		"code":    errUnsupported,
		"message": "Unsupported protocol version",
		"data":    map[string]any{"supported": allProtocolVersions, "requested": requested},
	}
}

func headerMismatchError(headerVersion, metaVersion string) map[string]any {
	return map[string]any{
		"code": errHeaderMismatch,
		"message": fmt.Sprintf("Header mismatch: MCP-Protocol-Version %q 与请求体 _meta 里的 %q 不一致",
			headerVersion, metaVersion),
	}
}

// httpStatusForError：只有 modern 请求才把错误映射成 400/404（legacy 一律 200）
func httpStatusForError(code int, modern bool) int {
	if !modern {
		return 200
	}
	switch code {
	case errUnsupported, errHeaderMismatch:
		return 400
	case errMethodMissing:
		return 404
	}
	return 200
}

// withResultType：modern 的结果必须带 resultType
func withResultType(result map[string]any, modern bool) map[string]any {
	if modern {
		result["resultType"] = "complete"
	}
	return result
}

// withCache：列表/发现类结果在 modern 下带 ttlMs + cacheScope（private：工具集可能按令牌不同）
func withCache(result map[string]any, modern bool, ttlMs int) map[string]any {
	if modern {
		result["ttlMs"] = ttlMs
		result["cacheScope"] = "private"
	}
	return result
}

// discoverResult：server/discover 的结果（规范要求服务端必须实现）
func discoverResult(serverName, serverVersion, instructions string) map[string]any {
	caps := map[string]any{
		"tools":      map[string]any{"listChanged": false},
		"extensions": map[string]any{tasksExtension: map[string]any{}},
	}
	return map[string]any{
		"resultType":        "complete",
		"supportedVersions": allProtocolVersions,
		"capabilities":      caps,
		"instructions":      instructions,
		"_meta":             map[string]any{metaServerInfo: map[string]any{"name": serverName, "version": serverVersion}},
		"ttlMs":             listTTLMs,
		"cacheScope":        "private",
	}
}

// toTaskStatus：我们的作业状态与 Tasks 的状态词完全一致（保留一层做显式约定）
func toTaskStatus(status string) string {
	switch status {
	case "input_required", "completed", "failed", "cancelled":
		return status
	}
	return "working"
}

// createTaskResult：resultType=task + taskId/status/ttlMs/pollIntervalMs
func createTaskResult(taskID, status string) map[string]any {
	return map[string]any{
		"resultType":     "task",
		"taskId":         taskID,
		"status":         toTaskStatus(status),
		"ttlMs":          taskTTLMs,
		"pollIntervalMs": taskPollMs,
	}
}

// taskSnapshot：tasks/get 的结果
func taskSnapshot(v jobView, prompt, hint string) map[string]any {
	status := toTaskStatus(string(v.Status))
	out := map[string]any{
		"resultType":     "complete",
		"taskId":         v.JobID,
		"status":         status,
		"ttlMs":          taskTTLMs,
		"pollIntervalMs": taskPollMs,
	}
	switch status {
	case "completed":
		out["result"] = map[string]any{
			"content":           []map[string]any{{"type": "text", "text": fmt.Sprintf("作业 %s 已完成（退出码 %d）", v.JobID, v.ExitCode)}},
			"structuredContent": taskStructured(v),
		}
	case "failed":
		msg := "任务执行失败"
		if v.Reason == "session_lost" {
			msg = "会话已关闭"
		}
		out["error"] = map[string]any{"code": -32603, "message": msg}
	case "input_required":
		msg := strings.TrimSpace(prompt + "\n" + hint)
		out["inputRequests"] = map[string]any{
			"prompt": map[string]any{
				"method": "elicitation/create",
				"params": map[string]any{
					"mode":            "form",
					"message":         msg,
					"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				},
			},
		}
	}
	return out
}

func taskStructured(v jobView) map[string]any {
	return map[string]any{
		"jobId":       v.JobID,
		"status":      string(v.Status),
		"exitCode":    v.ExitCode,
		"elapsedMs":   v.ElapsedMs,
		"cursor":      v.Cursor,
		"cursorMoved": v.CursorMoved,
		"lastLine":    v.LastLine,
		"markerSeen":  v.MarkerSeen,
		"reason":      v.Reason,
	}
}

// ── MRTR：requestState 编解码（与扩展侧 src/mrt.ts 一致）──

func encodeRequestState(jobID string) string {
	b, _ := json.Marshal(map[string]string{"jobId": jobID})
	return base64.StdEncoding.EncodeToString(b)
}

func decodeRequestState(raw string) string {
	if raw == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return ""
	}
	var v struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return ""
	}
	return v.JobID
}

// isSecretPrompt：密码/口令/动态码一律不代填
func isSecretPrompt(text string) bool {
	l := strings.ToLower(text)
	for _, k := range []string{"password", "passwd", "密码", "passphrase", "verification code", "动态码", "验证码", "otp"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// firstInputValue：从 inputResponses 里取第一个可用字符串（兼容 elicitation 的 content 形状）
func firstInputValue(responses any) string {
	m, ok := responses.(map[string]any)
	if !ok {
		return ""
	}
	for _, v := range m {
		switch t := v.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				return t
			}
		case map[string]any:
			if s, ok := t["content"].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
			if c, ok := t["content"].(map[string]any); ok {
				for _, cv := range c {
					if s, ok := cv.(string); ok && strings.TrimSpace(s) != "" {
						return s
					}
				}
			}
		}
	}
	return ""
}

// taskExpired：任务超过 TTL 就当作过期（客户端拿到后应重新发起）
func taskExpired(startedAt time.Time, now time.Time) bool {
	return now.Sub(startedAt) > taskTTLMs*time.Millisecond
}

// parseIntArg 把 JSON 数字/字符串统一成 int64（MCP 客户端有时把数字写成字符串）
func parseIntArg(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	}
	return 0
}

// clientSupportsElicitation 客户端有没有声明 elicitation 能力。
//
// 没声明却给 resultType:"input_required"，客户端会抛 "no handler for elicitation/create"，
// 作业就卡在 input_required 占着会话。没声明时退化成普通结果（status 仍是 input_required）。
func clientSupportsElicitation(m requestMeta) bool {
	if m.ClientCaps == nil {
		return false
	}
	_, ok := m.ClientCaps["elicitation"]
	return ok
}

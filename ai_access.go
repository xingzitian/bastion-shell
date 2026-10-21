package main

// 桌面版的「AI 访问」页（与扩展侧的 AI 访问树/总览**同一份语义**）。
//
// 现状差异（必须如实显示在 UI 上，别让人以为两边一样）：
//   - 扩展侧：每个 AI 可以签一把独立令牌 + 权限分档（只读/可写/全权）。
//   - 桌面版：**单令牌**（主令牌 = 全权）。所以这一页的重点是"谁绑在哪条会话上"，
//     以及把端点/token 一键复制给 AI 客户端。
//
// 绑定文件与扩展**共用**（~/.bastionshell/mcp-bindings.json），所以在这里能看到
// 扩展侧建的绑定（标成"扩展侧创建"），也能在这里解绑。

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ── 与扩展侧 aiAccess.ts 同一套"人话"（改文案时两边一起改）──

type scopeInfo struct {
	Label  string
	Plain  string
	Can    []string
	Cannot []string
}

var desktopScopeInfo = map[string]scopeInfo{
	"observer": {
		Label:  "只读",
		Plain:  "只能看，不能动",
		Can:    []string{"列会话", "读屏幕（tail）", "端点自检", "绑定会话"},
		Cannot: []string{"执行命令", "上传/下载文件", "连新机器"},
	},
	"operator": {
		Label:  "可写",
		Plain:  "能干活，但不能自己开新机器",
		Can:    []string{"列会话 / 读屏幕", "执行命令", "上传/下载文件"},
		Cannot: []string{"连新机器"},
	},
	"full": {
		Label:  "全权",
		Plain:  "什么都能做",
		Can:    []string{"执行命令", "上传/下载文件", "连新机器", "写个人习惯"},
		Cannot: nil,
	},
}

func scopePlain(scope string) (label, plain string, can, cannot []string) {
	s, ok := desktopScopeInfo[scope]
	if !ok {
		s = desktopScopeInfo["full"]
	}
	return s.Label, s.Plain, s.Can, s.Cannot
}

type aiBindingView struct {
	Alias     string `json:"alias"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

type aiClientView struct {
	ClientID   string          `json:"clientId"`
	Label      string          `json:"label"`
	Scope      string          `json:"scope"`
	ScopeLabel string          `json:"scopeLabel"`
	Plain      string          `json:"plain"`
	Can        []string        `json:"can"`
	Cannot     []string        `json:"cannot"`
	IsMain     bool            `json:"isMain"`
	External   bool            `json:"external"` // 扩展侧创建的客户端
	Bindings   []aiBindingView `json:"bindings"`
}

type aiOwnerView struct {
	ClientID string `json:"clientId"`
	Label    string `json:"label"`
	Alias    string `json:"alias"`
}

type aiSessionView struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Owners []aiOwnerView `json:"owners"`
}

type aiAccessView struct {
	// 桌面版单令牌：给 UI 显示掩码 + 复制配置用
	Endpoint     string          `json:"endpoint"`
	TokenMasked  string          `json:"tokenMasked"`
	Token        string          `json:"token"`
	MainOnly     bool            `json:"mainOnly"`
	Clients      []aiClientView  `json:"clients"`
	Sessions     []aiSessionView `json:"sessions"`
	BindingsFile string          `json:"bindingsFile"`
	Note         string          `json:"note"`
}

func maskToken(t string) string {
	s := strings.TrimSpace(t)
	if len(s) <= 8 {
		return "••••"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

/**
 * buildAiAccessView 汇总页面要的东西。
 *
 * clients 里：
 *   - `default` = 桌面版的**主令牌**（full）—— 桌面版只有一个令牌，所以绑定都挂在它名下；
 *   - 其它 clientId 说明是**扩展侧**用请求头/独立令牌创建的绑定：能看、能解绑，但权限档在扩展侧管。
 */
func buildAiAccessView() aiAccessView {
	doc := loadBindings()
	refs, _ := sessionRefsNow()

	live := make(map[string]bool, len(refs))
	for _, r := range refs {
		live[r.SID] = true
	}
	if cleanupBindings(doc, live) {
		saveBindings(doc)
	}

	url, _, _, ok := mcpEndpointInfo()
	if !ok {
		url = ""
	}
	cfg := loadOrCreateMcpConfig()
	endpoint := url
	if endpoint == "" {
		endpoint = "http://127.0.0.1:" + strconv.Itoa(cfg.Port) + "/mcp"
	}

	// 先建会话行（owners 稍后填）
	byID := make(map[string]*aiSessionView, len(refs))
	sessions := make([]aiSessionView, 0, len(refs))
	for _, r := range refs {
		row := aiSessionView{ID: r.SID, Name: r.Name, Owners: []aiOwnerView{}}
		sessions = append(sessions, row)
		byID[r.SID] = &sessions[len(sessions)-1]
	}

	clients := make([]aiClientView, 0, len(doc.Clients)+1)
	// 主令牌
	mainLabel, mainPlain, mainCan, mainCannot := scopePlain("full")
	main := aiClientView{
		ClientID:   "default",
		Label:      "主令牌（桌面版只有一个令牌）",
		Scope:      "full",
		ScopeLabel: mainLabel,
		Plain:      mainPlain,
		Can:        mainCan,
		Cannot:     mainCannot,
		IsMain:     true,
		Bindings:   []aiBindingView{},
	}
	if cb, ok := doc.Clients["default"]; ok {
		for _, a := range sortedAliases(cb.Sessions) {
			b := cb.Sessions[a]
			main.Bindings = append(main.Bindings, aiBindingView{Alias: a, SessionID: b.SessionID, Name: b.Name})
			if row, ok := byID[b.SessionID]; ok {
				row.Owners = append(row.Owners, aiOwnerView{ClientID: "default", Label: "主令牌", Alias: a})
			}
		}
	}
	clients = append(clients, main)

	// 其它客户端（一般是扩展侧建的；桌面版自己不会创建）
	for cid, cb := range doc.Clients {
		if cid == "default" {
			continue
		}
		label := cb.Label
		if label == "" {
			label = cid
		}
		v := aiClientView{
			ClientID:   cid,
			Label:      label,
			Scope:      "unknown",
			ScopeLabel: "（权限在扩展侧）",
			Plain: "这个客户端不是桌面版签发的：它可能用的是**主令牌（全权）**，也可能是扩展侧签发的受限令牌。" +
				"权限以扩展侧的「AI 访问」为准；桌面版这里只显示它的绑定。",
			Can:      nil,
			Cannot:   nil,
			External: true,
			Bindings: []aiBindingView{},
		}
		for _, a := range sortedAliases(cb.Sessions) {
			b := cb.Sessions[a]
			v.Bindings = append(v.Bindings, aiBindingView{Alias: a, SessionID: b.SessionID, Name: b.Name})
			if row, ok := byID[b.SessionID]; ok {
				row.Owners = append(row.Owners, aiOwnerView{ClientID: cid, Label: label, Alias: a})
			}
		}
		clients = append(clients, v)
	}

	return aiAccessView{
		Endpoint:     endpoint,
		TokenMasked:  maskToken(cfg.Token),
		Token:        cfg.Token,
		MainOnly:     len(doc.Clients) <= 1,
		Clients:      clients,
		Sessions:     sessions,
		BindingsFile: bindingsPath(),
		Note: "桌面版是**单令牌**（全权）：不像 VS Code 扩展那样能给每个 AI 签不同权限的令牌。" +
			"要限制某个 AI 只能看、不能执行命令，请用扩展侧的「AI 访问」。绑定文件两边共用，所以在这里能看到、也能解绑。",
	}
}

func sortedAliases(m map[string]bindingEntry) []string {
	out := make([]string, 0, len(m))
	for a := range m {
		out = append(out, a)
	}
	// 别名是 A..H，直接按字符串排序即可
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// registerAiAccessRoutes 页面数据 + 解绑
func registerAiAccessRoutes(mux *http.ServeMux) {
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/ai-access", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, buildAiAccessView())
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
	mux.HandleFunc("/api/ai-access/unbind", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			ClientID  string `json:"clientId"`
			Alias     string `json:"alias"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ClientID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要 clientId"})
			return
		}
		doc := loadBindings()
		ok := unbindSession(doc, req.ClientID, req.Alias, req.SessionID)
		if ok {
			saveBindings(doc)
			log.Printf("AI 访问页：已解绑 %s 的 %s", req.ClientID, firstNonEmpty(req.Alias, req.SessionID))
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "at": time.Now().Format(time.RFC3339)})
	})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

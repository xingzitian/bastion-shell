package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 共享配置：**转发规则** 与 **快捷命令**。
//
// 和连接档案（profile.go）同一套做法与同一条纪律：
//   - 文件放在 `~/.bastionshell/`，**两个客户端读同一份**（扩展侧本来就用这两个文件名）；
//   - 内部用 []map[string]any 保留**对方写入的、我们不认识的字段** ——
//     拿本方结构体整体重写就是数据丢失（扩展侧的转发规则有 label/profileId，
//     快捷命令有 description/sendEnter/file，桌面版结构体里都没有）；
//   - 老文件（%AppData%\bastionshell\forwards.json）迁进来，**不删原文件**；
//   - 这里**不涉及凭据**，一个密码字段都不该出现。

// forwardRulesSharedFile 共享转发规则文件名（与扩展侧同一个值）
const forwardRulesSharedFile = "forwardRules.jsonc"

// quickCommandsSharedFile 共享快捷命令文件名（与扩展侧同一个值）
const quickCommandsSharedFile = "quickCommands.jsonc"

const forwardRulesHeader = "// ============================================================\n" +
	"// BastionShell 端口转发规则（**桌面版与 VS Code 扩展共享这一份**）\n" +
	"// 直接编辑本文件并保存即可生效（刷新界面后读取）。\n" +
	"// ------------------------------------------------------------\n" +
	"//   id          规则 id（唯一）\n" +
	"//   profileId   所属档案名（对应 profiles.jsonc 里的 name；桌面版可留空）\n" +
	"//   label       给人看的名字\n" +
	"//   localHost   本地监听地址，默认 127.0.0.1\n" +
	"//   localPort   本地监听端口\n" +
	"//   remoteHost  目标地址（从目标机视角）\n" +
	"//   remotePort  目标端口\n" +
	"// ============================================================"

const quickCommandsHeader = "// ============================================================\n" +
	"// BastionShell 快捷命令（**桌面版与 VS Code 扩展共享这一份**）\n" +
	"// 直接编辑本文件并保存即可生效（刷新界面后读取）。\n" +
	"// ------------------------------------------------------------\n" +
	"//   id       命令 id（唯一）\n" +
	"//   label    按钮上显示的名字\n" +
	"//   command  要发到会话里的命令（扩展侧还支持数组/file/description/sendEnter，这里原样保留）\n" +
	"// ============================================================"

// ───────────────────────── 转发规则 ─────────────────────────

// forwardStore 转发规则持久化（共享文件；运行时状态在前端）
type forwardStore struct {
	mu      sync.Mutex
	path    string
	entries []map[string]any
}

func newForwardStore() *forwardStore {
	return &forwardStore{path: sharedFilePath(forwardRulesSharedFile)}
}

// legacyForwardsPath 桌面版自己的老转发规则文件（迁移来源；不删）
func legacyForwardsPath() string {
	if v, ok := os.LookupEnv("BASTIONSHELL_LEGACY_FORWARDS"); ok {
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "bastionshell", "forwards.json")
}

func (s *forwardStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, found, err := readJSONCText(s.path)
	if err == nil && found {
		var arr []map[string]any
		if err := parseJSONC(text, &arr); err != nil {
			log.Printf("共享转发规则文件解析失败（本次按空列表处理，原文件未动）：%v", err)
			return
		}
		s.entries = arr
		return
	}
	s.entries = nil
	s.migrateLegacyLocked()
}

// migrateLegacyLocked 把桌面版老的 forwards.json 迁进共享文件（只补不覆盖、不删原文件）
func (s *forwardStore) migrateLegacyLocked() {
	legacy := legacyForwardsPath()
	if legacy == "" {
		return
	}
	text, found, err := readJSONCText(legacy)
	if err != nil || !found {
		return
	}
	var wrap struct {
		Forwards []map[string]any `json:"forwards"`
	}
	if err := parseJSONC(text, &wrap); err != nil || len(wrap.Forwards) == 0 {
		return
	}
	existing := map[string]bool{}
	for _, e := range s.entries {
		if id, _ := e["id"].(string); id != "" {
			existing[id] = true
		}
	}
	migrated := 0
	for _, old := range wrap.Forwards {
		id, _ := old["id"].(string)
		if strings.TrimSpace(id) == "" || existing[id] {
			continue
		}
		entry := map[string]any{}
		for k, v := range old {
			entry[k] = v
		}
		if _, ok := entry["localHost"]; !ok {
			entry["localHost"] = "127.0.0.1"
		}
		if _, ok := entry["label"]; !ok {
			entry["label"] = fmt.Sprintf("%v → %v:%v", entry["localPort"], entry["remoteHost"], entry["remotePort"])
		}
		if _, ok := entry["profileId"]; !ok {
			entry["profileId"] = ""
		}
		s.entries = append(s.entries, entry)
		existing[id] = true
		migrated++
	}
	if migrated == 0 {
		return
	}
	if err := s.saveLocked(); err != nil {
		log.Printf("迁移桌面版老转发规则失败：%v", err)
		return
	}
	log.Printf("已把 %d 条转发规则从 %s 迁入共享文件 %s（原文件保留未动）", migrated, legacy, s.path)
}

func (s *forwardStore) snapshot() []forwardRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]forwardRule, 0, len(s.entries))
	for _, e := range s.entries {
		str := func(k string) string {
			v, _ := e[k].(string)
			return strings.TrimSpace(v)
		}
		num := func(k string) int {
			switch v := e[k].(type) {
			case float64:
				return int(v)
			case int:
				return v
			}
			return 0
		}
		r := forwardRule{
			ID:         str("id"),
			LocalHost:  str("localHost"),
			LocalPort:  num("localPort"),
			RemoteHost: str("remoteHost"),
			RemotePort: num("remotePort"),
			ProfileID:  str("profileId"),
			Label:      str("label"),
		}
		if r.LocalHost == "" {
			r.LocalHost = "127.0.0.1"
		}
		if r.ID == "" && r.LocalPort == 0 {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (s *forwardStore) upsert(r forwardRule) error {
	if strings.TrimSpace(r.ID) == "" || r.LocalPort <= 0 || r.RemotePort <= 0 {
		return fmt.Errorf("id/localPort/remotePort 必填")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := map[string]any{
		"id":         r.ID,
		"localHost":  orDefault(r.LocalHost, "127.0.0.1"),
		"localPort":  r.LocalPort,
		"remoteHost": r.RemoteHost,
		"remotePort": r.RemotePort,
	}
	// ⚠️ 可选字段（profileId / label）**空值不覆盖**：
	// 扩展侧可能给这条规则记了档案名和名字，桌面版这次没带，就不能把它抹成空
	// （这是"不丢对方数据"的一部分；测试 TestForwardStorePreservesExtensionFields 盯着它）。
	optional := map[string]string{}
	if v := strings.TrimSpace(r.ProfileID); v != "" {
		optional["profileId"] = v
	}
	if v := strings.TrimSpace(r.Label); v != "" {
		optional["label"] = v
	}
	for i := range s.entries {
		if id, _ := s.entries[i]["id"].(string); id == r.ID {
			for k, v := range fields {
				s.entries[i][k] = v
			}
			for k, v := range optional {
				s.entries[i][k] = v
			}
			return s.saveLocked()
		}
	}
	entry := map[string]any{}
	for k, v := range fields {
		entry[k] = v
	}
	for k, v := range optional {
		entry[k] = v
	}
	// 新规则：profileId 至少要是空串（扩展侧的规则校验要求它是字符串），label 自动补一个
	if _, ok := entry["profileId"]; !ok {
		entry["profileId"] = ""
	}
	if _, ok := entry["label"]; !ok {
		entry["label"] = fmt.Sprintf("%d → %s:%d", r.LocalPort, r.RemoteHost, r.RemotePort)
	}
	s.entries = append(s.entries, entry)
	return s.saveLocked()
}

func (s *forwardStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.entries))
	for _, e := range s.entries {
		if got, _ := e["id"].(string); got != id {
			out = append(out, e)
		}
	}
	s.entries = out
	return s.saveLocked()
}

func (s *forwardStore) saveLocked() error {
	return saveSharedArray(s.path, forwardRulesHeader, s.entries)
}

// ───────────────────────── 快捷命令 ─────────────────────────

// quickCommand 快捷命令（桌面版认识的字段；其它字段原样保留在文件里）
type quickCommand struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`
}

type quickCommandStore struct {
	mu      sync.Mutex
	path    string
	entries []map[string]any
}

func newQuickCommandStore() *quickCommandStore {
	return &quickCommandStore{path: sharedFilePath(quickCommandsSharedFile)}
}

func (s *quickCommandStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, found, err := readJSONCText(s.path)
	if err == nil && found {
		var arr []map[string]any
		if err := parseJSONC(text, &arr); err != nil {
			log.Printf("共享快捷命令文件解析失败（本次按空列表处理，原文件未动）：%v", err)
			return
		}
		s.entries = arr
		return
	}
	s.entries = nil
}

func (s *quickCommandStore) snapshot() []quickCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]quickCommand, 0, len(s.entries))
	for _, e := range s.entries {
		str := func(k string) string {
			v, _ := e[k].(string)
			return strings.TrimSpace(v)
		}
		// 扩展侧的 command 允许是数组（多行命令）—— 这里取首行做展示，
		// 完整内容仍原样留在文件里，不会被桌面版抹掉。
		cmd := str("command")
		if cmd == "" {
			if arr, ok := e["command"].([]any); ok && len(arr) > 0 {
				if s0, ok := arr[0].(string); ok {
					cmd = s0
				}
			}
		}
		c := quickCommand{ID: str("id"), Label: str("label"), Command: cmd}
		if c.ID == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (s *quickCommandStore) upsert(c quickCommand) error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("id 必填")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := map[string]any{
		"id":      c.ID,
		"label":   c.Label,
		"command": c.Command,
	}
	for i := range s.entries {
		if id, _ := s.entries[i]["id"].(string); id == c.ID {
			for k, v := range fields {
				s.entries[i][k] = v
			}
			return s.saveLocked()
		}
	}
	entry := map[string]any{}
	for k, v := range fields {
		entry[k] = v
	}
	s.entries = append(s.entries, entry)
	return s.saveLocked()
}

func (s *quickCommandStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.entries))
	for _, e := range s.entries {
		if got, _ := e["id"].(string); got != id {
			out = append(out, e)
		}
	}
	s.entries = out
	return s.saveLocked()
}

func (s *quickCommandStore) saveLocked() error {
	return saveSharedArray(s.path, quickCommandsHeader, s.entries)
}

// ───────────────────────── 公共写盘 ─────────────────────────

// saveSharedArray 头注释 + JSON 数组（与扩展侧 writeJsonFile 的行为一致）
func saveSharedArray(path, header string, entries []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if entries == nil {
		entries = []map[string]any{}
	}
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return writeJSONCTextAtomic(path, header+"\n"+string(body)+"\n")
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// registerQuickCommandRoutes 快捷命令的 REST（前端 shim 调这里，落的还是共享文件）
func registerQuickCommandRoutes(mux *http.ServeMux) {
	store := newQuickCommandStore()
	store.load()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/quick-commands", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, store.snapshot())
		case http.MethodPost:
			var c quickCommand
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			if err := store.upsert(c); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		case http.MethodDelete:
			if err := store.remove(r.URL.Query().Get("id")); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
}

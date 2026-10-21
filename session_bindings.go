package main

// 会话身份与绑定（P0a）—— 与 VS Code 扩展**同一份契约**（docs/session-identity.md）。
//
// 为什么桌面版也要有：没有它就退回到"没指定会话就用最近活动的那条"，
// 而那正是"命令打到错机器上"的根源。两个实现读同一个 ~/.bastionshell/mcp-bindings.json，
// 所以用户在扩展里绑的 A/B，桌面版这边也认。

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	bindingsFile    = "mcp-bindings.json"
	maxAliases      = 8
	bindingTTL      = 12 * time.Hour
	bindingsVersion = 1
)

// sessionRef 解析会话时用到的最小信息
type sessionRef struct {
	SID    string
	Name   string
	State  string
	Cursor int64
}

type bindingEntry struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Since     string `json:"since"`
}

type clientBindings struct {
	Label     string                  `json:"label"`
	UpdatedAt string                  `json:"updatedAt"`
	Sessions  map[string]bindingEntry `json:"sessions"`
}

type bindingsDoc struct {
	Version int                       `json:"version"`
	Clients map[string]clientBindings `json:"clients"`

	mu sync.Mutex `json:"-"`
}

func bindingsPath() string {
	return sharedFilePath(bindingsFile)
}

// normalizeClientID 与扩展侧同一套规则（小写、只留安全字符、空→default）
func normalizeClientID(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	v := b.String()
	if len(v) > 32 {
		v = v[:32]
	}
	if v == "" {
		return "default"
	}
	return v
}

func loadBindings() *bindingsDoc {
	doc := &bindingsDoc{Version: bindingsVersion, Clients: map[string]clientBindings{}}
	text, found, err := readJSONCText(bindingsPath())
	if err != nil || !found {
		return doc
	}
	if err := parseJSONC(text, doc); err != nil {
		// 文件坏了：不覆盖（用户的东西不动），本次按空绑定处理
		log.Printf("绑定文件解析失败（本次按空绑定处理，原文件未动）：%v", err)
		return &bindingsDoc{Version: bindingsVersion, Clients: map[string]clientBindings{}}
	}
	if doc.Clients == nil {
		doc.Clients = map[string]clientBindings{}
	}
	return doc
}

func saveBindings(doc *bindingsDoc) {
	if doc.Clients == nil {
		doc.Clients = map[string]clientBindings{}
	}
	doc.Version = bindingsVersion
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		log.Printf("保存绑定失败：%v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(bindingsPath()), 0o700); err != nil {
		log.Printf("保存绑定失败（建目录）：%v", err)
		return
	}
	if err := writeJSONCTextAtomic(bindingsPath(), bindingsHeader+"\n"+string(body)+"\n"); err != nil {
		log.Printf("保存绑定失败：%v", err)
	}
}

const bindingsHeader = `// BastionShell 的 AI 会话绑定（自动生成，别手改）
//
// 桌面版与 VS Code 扩展**共用这一份**：clients.<clientId>.sessions.<别名> = { sessionId, name, since }`

func (d *bindingsDoc) client(id string) clientBindings {
	id = normalizeClientID(id)
	cb, ok := d.Clients[id]
	if !ok {
		cb = clientBindings{Label: id, Sessions: map[string]bindingEntry{}}
		if id == "default" {
			cb.Label = "未标识客户端"
		}
	}
	if cb.Sessions == nil {
		cb.Sessions = map[string]bindingEntry{}
	}
	return cb
}

func (d *bindingsDoc) put(id string, cb clientBindings) {
	id = normalizeClientID(id)
	d.Clients[id] = cb
}

// owners 某条会话被哪些客户端绑着
func (d *bindingsDoc) owners(sid string) []string {
	var out []string
	for id, cb := range d.Clients {
		for _, b := range cb.Sessions {
			if b.SessionID == sid {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func aliasOfSession(cb clientBindings, sid string) string {
	for alias, b := range cb.Sessions {
		if b.SessionID == sid {
			return alias
		}
	}
	return ""
}

func bindingByAlias(cb clientBindings, alias string) (bindingEntry, bool) {
	want := strings.ToLower(strings.TrimSpace(alias))
	for a, b := range cb.Sessions {
		if strings.ToLower(a) == want {
			return b, true
		}
	}
	return bindingEntry{}, false
}

func nextAlias(cb clientBindings) string {
	used := map[string]bool{}
	for a := range cb.Sessions {
		used[strings.ToUpper(a)] = true
	}
	for i := 0; i < maxAliases; i++ {
		a := string(rune('A' + i))
		if !used[a] {
			return a
		}
	}
	return ""
}

// bindSession 绑定一条会话（同名会话已绑过就幂等返回旧别名）
func bindSession(doc *bindingsDoc, clientID string, ref sessionRef, as string) (alias string, existing bool, errMsg string) {
	cb := doc.client(clientID)
	if old := aliasOfSession(cb, ref.SID); old != "" {
		return old, true, ""
	}
	a := strings.TrimSpace(as)
	if a != "" {
		if len(a) > 16 {
			return "", false, fmt.Sprintf("别名最长 16 个字符（收到 %q）", a)
		}
		if b, ok := cb.Sessions[a]; ok && b.SessionID != ref.SID {
			return "", false, fmt.Sprintf("别名 %s 已经指向 %s 了；换一个别名，或先解绑", a, b.Name)
		}
	} else {
		a = nextAlias(cb)
		if a == "" {
			return "", false, fmt.Sprintf("一个客户端最多绑定 %d 条会话（A..H）。先解绑不用的", maxAliases)
		}
	}
	cb.Sessions[a] = bindingEntry{SessionID: ref.SID, Name: ref.Name, Since: time.Now().Format(time.RFC3339)}
	cb.UpdatedAt = time.Now().Format(time.RFC3339)
	doc.put(clientID, cb)
	return a, false, ""
}

func unbindSession(doc *bindingsDoc, clientID, alias, sid string) bool {
	id := normalizeClientID(clientID)
	cb, ok := doc.Clients[id]
	if !ok {
		return false
	}
	removed := false
	for a, b := range cb.Sessions {
		if (alias != "" && strings.EqualFold(a, alias)) || (sid != "" && b.SessionID == sid) {
			delete(cb.Sessions, a)
			removed = true
		}
	}
	if removed {
		cb.UpdatedAt = time.Now().Format(time.RFC3339)
		doc.Clients[id] = cb
	}
	return removed
}

// cleanupBindings 丢掉"会话没了"和"超 12 小时没动"的绑定；返回是否有改动
func cleanupBindings(doc *bindingsDoc, live map[string]bool) bool {
	changed := false
	now := time.Now()
	for id, cb := range doc.Clients {
		for a, b := range cb.Sessions {
			stale := !live[b.SessionID]
			if !stale && b.Since != "" {
				if t, err := time.Parse(time.RFC3339, b.Since); err == nil && now.Sub(t) > bindingTTL {
					stale = true
				}
			}
			if stale {
				delete(cb.Sessions, a)
				changed = true
			}
		}
		doc.Clients[id] = cb
	}
	return changed
}

func describeBindings(doc *bindingsDoc, clientID string) string {
	cb := doc.client(clientID)
	if len(cb.Sessions) == 0 {
		return "当前客户端还没有绑定任何会话（写入类工具需要先 bastion_use 绑定）。"
	}
	aliases := make([]string, 0, len(cb.Sessions))
	for a := range cb.Sessions {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	parts := make([]string, 0, len(aliases))
	for _, a := range aliases {
		b := cb.Sessions[a]
		parts = append(parts, fmt.Sprintf("%s=%s（%s）", a, b.SessionID, b.Name))
	}
	return fmt.Sprintf("当前客户端（%s）绑定：%s", normalizeClientID(clientID), strings.Join(parts, "、"))
}

// ───────────────── 解析「用哪条会话」 ─────────────────

func fmtRef(r sessionRef) string { return fmt.Sprintf("%s（%s）", r.SID, r.Name) }

func listCandidates(list []sessionRef) string {
	parts := make([]string, 0, len(list))
	for _, r := range list {
		parts = append(parts, fmt.Sprintf("%s[%s]", fmtRef(r), r.State))
	}
	return strings.Join(parts, "、")
}

// expectCursorProblem 与扩展侧同一条规则（桌面版目前只占位，写入暂不校验）
func expectCursorProblem(expected *int64, actual int64) string {
	if expected == nil || *expected == actual {
		return ""
	}
	return fmt.Sprintf(
		"会话在你上次读取之后被改动过（cursor %d → %d）：可能是用户在那个终端里敲了东西，或者另一个客户端执行了命令。"+
			"先 bastion_tail 看一眼确认，再重试。", *expected, actual)
}

// resolveSessionRef 解析会话选择（契约 §2/§3）：
// id 精确 > 完整名精确 > 别名 > 唯一片段；歧义/未命中一律拒绝；没传时：唯一绑定 > 拒绝。
func resolveSessionRef(list []sessionRef, sel, mode, clientID string, doc *bindingsDoc, fallback *sessionRef, allowWriteFallback bool) (sessionRef, string) {
	cb := doc.client(clientID)
	want := strings.TrimSpace(sel)

	if want == "" {
		bound := make([]bindingEntry, 0, len(cb.Sessions))
		for _, b := range cb.Sessions {
			bound = append(bound, b)
		}
		if len(bound) == 1 {
			for _, r := range list {
				if r.SID == bound[0].SessionID {
					return r, ""
				}
			}
			return sessionRef{}, fmt.Sprintf("绑定指向的会话 %s（%s）已经不在了（可能被关闭）。先 bastion_listSessions 重新选一条，再 bastion_use。",
				bound[0].SessionID, bound[0].Name)
		}
		if len(bound) > 1 {
			aliases := make([]string, 0, len(cb.Sessions))
			for a := range cb.Sessions {
				aliases = append(aliases, a)
			}
			sort.Strings(aliases)
			parts := make([]string, 0, len(aliases))
			for _, a := range aliases {
				b := cb.Sessions[a]
				parts = append(parts, fmt.Sprintf("%s=%s（%s）", a, b.SessionID, b.Name))
			}
			return sessionRef{}, fmt.Sprintf("你现在绑定了 %d 条会话：%s。请用 session 参数指定要操作哪一条 —— 这里不会替你猜。",
				len(bound), strings.Join(parts, "、"))
		}
		if (mode == "read" || allowWriteFallback) && fallback != nil {
			return *fallback, ""
		}
		if mode == "read" {
			return sessionRef{}, "现在没有任何会话。请让用户先在 BastionShell 里连接一台机器。"
		}
		return sessionRef{}, "你还没有绑定任何会话（这是为了避免把命令发到别的 AI 或用户的会话上）。先 bastion_listSessions，再 bastion_use({session: \"s-xxxx\"})。"
	}

	for _, r := range list {
		if strings.EqualFold(r.SID, want) {
			return r, ""
		}
	}
	for _, r := range list {
		if r.Name == want {
			return r, ""
		}
	}
	if b, ok := bindingByAlias(cb, want); ok {
		for _, r := range list {
			if r.SID == b.SessionID {
				return r, ""
			}
		}
		return sessionRef{}, fmt.Sprintf("别名 %s 指向的会话 %s（%s）已经不在了。先 bastion_listSessions 重新选一条。", want, b.SessionID, b.Name)
	}
	var frag []sessionRef
	low := strings.ToLower(want)
	for _, r := range list {
		if strings.Contains(strings.ToLower(r.Name), low) {
			frag = append(frag, r)
		}
	}
	if len(frag) == 1 {
		return frag[0], ""
	}
	if len(frag) > 1 {
		return sessionRef{}, fmt.Sprintf("「%s」匹配到 %d 条会话：%s。请用 session id 指定要操作哪一条。", want, len(frag), listCandidates(frag))
	}
	if len(list) == 0 {
		return sessionRef{}, fmt.Sprintf("找不到会话「%s」—— 现在一条会话都没有。请让用户先在 BastionShell 里连接一台机器。", want)
	}
	return sessionRef{}, fmt.Sprintf("找不到会话「%s」。当前有：%s。先用 bastion_listSessions 看一遍，再用 session id 指定。", want, listCandidates(list))
}

// newSessionSID 不可变会话 id（s-xxxx，进程内不重复）
var (
	sidMu   sync.Mutex
	sidUsed = map[string]bool{}
)

func newSessionSID() string {
	sidMu.Lock()
	defer sidMu.Unlock()
	for i := 0; i < 1000; i++ {
		sid := fmt.Sprintf("s-%04x", rand.Intn(0x10000))
		if !sidUsed[sid] {
			sidUsed[sid] = true
			return sid
		}
	}
	sid := fmt.Sprintf("s-%04x", time.Now().UnixNano()&0xffff)
	sidUsed[sid] = true
	return sid
}

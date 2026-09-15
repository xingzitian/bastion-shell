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

// Profile 连接档案。
//
// ⚠️ 字段名**与 VS Code 扩展侧完全一致**（`username` / `authMethod` / `privateKeyPath` / `mode`）——
// 因为这份文件是**两个客户端共享**的（`~/.bastionshell/profiles.jsonc`），只能有一套词汇。
// 桌面版自己的老文件里叫 `user` / `key`，迁移时映射过来。
type Profile struct {
	// ID 只给前端当 React key（值就是 Name），**不写进共享文件** ——
	// 免得两个客户端往同一个文件里塞各自的私有字段。
	ID string `json:"id,omitempty"`
	// 档案名（唯一，AI 调用时用它）
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	// Username 登录用户名（扩展侧叫 username，别写成 user）
	Username string `json:"username"`
	// AuthMethod password=密码 / key=私钥。空 = password
	AuthMethod string `json:"authMethod,omitempty"`
	// PrivateKeyPath 私钥文件路径（仅 authMethod=key）
	PrivateKeyPath string `json:"privateKeyPath,omitempty"`
	// Mode direct=直接 SSH（进 shell）/ bastion=堡垒机（登录后过菜单选机）。
	// 桌面版目前不区分这两者（都是"连上之后你自己敲 IP"），但**字段要原样保留**，
	// 免得扩展里设成 direct 的档案被桌面版一次保存就改回 bastion。
	Mode string `json:"mode,omitempty"`
}

// profileStore 档案持久化：**读写与 VS Code 扩展共享的那份文件**。
//
// 为什么内部用 []map[string]any 而不是 []Profile：
// 共享文件里会有**对方写入的、我们不认识的字段**（扩展侧以后加字段是迟早的事）。
// 拿本方结构体整体重写，会把那些字段悄悄抹掉 —— 那是数据丢失，不是"同步"。
// 所以：读进来保留全部原始字段，只改我们认识的那几个。
type profileStore struct {
	mu      sync.Mutex
	path    string
	entries []map[string]any
}

// profileSharedFile 共享档案文件名（与扩展侧同一个值）
const profileSharedFile = "profiles.jsonc"

// profilesHeader 与扩展侧**同一段**说明 —— 两边写出来的文件长得一样，用户不用学两套。
const profilesHeader = "// ============================================================\n" +
	"// BastionShell 连接档案配置（**桌面版与 VS Code 扩展共享这一份**）\n" +
	"// 直接编辑本文件并保存即可生效（刷新界面后读取）。\n" +
	"// 密码不写在这里：两边的密码各自存在系统凭据库里，从不落到本文件。\n" +
	"// ------------------------------------------------------------\n" +
	"// 每个档案字段说明：\n" +
	"//   name           档案名（唯一，AI 调用时用它）\n" +
	"//   host           主机地址（堡垒机或普通服务器的 IP/域名）\n" +
	"//   port           端口，默认 22\n" +
	"//   username       登录用户名\n" +
	"//   authMethod     认证方式：password=密码 / key=私钥\n" +
	"//   privateKeyPath 私钥文件路径（仅 authMethod=key 时需要，可省略）\n" +
	"//   mode           连接模式：direct=直接 SSH；bastion=堡垒机（登录后过菜单选机）\n" +
	"// ------------------------------------------------------------\n" +
	"// 这个文件归**两个客户端共用**：在这一份里加档案，桌面版和 VS Code 扩展都会看到。\n" +
	"// ============================================================"

func newProfileStore() *profileStore {
	return &profileStore{path: sharedFilePath(profileSharedFile)}
}

// legacyProfilesPath 桌面版自己的老档案文件（迁移来源；迁移后**不删**）。
//
// BASTIONSHELL_LEGACY_PROFILES 可以改写它（测试用：免得测试跑到用户真实的档案文件上）；
// 显式设成空串 = 关掉迁移。
func legacyProfilesPath() string {
	if v, ok := os.LookupEnv("BASTIONSHELL_LEGACY_PROFILES"); ok {
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "bastionshell", "profiles.json")
}

// load 读共享档案。
//
// 共享文件不存在时：尝试把桌面版老文件迁进来（这样老用户升级后档案不会凭空消失）。
func (s *profileStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, found, err := readJSONCText(s.path)
	if err == nil && found {
		var arr []map[string]any
		if err := parseJSONC(text, &arr); err != nil {
			log.Printf("共享档案文件解析失败（本次按空列表处理，原文件未动）：%v", err)
			return
		}
		s.entries = arr
		return
	}
	s.entries = nil
	s.migrateLegacyLocked()
}

// migrateLegacyLocked 把桌面版的 %AppData%\bastionshell\profiles.json 迁进共享文件。
//
// 两条纪律：
//   - **不删原文件**（用户的东西不动，迁移出问题还能回去看）；
//   - **只补不覆盖**：共享文件里已有的同名档案一律不动（那份才是"共享真源"）。
func (s *profileStore) migrateLegacyLocked() {
	legacy := legacyProfilesPath()
	if legacy == "" {
		return
	}
	text, found, err := readJSONCText(legacy)
	if err != nil || !found {
		return
	}
	var wrap struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := parseJSONC(text, &wrap); err != nil || len(wrap.Profiles) == 0 {
		return
	}
	existing := map[string]bool{}
	for _, e := range s.entries {
		if name, _ := e["name"].(string); name != "" {
			existing[name] = true
		}
	}
	migrated := 0
	for _, old := range wrap.Profiles {
		name, _ := old["name"].(string)
		if strings.TrimSpace(name) == "" || existing[name] {
			continue
		}
		entry := map[string]any{
			"name": name,
			"host": old["host"],
			"port": old["port"],
		}
		if u, ok := old["user"].(string); ok {
			entry["username"] = u
		} else if u, ok := old["username"].(string); ok {
			entry["username"] = u
		}
		if k, ok := old["key"].(string); ok && k != "" {
			entry["privateKeyPath"] = k
			entry["authMethod"] = "key"
		} else if k, ok := old["privateKeyPath"].(string); ok && k != "" {
			entry["privateKeyPath"] = k
			entry["authMethod"] = "key"
		} else {
			entry["authMethod"] = "password"
		}
		// ⚠️ 老文件里的 `password` 字段**故意不迁**：共享文件里不放凭据。
		// 桌面版的密码在系统凭据库（DPAPI）里，按档案名取。
		s.entries = append(s.entries, entry)
		existing[name] = true
		migrated++
	}
	if migrated == 0 {
		return
	}
	if err := s.saveLocked(); err != nil {
		log.Printf("迁移桌面版老档案失败：%v", err)
		return
	}
	log.Printf("已把 %d 个档案从 %s 迁入共享文件 %s（原文件保留未动，密码不参与迁移）",
		migrated, legacy, s.path)
}

// snapshot 给接口层 / AI 用的视图
func (s *profileStore) snapshot() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, 0, len(s.entries))
	for _, e := range s.entries {
		p := profileFromEntry(e)
		if p.Name == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// profileFromEntry 原始字段 → Profile（只认我们认识的键，其余原样留在 entries 里）
func profileFromEntry(e map[string]any) Profile {
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
	name := str("name")
	p := Profile{
		ID:             name,
		Name:           name,
		Host:           str("host"),
		Port:           num("port"),
		Username:       str("username"),
		AuthMethod:     str("authMethod"),
		PrivateKeyPath: str("privateKeyPath"),
		Mode:           str("mode"),
	}
	if p.Port == 0 {
		p.Port = 22
	}
	if p.AuthMethod != "key" {
		p.AuthMethod = "password"
	}
	if p.Mode != "direct" {
		p.Mode = "bastion"
	}
	return p
}

// upsert 新增或更新一个档案。
//
// 更新时**只改我们认识的字段**，entry 里其它键（扩展侧写的）原样留着。
func (s *profileStore) upsert(p Profile) error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return fmt.Errorf("档案名不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := map[string]any{
		"name":     name,
		"host":     strings.TrimSpace(p.Host),
		"port":     p.Port,
		"username": strings.TrimSpace(p.Username),
	}
	if p.Port == 0 {
		fields["port"] = 22
	}
	if strings.TrimSpace(p.AuthMethod) == "key" {
		fields["authMethod"] = "key"
		if k := strings.TrimSpace(p.PrivateKeyPath); k != "" {
			fields["privateKeyPath"] = k
		}
	} else {
		fields["authMethod"] = "password"
		// 从 key 改回 password 时要把私钥路径清掉，否则残留一个不生效的路径
		delete2(s.entries, name, "privateKeyPath")
	}
	if m := strings.TrimSpace(p.Mode); m == "direct" || m == "bastion" {
		fields["mode"] = m
	}
	for i := range s.entries {
		if n, _ := s.entries[i]["name"].(string); n == name {
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

// delete2 删掉某个 entry 里的一个键（上面 upsert 里改认证方式时用）
func delete2(entries []map[string]any, name, key string) {
	for _, e := range entries {
		if n, _ := e["name"].(string); n == name {
			delete(e, key)
			return
		}
	}
}

func (s *profileStore) remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.entries))
	for _, e := range s.entries {
		if n, _ := e["name"].(string); n != name {
			out = append(out, e)
		}
	}
	s.entries = out
	return s.saveLocked()
}

// saveLocked 写出共享文件：头注释 + 档案数组。
//
// 数组内部的注释会被规范化掉（和扩展侧的 writeJsonFile 行为一致）；
// 头顶那段说明会保留。
func (s *profileStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if s.entries == nil {
		s.entries = []map[string]any{}
	}
	body, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	text := profilesHeader + "\n" + string(body) + "\n"
	return writeJSONCTextAtomic(s.path, text)
}

func registerForwardRoutes(mux *http.ServeMux, store *forwardStore) {
	store.load()
	writeJSON := func(w http.ResponseWriter, code int, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/forwards", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, store.snapshot())
		case http.MethodPost:
			var fw forwardRule
			if err := json.NewDecoder(r.Body).Decode(&fw); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			if fw.ID == "" || fw.LocalPort <= 0 || fw.RemotePort <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id/localPort/remotePort required"})
				return
			}
			if err := store.upsert(fw); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		case http.MethodDelete:
			id := r.URL.Query().Get("id")
			if err := store.remove(id); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
}

// registerProfileRoutes 挂档案 REST API（GET 列表 / POST 保存 / DELETE 删除）
func registerProfileRoutes(mux *http.ServeMux, store *profileStore) {
	store.load()
	writeJSON := func(w http.ResponseWriter, code int, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/profiles", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, store.snapshot())
		case http.MethodPost:
			var p Profile
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			if p.Name == "" || p.Host == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name/host required"})
				return
			}
			if err := store.upsert(p); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		case http.MethodDelete:
			name := r.URL.Query().Get("name")
			if err := store.remove(name); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
}

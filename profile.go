package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Profile 连接档案：命名的 SSH 连接参数（密码为可选存储，明文落在本机用户配置目录）
type Profile struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Key      string `json:"key"`
}

// ProfileStore 档案持久化（JSON 文件，放用户配置目录，权限 0600）
type ProfileStore struct {
	mu   sync.Mutex
	path string
	list []Profile
}

func newProfileStore() *ProfileStore {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return &ProfileStore{path: filepath.Join(dir, "bastionshell", "profiles.json")}
}

func (s *ProfileStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var wrap struct {
		Profiles []Profile `json:"profiles"`
	}
	if json.Unmarshal(data, &wrap) == nil {
		s.list = wrap.Profiles
	}
}

func (s *ProfileStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Profiles []Profile `json:"profiles"`
	}{s.list}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *ProfileStore) upsert(p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].Name == p.Name {
			s.list[i] = p
			return s.saveLocked()
		}
	}
	s.list = append(s.list, p)
	return s.saveLocked()
}

func (s *ProfileStore) remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list[:0]
	for _, p := range s.list {
		if p.Name != name {
			out = append(out, p)
		}
	}
	s.list = out
	return s.saveLocked()
}

func (s *ProfileStore) snapshot() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, len(s.list))
	copy(out, s.list)
	return out
}

// ---- 端口转发规则持久化（复用 forward.go 的 forwardRule，运行时状态在前端）----

type forwardStore struct {
	mu   sync.Mutex
	path string
	list []forwardRule
}

func newForwardStore() *forwardStore {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return &forwardStore{path: filepath.Join(dir, "bastionshell", "forwards.json")}
}

func (s *forwardStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var wrap struct {
		Forwards []forwardRule `json:"forwards"`
	}
	if json.Unmarshal(data, &wrap) == nil {
		s.list = wrap.Forwards
	}
}

func (s *forwardStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Forwards []forwardRule `json:"forwards"`
	}{s.list}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *forwardStore) upsert(r forwardRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].ID == r.ID {
			s.list[i] = r
			return s.saveLocked()
		}
	}
	s.list = append(s.list, r)
	return s.saveLocked()
}

func (s *forwardStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list[:0]
	for _, r := range s.list {
		if r.ID != id {
			out = append(out, r)
		}
	}
	s.list = out
	return s.saveLocked()
}

func (s *forwardStore) snapshot() []forwardRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]forwardRule, len(s.list))
	copy(out, s.list)
	return out
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
func registerProfileRoutes(mux *http.ServeMux, store *ProfileStore) {
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

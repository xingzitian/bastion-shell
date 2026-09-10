package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// secretStore 敏感数据（密码）加密存储：DPAPI 加密后落 JSON 文件（0600）。
// protectData / unprotectData 在 secrets_windows.go（DPAPI）与 secrets_other.go（非 Windows 回退）实现。
type secretStore struct {
	mu   sync.Mutex
	path string
	vals map[string]string // key -> base64(DPAPI 密文)
}

func newSecretStore() *secretStore {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return &secretStore{path: filepath.Join(dir, "bastionshell", "secrets.json"), vals: map[string]string{}}
}

func (s *secretStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	if json.Unmarshal(data, &s.vals) != nil || s.vals == nil {
		s.vals = map[string]string{}
	}
}

func (s *secretStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.vals, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *secretStore) set(key, value string) error {
	if value == "" {
		return s.remove(key)
	}
	enc, err := protectData([]byte(value))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vals[key] = base64.StdEncoding.EncodeToString(enc)
	return s.saveLocked()
}

func (s *secretStore) get(key string) (string, bool) {
	s.mu.Lock()
	b64, ok := s.vals[key]
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", false
	}
	plain, err := unprotectData(raw)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

func (s *secretStore) remove(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vals[key]; !ok {
		return nil
	}
	delete(s.vals, key)
	return s.saveLocked()
}

func registerSecretRoutes(mux *http.ServeMux, store *secretStore) {
	store.load()
	writeJSON := func(w http.ResponseWriter, code int, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/secret", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		switch r.Method {
		case http.MethodGet:
			if key == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key required"})
				return
			}
			if v, ok := store.get(key); ok {
				writeJSON(w, http.StatusOK, map[string]string{"value": v})
			} else {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			}
		case http.MethodPost:
			var req struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key/value required"})
				return
			}
			if err := store.set(req.Key, req.Value); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		case http.MethodDelete:
			if key == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key required"})
				return
			}
			if err := store.remove(key); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
}

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// registerUploadRoutes 挂文件上传：浏览器读到的文件字节（base64）→ 写临时文件 → 返回路径，
// 供 trzsz UploadFiles 使用（浏览器/WebView 模式拿不到真实文件路径）。
func registerUploadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
			Data string `json:"data"` // base64
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			http.Error(w, `{"error":"bad base64"}`, http.StatusBadRequest)
			return
		}
		name := req.Name
		if name == "" {
			name = fmt.Sprintf("upload-%d", time.Now().UnixNano())
		}
		name = filepath.Base(name) // 防路径穿越
		// 写到独立临时子目录，文件名保持原名（trzsz 用 basename 作为远端文件名，不能加前缀）
		tmpDir, err := os.MkdirTemp("", "bastionshell-upload-")
		if err != nil {
			http.Error(w, `{"error":"mkdir failed"}`, http.StatusInternalServerError)
			return
		}
		tmpPath := filepath.Join(tmpDir, name)
		if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
			http.Error(w, `{"error":"write failed"}`, http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"path": tmpPath})
	})
}

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// bastionConfigDir 配置目录（%APPDATA%\bastionshell）。
func bastionConfigDir() string {
	if v := strings.TrimSpace(os.Getenv("BASTIONSHELL_CONFIG_DIR")); v != "" {
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return os.TempDir()
	}
	return filepath.Join(dir, "bastionshell")
}

// serveBackend 启动 Go 的壳后端（默认 18091）。
//
// 统一核心之后，终端/会话/执行/上传/转发都由 core 的界面后端（18090）负责，
// 这里只剩「壳能力」：DPAPI 存密码、系统文件对话框、拉起 core 后端、诊断。
// 界面（wails:// 源）与 core（18090）都要跨域访问，所以统一加 CORS。
func serveBackend(port int) error {
	mux := http.NewServeMux()
	registerProfileRoutes(mux, newProfileStore()) // 档案读写（core 侧也读同一份 jsonc）
	registerForwardRoutes(mux, newForwardStore()) // 转发规则的持久化列表
	registerQuickCommandRoutes(mux)               // 快捷命令
	registerSecretRoutes(mux, newSecretStore())   // DPAPI 存密码
	registerUploadRoutes(mux)                     // 浏览器文件字节 → 本地临时文件（core 要路径）
	registerPickFilesRoute(mux)                   // 系统「选择文件」对话框
	registerCliShellRoutes(mux)                   // CLI 薄壳：拉起/停止 core 后端（GET /api/shell/cli?profile=…）
	registerFsProbeRoute(mux)                     // 诊断：从应用进程视角看哪些目录可写
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("壳后端已启动: http://%s", addr)
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

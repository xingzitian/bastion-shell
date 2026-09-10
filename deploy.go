package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// registerDeployRoutes 挂部署相关接口：原生文件多选对话框（拿到真实本地路径）。
// 部署执行本身走前端编排：复用堡垒机连接上的会话（openSession + rz 上传 + 写脚本），
// 因此后端不再需要直连目标机器的 deploy 通道。
func registerDeployRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/pick-files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if appCtx == nil {
			http.Error(w, `{"error":"context not ready"}`, http.StatusInternalServerError)
			return
		}
		files, err := runtime.OpenMultipleFilesDialog(appCtx, runtime.OpenDialogOptions{Title: "选择要上传的文件"})
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"paths": files})
	})
}

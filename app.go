package main

import (
	"context"
)

// appCtx 全局 Wails 上下文：供后端 HTTP 处理器调用原生对话框（如选文件）用
var appCtx context.Context

// App struct
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	appCtx = ctx
}

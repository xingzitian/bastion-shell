//go:build windows

package trzsz

import (
	"os/exec"
	"syscall"
)

// hideWindow 隐藏子进程控制台窗口：sz/rz 是控制台程序，从 GUI 应用（Wails）启动会闪黑框。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

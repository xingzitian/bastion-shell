//go:build !windows

package trzsz

import "os/exec"

// hideWindow 非 Windows 平台无控制台窗口问题。
func hideWindow(cmd *exec.Cmd) {}

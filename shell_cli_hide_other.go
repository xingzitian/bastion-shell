//go:build !windows

package main

import "os/exec"

// hideWindow 在非 Windows 上是空操作（没有"弹控制台窗口"这回事）。
func hideWindow(cmd *exec.Cmd) {}

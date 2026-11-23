//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow 让子进程**不弹控制台窗口**。
//
// 为什么必须有：桌面是 GUI 程序，用 exec.Command 起 node（控制台程序）时，
// Windows 会为它创建一个新的控制台窗口 —— 用户看到的就是"一会儿跳出来一个窗口，消失"。
// 2026-11-23 用户明确报了这个问题（点 ⚡、以及我跑 e2e 时都会闪）。
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	// CREATE_NO_WINDOW：比 HideWindow 更彻底（连控制台都不分配）
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}

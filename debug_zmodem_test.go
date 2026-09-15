package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 诊断用（不是断言测试）：把 rz 上传时**屏幕上真正发生的事**打出来。
//
// 为什么需要：zmodem 这条通道一旦卡住，从外面只能看到"没动静" ——
// 到底是远端没有 rz、还是本地落进了 zenity 选文件对话框、还是协议没谈起来，
// 只有屏幕原文能分清。跑法：
//
//	BASTION_E2E_ZMODEM=1 BASTION_E2E_SSH=127.0.0.1:2222 ... \
//	  go test -run TestDebugZmodemUpload -v -timeout 120s ./
func TestDebugZmodemUpload(t *testing.T) {
	e2eZmodem(t)
	s, _ := e2eSession(t)

	local := t.TempDir()
	src := filepath.Join(local, "probe.txt")
	if err := os.WriteFile(src, []byte("hello zmodem\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 先确认远端到底有没有 rz
	probe, _ := s.exec("command -v rz sz; echo rc=$?; echo $PATH", execOpts{QuietMs: 3000})
	t.Logf("远端 rz/sz 探测：\n%s", probe.Output)

	// 走**应用里那条原始路径**（不是传输工具）：直接 UploadFiles
	if err := s.uploadFiles([]string{src}); err != nil {
		t.Logf("UploadFiles 直接返回错误：%v", err)
	}
	for i := 0; i < 8; i++ {
		time.Sleep(2 * time.Second)
		t.Logf("t=%2ds transferring=%v 屏幕最后 6 行：\n%s", (i+1)*2, s.isTransferring(), s.screenTail(6))
		if i >= 2 && !s.isTransferring() {
			break
		}
	}
	t.Logf("最终屏幕（最后 20 行）：\n%s", s.screenTail(20))
}

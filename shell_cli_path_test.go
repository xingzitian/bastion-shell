package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCliScriptCandidates(t *testing.T) {
	got := cliScriptCandidates("/repo/bastion-wails/build/bin")
	want := []string{
		"/repo/bastion-wails/build/bin/bastion-cli/main.js",
		"/repo/bastion-wails/build/bin/../bastion-cli/main.js",
		"/repo/bastion-wails/build/bin/../../bastion-vscode/out/cli/main.js",
		"/repo/bastion-wails/build/bin/../../../bastion-vscode/out/cli/main.js",
		"/repo/bastion-wails/build/bin/../../../../bastion-vscode/out/cli/main.js",
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if samePath(g, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("候选里缺少 %q；实际：%v", w, got)
		}
	}
}

// samePath 只比较"正常化之后"的字符串（测试里不要碰真实文件系统）。
func samePath(a, b string) bool {
	return filepath.ToSlash(filepath.Clean(a)) == filepath.ToSlash(filepath.Clean(b))
}

func TestCliScriptPathEnvWins(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "main.js")
	if err := os.WriteFile(f, []byte("// x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASTIONSHELL_CLI_JS", f)
	got, err := cliScriptPath()
	if err != nil {
		t.Fatalf("应该用环境变量：%v", err)
	}
	if !samePath(got, f) {
		t.Fatalf("返回 %q，期望 %q", got, f)
	}
}

func TestCliScriptPathExplainsHowToFix(t *testing.T) {
	t.Setenv("BASTIONSHELL_CLI_JS", "")
	// 把 exe 目录指到临时目录，保证候选全不存在（真实 exe 存在，但测试进程的 exe 是 go test 的临时二进制）
	if _, err := cliScriptPath(); err != nil {
		msg := err.Error()
		for _, want := range []string{"BASTIONSHELL_CLI_JS", "out/cli/main.js", "试过"} {
			if !strings.Contains(msg, want) {
				t.Errorf("报错里应包含 %q，实际：%s", want, msg)
			}
		}
	}
}

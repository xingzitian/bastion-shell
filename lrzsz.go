package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// useZmodem 是否启用 zmodem（lrzsz）模式：
// Windows 上用内嵌的 sz/rz（服务器只需 rz/sz，无需 trz/tsz），Linux 自测沿用原生 trzsz。
var useZmodem bool

// initLrzsz Windows 上把内嵌 lrzsz 解压到临时目录并加入 PATH，供 trzsz-go 的 zmodem 模式 exec sz/rz。
func initLrzsz() {
	if runtime.GOOS != "windows" {
		return
	}
	dir, err := os.MkdirTemp("", "bastionshell-lrzsz-")
	if err != nil {
		return
	}
	for _, name := range []string{"sz.exe", "rz.exe", "msys-2.0.dll"} {
		data, err := lrzszFS.ReadFile("assets/" + name)
		if err != nil {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			return
		}
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	useZmodem = true
}

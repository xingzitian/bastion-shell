//go:build !windows

package main

// 非 Windows 平台无 DPAPI，退回明文（仅用于 Linux 下 go build 语法检查；正式版只跑 Windows）
func protectData(plain []byte) ([]byte, error) {
	return plain, nil
}

func unprotectData(enc []byte) ([]byte, error) {
	return enc, nil
}

//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// protectData 用 Windows DPAPI 加密（绑定当前用户，无需密钥管理）
func protectData(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return []byte{}, nil
	}
	var out windows.DataBlob
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	res := make([]byte, int(out.Size))
	copy(res, unsafe.Slice(out.Data, int(out.Size)))
	return res, nil
}

// unprotectData 用 Windows DPAPI 解密
func unprotectData(enc []byte) ([]byte, error) {
	if len(enc) == 0 {
		return []byte{}, nil
	}
	var out windows.DataBlob
	in := windows.DataBlob{Size: uint32(len(enc)), Data: &enc[0]}
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	res := make([]byte, int(out.Size))
	copy(res, unsafe.Slice(out.Data, int(out.Size)))
	return res, nil
}

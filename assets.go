package main

import "embed"

// lrzszFS 内嵌 Windows 版 lrzsz（sz.exe / rz.exe / msys-2.0.dll），
// 用于 zmodem 模式的文件传输（服务器只需有 rz/sz，无需 trz/tsz）。
//
//go:embed assets/*
var lrzszFS embed.FS

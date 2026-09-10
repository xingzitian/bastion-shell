#!/usr/bin/env bash
# BastionShell (Wails) 交叉编译到 Windows：Debian WSL 里执行
set -e
export PATH="$PATH:/home/wsl/go/bin"
export GOPROXY=https://goproxy.cn,direct
export GOFLAGS=-mod=mod
export CC=x86_64-w64-mingw32-gcc-posix
export CXX=x86_64-w64-mingw32-g++-posix
export CGO_ENABLED=1
cd "$(dirname "$0")"
wails build -platform windows/amd64 -trimpath -ldflags "-s -w" "$@"

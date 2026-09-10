# 第三方组件与许可

本仓库**直接分发**了若干第三方二进制与源码。它们各有各的许可证，**不是**本项目的代码。
这里列清出处，方便使用者核对，也满足再分发时的署名/许可要求。

> 本项目自身的代码许可见仓库根目录（如果还没加许可证文件，默认是「保留所有权利」）。

## 1. assets/ 里内嵌的二进制（随程序分发）

`assets.go` 用 `//go:embed assets/*` 把这三个文件打进可执行文件，
运行时由 `lrzsz.go` 解压到临时目录并加入 PATH，供 zmodem（rz/sz）文件传输使用。

| 文件 | 大小 | 说明 | 许可 |
|---|---|---|---|
| `assets/rz.exe` | 296 KB | lrzsz 的 Windows 移植版 `rz`（接收文件） | **GPL**（lrzsz / 其 Windows 移植版） |
| `assets/sz.exe` | 306 KB | lrzsz 的 Windows 移植版 `sz`（发送文件） | **GPL** |
| `assets/msys-2.0.dll` | 2.9 MB | MSYS2 运行时，上面两个 exe 依赖它 | **GPL**（MSYS2 runtime，含例外条款，见下） |

⚠️ **再分发提醒**：GPL 组件随本程序一起分发时，需要同时提供对应源码或获取源码的方式。
如果这个仓库要正式对外发布安装包，建议：
1. 在发布页写明这三个文件的来源与版本；
2. 附上对应上游源码的链接/副本；
3. 或者改成「首次使用时由用户自己下载 lrzsz」，仓库里不放二进制。

（MSYS2 的 runtime 是 GPLv3 **带例外**：把 runtime 与你的程序一起分发不会传染你的程序，
但要求随附其许可证文本与源码出处。详见 https://www.msys2.org/ 与
https://cygwin.com/licensing.html 的说明。）

## 2. third_party/trzsz-go/（vendored 源码，带本地补丁）

| | |
|---|---|
| 上游 | https://github.com/trzsz/trzsz-go |
| 版本 | v1.2.0（本地有补丁，见 `third_party/trzsz-go/PATCHES.md`） |
| 许可 | MIT（原文见 `third_party/trzsz-go/LICENSE`） |
| 为什么 vendor | 上游没有提供「隐藏子进程控制台窗口」的开关，而 GUI 程序启动 rz/sz 会闪黑框 |

## 3. Go 依赖

完整清单见 `go.mod` / `go.sum`。主要几项：

| 模块 | 用途 | 许可 |
|---|---|---|
| `github.com/wailsapp/wails/v2` | 原生窗口壳（WebView2） | MIT |
| `github.com/trzsz/trzsz-go` | zmodem / trzsz 传输协议实现 | MIT |
| `github.com/gorilla/websocket` | 前端 ↔ Go 的 WebSocket 通道 | BSD-3-Clause |
| `golang.org/x/crypto`、`golang.org/x/sys` | SSH 与系统调用 | BSD-3-Clause |

## 4. 前端依赖

完整清单见 `frontend/package.json` 与 `frontend/package-lock.json`（版本已锁定）。

| 模块 | 用途 | 许可 |
|---|---|---|
| `react` / `react-dom` | 界面框架 | MIT |
| `zustand` | 状态管理 | MIT |
| `@xterm/xterm` + `addon-fit` / `addon-web-links` | 终端渲染 | MIT |
| `vite` / `@vitejs/plugin-react` | 构建工具（仅开发期） | MIT |

图标与 `frontend/src/assets/` 下的样式均为本项目所有。
（早前 Wails 模板自带的 Nunito 字体与演示页已随前端源码替换一并删除。）

# BastionShell（Wails 原生窗口版）

BastionShell 的 Wails 壳：Go 后端 + 内嵌 React 前端，打包成 Windows 原生窗口程序（WebView2），不再是 Edge `--app` 浏览器页。

## 架构

```
原生窗口 (Wails / WebView2)
  └─ 前端 React (frontend/dist，含 shim.js)
       ├─ WebSocket → ws://127.0.0.1:18090/ws    (SSH 连接/会话/转发/上传)
       └─ HTTP      → http://127.0.0.1:18090/api  (档案/转发规则/文件上传预处理)
后端 Go（进程内，main.go 启动 serveBackend(18090)）
  ├─ server.go   HTTP+WS + 连接池 + 交互式 MFA + trzsz 过滤器
  ├─ ssh.go      SSH 连接 / 私钥 / keyboard-interactive
  ├─ forward.go  端口转发运行时
  ├─ profile.go  档案 + 转发规则持久化 (REST)
  ├─ upload.go   浏览器文件上传 (base64 → 临时文件)
  └─ lrzsz.go    内嵌 lrzsz (sz/rz)，zmodem 文件传输
```

后端 Go 代码全部在本仓库内（自包含，不依赖任何兄弟目录）。
> 历史说明：早期还有一个前后端分离的 `bastion-go` 实验版，已废弃；
> server/forward/profile/upload/lrzsz/assets 这些文件最终都并回了本仓库。

## 前端

界面是 React + TypeScript（xterm.js 做终端），源码就在本仓库：

| 目录 | 是什么 |
|---|---|
| `frontend/src/` | 界面源码（`App.tsx`、`components/`、`store/`、`lib/` + `shared/` 里的类型定义） |
| `frontend/public/shim.js` | **适配层**：把 `window.bastion` 这层接口翻译到 Go 后端的 WebSocket / HTTP。<br>被 vite 原样复制进 `dist/`，并在应用 bundle **之前**加载（见 `index.html`） |
| `frontend/index.html` | 入口页。注意 CSP 里的 `connect-src`：界面要连 `ws://127.0.0.1:18090`，不放开会被 CSP 拦掉 |
| `frontend/dist/` | 构建产物（**不入库**，由 `npm run build` 生成，`go:embed` 嵌进 exe） |
| `frontend/wailsjs/` | Wails 生成的绑定（当前界面走 shim，不直接用，留着备用） |

`wails.json` 里配了 `frontend:install = npm ci`、`frontend:build = npm run build`，
所以 **`wails build` 会自动构建前端**，你只需要：

```bash
bash build-win.sh        # 内部就是 wails build
```

单独改界面调试时：

```bash
cd frontend
npm ci
npm run build            # 产出 frontend/dist
```

> ⚠️ 不要单独跑 `go build`：`main.go` 有 `//go:embed all:frontend/dist`，
> dist 不存在会直接编译失败。用 `wails build` 就对了。
>
> 国内的 npm 源可以这样配（否则装依赖会很慢）：
> `npm config set registry https://registry.npmmirror.com`

## 构建（Debian WSL，交叉编译到 Windows）

前置（Debian WSL 已装好）：Go ≥1.25、mingw-w64、Wails CLI、node/npm。

```bash
# 在 Debian WSL 里执行
bash build-win.sh
```

产物：`build/bin/BastionShell.exe`（约 18.5MB，原生窗口 + 内嵌前端 + lrzsz + 图标）。

关键点：
- 用 mingw **posix** 变体（`x86_64-w64-mingw32-gcc-posix`），win32 变体不支持 cgo 的 `-mthreads`；
- `frontend:install`/`frontend:build` 置空——前端是预先构建好的 React 产物，直接放在 `frontend/dist/`；
- 版本信息走 `wails.json` 的 `info` 块（注意：当前 Wails 跨编译下版本信息字符串偶发为空，属上游小 bug，不影响运行）。

## 运行

双击 `build/bin/BastionShell.exe` 即可。程序启动后：起后端服务（127.0.0.1:18090）+ 打开原生窗口加载前端。

## 目录

```
main.go        Wails 入口（起后端 + wails.Run）
app.go         App 结构（startup）
server.go      HTTP+WS 后端 + CORS
ssh.go         SSH 连接/私钥/MFA
forward.go     端口转发
profile.go     档案/转发规则持久化
upload.go      文件上传预处理
lrzsz.go       内嵌 lrzsz 解压 + zmodem 开关
assets.go      go:embed lrzsz 二进制
assets/        sz.exe / rz.exe / msys-2.0.dll
frontend/dist  React 构建（index.html + shim.js + assets/）
build/         图标 + Windows 资源 + 产物
build-win.sh   一键交叉编译脚本
```

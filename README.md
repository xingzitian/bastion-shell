# BastionShell（Wails 原生窗口版）

<!-- 第一行动态（数字/版本自己更新）；第二行是平台与技术栈，属固定事实 -->
[![Build](https://github.com/xingzitian/bastion-shell/actions/workflows/build.yml/badge.svg?branch=main)](https://github.com/xingzitian/bastion-shell/actions/workflows/build.yml)
[![license](https://img.shields.io/github/license/xingzitian/bastion-shell?color=blue)](LICENSE)
[![last commit](https://img.shields.io/github/last-commit/xingzitian/bastion-shell)](https://github.com/xingzitian/bastion-shell/commits/main)
[![go version](https://img.shields.io/github/go-mod/go-version/xingzitian/bastion-shell?logo=go&logoColor=white)](go.mod)

[![平台](https://img.shields.io/badge/%E5%B9%B3%E5%8F%B0-Windows%20x64-0078D6?logo=windows&logoColor=white)](#运行)
[![WebView2](https://img.shields.io/badge/%E4%BE%9D%E8%B5%96-WebView2-0078D6)](https://developer.microsoft.com/microsoft-edge/webview2/)
[![Wails](https://img.shields.io/badge/Wails-v2-DF0000)](https://wails.io/)
[![React](https://img.shields.io/badge/React-61DAFB?logo=react&logoColor=black)](https://react.dev/)
[![xterm.js](https://img.shields.io/badge/xterm.js-4B5563)](https://xtermjs.org/)
[![ZMODEM](https://img.shields.io/badge/%E4%BC%A0%E8%BE%93-ZMODEM%20%2F%20trzsz-4B5563)](#前端)

> 仓库：<https://github.com/xingzitian/bastion-shell> ·
> 问题反馈：<https://github.com/xingzitian/bastion-shell/issues> ·
> 第三方组件与许可：见 [THIRD-PARTY.md](THIRD-PARTY.md)

BastionShell 的 Wails 壳：Go 后端 + 内嵌 React 前端，打包成 Windows 原生窗口程序（WebView2），不再是 Edge `--app` 浏览器页。

## ⚠️ 当前能力边界（**这一版落后于 VS Code 版**）

先说实话，免得下错结论：**这一版不是 VS Code 版的移植，是另一套独立实现**（Go + React）。
两边共有：SSH 连接复用、rz/sz 传输、端口转发、广播输入、快捷命令、主题、多会话。
本版**独有**：分屏 / 多标签、终端高亮规则、OSC52、原生窗口。

**已经补上的**（原来只有扩展有）：

| 能力 | 现状 |
|---|---|
| **MCP 出口** | ✅ 有端点（默认 `http://127.0.0.1:39311/mcp`），8 个工具 + Bearer token + 端口占用自动退让 |
| **命令执行引擎**（哨兵 + 退出码 + 输出静止兜底） | ✅ 有；AI 的 `bastion_exec` 就建在它上面 |
| **会话登记表 + 屏幕快照** | ✅ 有；`bastion_listSessions` / `bastion_tail` 用它 |
| **AI 传文件** | ✅ 有（`bastion_push` / `bastion_pull`）：能力探测 + 无 lrzsz 自动降级 base64 + 目录 tar 打包 + 传完回读校验 |
| **高危命令拦截** | ✅ 有：命中规则**不发出去**，理由是共享规则文件里那套 |
| **个人习惯** | ✅ 有（`bastion_habits`）：提权方式会真的影响 `bastion_exec` 的行为 |
| **共享规则 JSON** | ✅ 菜单识别 / 高危命令 / 个人习惯**与 VS Code 扩展读同一份文件**（见下） |

下面这些能力**目前仍然只有 VS Code 扩展有**（[`bastion-vscode`](https://github.com/xingzitian/bastion-vscode)）：

| 还缺什么 | 意味着 |
|---|---|
| **AI 自己连一台新机器**（`bastion_connect`） | 只能操作**用户已经手动登录好的**会话；连目标机、过菜单、选资产仍然是人的活（菜单识别只用于"拦"，不用于"自动走"） |
| **部署执行与报告** | 没有逐条命令输出捕获、没有 Markdown 报告，失败只能人肉看终端 |
| **rsync 增量同步** | 仍是实验性未完成（见文末） |
| **终端高亮 / 分屏多标签** | 这两样反过来是**本版独有**，扩展没有 |

> 完整对照（含实测规模、为什么会分裂、后续怎么办）：
> <https://github.com/xingzitian/bastion-vscode/blob/main/docs/two-flavors.md>
>
> **当前的开发重心在 VS Code 扩展 + MCP**；这一版按"能用但不全"对待。
> 长期方向是把"会话宿主"抽成共享组件（本地守护进程），让这一版变成薄前端，而不是在 Go 里再写一遍。

## MCP：让 AI 操作你已经登录好的会话

桌面版现在**自己就是一个 MCP 服务端**：外部 AI 客户端（VS Code / Copilot / Claude / Trae…）
能在**用户已经手动认证过的会话**上执行命令、读屏幕、列档案。

设计上和扩展版是同一套语义（同一批工具名、同一段给模型看的说明），所以两边的文档和提示词不用分叉。

| 工具 | 作用 | 只读 |
|---|---|---|
| `bastion_listSessions` | 列出会话 + 每条会话现在能不能直接执行命令 | ✅ |
| `bastion_health` | 端点自检（AI 报错时的第一条退路） | ✅ |
| `bastion_tail` | 读某条会话屏幕上最后几行 | ✅ |
| `bastion_listProfiles` | 列出连接档案 | ✅ |
| `bastion_exec` | 在会话上执行命令，返回输出 **+ 退出码**；命中高危规则时**不发** | ❌ |
| `bastion_habits` | 读写个人习惯（提权方式 / 常用目录 / 口头约定） | ❌ |
| `bastion_push` | 传文件/目录到远端（自动选通道 + 回读校验） | ❌ |
| `bastion_pull` | 把远端文件拉回本机（`sz` / base64 降级） | ❌ |

`bastion_connect`（AI 自己连一台新机器）**还没有** —— 桌面版的菜单识别只用来"拦"，
不用来"自动走"。没实现就不注册：宁可 `tools/list` 里没有，也不要让模型看到、调了却拿到一句"没实现"。

**连接信息**：端点和 token 在程序配置目录下 —— Windows 是
`%AppData%\bastionshell\mcp.json`（其它平台 `~/.config/bastionshell/mcp.json`）。
token 是消费级凭证（拿到它就能在这些服务器上执行任意命令），所以：

- 端点**只监听 127.0.0.1**，必须带 `Authorization: Bearer <token>`；
- token 长期不变（每次启动换 token 会让配好的客户端全部失效）；
- **内部后端（18090）故意不返回 token** —— 那个端口是 CORS `*` 且不鉴权的，
  把 token 交出去等于给任意网页开后门。

客户端配一个 HTTP MCP 服务器即可（以 VS Code 的 `mcp.json` 为例）：

```json
{
  "servers": {
    "bastionshell": {
      "type": "http",
      "url": "http://127.0.0.1:39311/mcp",
      "headers": { "Authorization": "Bearer <mcp.json 里的 token>" }
    }
  }
}
```

默认端口 39311 和 VS Code 扩展**取的是同一个值**：两个程序同时开着会冲突 ——
后启动的那个自动退到随机端口，并在 `mcp.json` 与日志里写明（`portFallback`）。

关掉它：设环境变量 `BASTIONSHELL_MCP=0`。换配置目录：`BASTIONSHELL_CONFIG_DIR=<目录>`。

### 它到底能干什么 / 不能干什么

**能**：让 AI 在你已经登进去的那台机器上跑命令（看日志、查进程、改配置、写脚本）、
把输出和退出码带回来、读屏幕确认现状、在你手动过完菜单后接着干。

**不能**：替你做 MFA、替你输密码、替你选资产、替你连一台新机器（这些都需要人）。
会话是**人和 AI 共用同一条** —— 你在窗口里敲、它接着用，不需要重新认证。

### 传文件：和一个说法、一份证据

`bastion_push` / `bastion_pull`（以及界面上拖文件）走的是**同一套**标准工具：

| 情况 | 走哪条通道 |
|---|---|
| 目标机装了 lrzsz | `rz` / `sz`（和内嵌的 lrzsz 一套实现） |
| 目标机**没装** lrzsz | **自动降级 base64** 分块（上传超过 4MB 会直接拒绝并说明原因） |
| 传的是**目录** | 先本机 `tar -czf` 打包 → 传 → 远端 `tar -xzf` 解开 → 删掉临时包 |

远端已有同名文件时按「覆盖方式」处理，默认是 **skip：跳过并明确告诉你**（不覆盖）。
想改成 `overwrite` / `rename`：设环境变量 `BASTIONSHELL_UPLOAD_OVERWRITE`。

**传完一定回读远端**（哈希 + 大小 + 路径 + 能不能读），成功失败都有依据 ——
判定**只看内容哈希**，不看 mtime（ZMODEM 会把源文件的 mtime 一起带过去，拿它判断必然误报）。

## 共享规则：两个版本读同一份文件

桌面版和 VS Code 扩展现在读**同一个目录**里的规则文件（`~/.bastionshell/`）：

| 文件 | 里面是什么 | 谁写 |
|---|---|---|
| `profiles.jsonc` | **连接档案**：主机、端口、用户名、认证方式、私钥路径、direct/bastion 模式 | 两边都能写（界面里加的机器，另一边直接能看到），也可以手改 |
| `habits.jsonc` | 个人习惯：提权方式（none/sudo/sudo-i/ask）、常用工作目录、口头约定 | 两边都能写，也可以手改 |
| `dangerRules.jsonc` | 高危命令规则：内置的可按 id 关掉，也能加自己那套 | 手改 |
| `menuHints.jsonc` | 堡垒机菜单识别规则：内置的可按原文关掉，也能追加自己那家堡垒机的提示语 | 手改 |

语义刻意统一：**内置的照用，文件只做「关掉某条」和「追加自己的」**（不搞"填了就整组替换"，
那样以后内置规则改进了也用不上）。文件坏了只记日志、**绝不覆盖**你的文件。

**连接档案也在这套里**（`profiles.jsonc`）：桌面版以前自己存一份 `%AppData%\bastionshell\profiles.json`，
现在改成读写共享文件，并在第一次启动时把老档案**迁进来**（**原文件保留不动**，老文件里的明文密码
**故意不迁** —— 凭据只留在各家的系统凭据库里）。

写回时**只改自己认识的字段**：扩展侧以后加字段是迟早的事，拿本方结构体整体重写会把它们抹掉 ——
那是数据丢失，不是同步。两边都有测试钉住这一条（含一条"用桌面版真实写出的文件内容去验扩展侧读得到"）。

- 桌面版自己的东西（档案、转发规则、MCP token）在 `%AppData%\bastionshell`，**别和共享目录混**；
- `BASTIONSHELL_SHARED_DIR` 可以改写共享目录（测试/便携用，扩展侧认同一个变量）。

> 提权习惯会真的影响行为：记成 `sudo`（免密）时，`sudo <命令>` 才会加命令结束标记；
> 记成 `ask` / `sudo-i` 就退回"输出静止"兜底，并在弹密码时明确告诉 AI「让用户来输」。

## 架构

```
原生窗口 (Wails / WebView2)
  └─ 前端 React (frontend/dist，含 shim.js)
       ├─ WebSocket → ws://127.0.0.1:18090/ws    (SSH 连接/会话/转发/上传)
       └─ HTTP      → http://127.0.0.1:18090/api  (档案/转发规则/文件上传预处理)
后端 Go（进程内，main.go 启动 serveBackend(18090)）
  ├─ server.go   HTTP+WS + 连接池 + 交互式 MFA
  ├─ session.go  会话登记表 + 输出滚动缓冲 + 就绪判定（exec/AI 的地基）
  ├─ exec.go     命令执行引擎（哨兵 + 退出码 + 输出静止兜底）
  ├─ menu.go     堡垒机菜单识别（决定"能不能往这条会话发命令"）
  ├─ ssh.go      SSH 连接 / 私钥 / keyboard-interactive
  ├─ forward.go  端口转发运行时
  ├─ profile.go  档案 + 转发规则持久化 (REST)
  ├─ upload.go   浏览器文件上传 (base64 → 临时文件)
  └─ lrzsz.go    内嵌 lrzsz (sz/rz)，zmodem 文件传输

外部 AI 客户端（VS Code / Claude / Trae…）
  └─ HTTP POST http://127.0.0.1:39311/mcp   (Bearer token)
       ├─ mcp_server.go   JSON-RPC + Streamable HTTP + 鉴权
       ├─ mcp_tools.go    工具定义（与扩展侧同名同义）
       ├─ ai_api.go       工具实现（找会话 / 拦菜单 / 执行 / 拼提示）
       └─ mcp_register.go 端点生命周期 + token/端口持久化
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
main.go        Wails 入口（起后端 + MCP 端点 + wails.Run）
app.go         App 结构（startup）
server.go      HTTP+WS 后端 + CORS + 会话生命周期
session.go     会话登记表 / 输出滚动缓冲 / 就绪判定 / rz-sz 落地
exec.go        命令执行引擎（哨兵/退出码/静止兜底）
menu.go        堡垒机菜单识别 + 会话状态判定
danger.go      高危命令规则（内置 + 共享文件覆盖）
shared_rules.go 共享规则目录：habits / dangerRules / menuHints
jsonc.go       带注释的 JSON 读写（只动目标节点，注释保留）
termtext.go    终端文本整理（去 ANSI、\r 覆盖、标记退避）
tailbuf.go     滚动缓冲（逻辑索引，裁剪后标记仍有效）
transfer.go    传输标准工具（能力探测/降级/tar/回读校验）
upload_verify.go 远端回读与上传判定（哈希为准，不看 mtime）
mcp_server.go  MCP JSON-RPC + Streamable HTTP 传输
mcp_tools.go   MCP 工具定义与分发
ai_api.go      工具实现（会话能力层）
mcp_register.go MCP 端点生命周期 + mcp.json
version.go     程序版本（与 wails.json 对齐，有测试盯着）
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

## 测试

```bash
go test ./              # 单元测试（不需要任何外部环境）
```

单元测试里那些"真机上的文本形态"（pty 回显、被折行切碎的哨兵、堡垒机菜单原文）
都是从实机抓下来固化进用例的，不需要 SSH 也能跑。

需要靶机的 E2E 靠环境变量开启（不设就整组跳过）：

```bash
# 一台装了 sshd 的机器/容器/WSL；用户能免密登入、有 shell 即可
export BASTION_E2E_SSH=127.0.0.1:2222
export BASTION_E2E_USER=bastiontest
export BASTION_E2E_KEY=/path/to/id_ed25519
go test -run TestE2E -v ./

# 再加这一条，会用**官方 MCP SDK**（VS Code 用的就是它）连真端点、跑真命令
export BASTION_E2E_MCP_SDK_DIR=/path/to/dir-with-node_modules  # 里面要有 @modelcontextprotocol/sdk
```

`debug_echo_test.go` 是诊断用的：它把真实回显的**原始字节**打出来。
排查"AI 拿到的输出里怎么混进了怪东西"时，先跑它看清楚字节，再改代码。

**注意两条通道的验证程度不一样**（别把"没验过"当成"验过了"）：

| 通道 | 验证情况 |
|---|---|
| base64 降级 / 目录 tar / 回读校验 | ✅ 在真靶机上端到端验过（含中文+空格文件名、覆盖/跳过/改名三种模式） |
| `rz` / `sz`（需要目标机装 lrzsz） | ✅ 已验（2026-09-15）：在装了 lrzsz 的 Ubuntu 靶机上跑 `TestE2EZmodem*`。**附带把「上传权限是不是 0000」实测掉了：rz 上传的远端文件是 `644`，属主正确、`[ -r ]` 可读、md5 与本地一致 —— 没有 0000 问题** |

rz/sz 那一组要**单独跑**（`initLrzsz()` 会把全局传输模式切成 zmodem，而其它用例是在 trzsz 模式下写的断言）：

```bash
export BASTION_E2E_ZMODEM=1      # 明确表示"这台靶机装了 lrzsz"
go test -run TestE2EZmodem -v ./
```

## 🚧 未完成：rsync 增量同步（`tools/rsync-probe/`）

前端的 `SyncDialog` 还在，但 **Go 侧从来没有实现过**（`window.bastion.syncDirectory` 在后端不存在）——
也就是说这个功能在桌面版里**是空的**，别被界面骗了。

它的调查工具和实测记录都在 [`tools/rsync-probe/`](tools/rsync-probe/)（含 `RESULTS.md`，四轮实测的原始输出，
以及被实测**推翻**的两条旧结论）。真正跑通的实现目前只在 VS Code 扩展那侧
（[`bastion-vscode`](https://github.com/xingzitian/bastion-vscode)，同样标记为实验性、默认关闭）：
本地测试台全绿，但在**某类堡垒机**上大文件传到 ~95% 会停住，原因暂时定不到应用层。
**欢迎高手接手**——探针、测试台、失败现象的字节级日志都在仓库里。

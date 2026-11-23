# BastionShell 桌面版更新记录

## 0.9.3（2026-11-23）

这一版**界面没动**，改的是后端与打包：桌面窗口里的 React 界面和以前一样，新增能力都是"背后的"。

### 新增：CLI 宿主入口（后端）

桌面后端多了 5 条路由，用来让 CLI 宿主（同一份 `src/core`）为界面提供终端：

| 路由 | 作用 |
|---|---|
`GET /api/shell/cli?profile=<档案>` | 起 CLI 宿主（单条会话），302 跳到它的网页终端 |
`GET /api/shell/cli?profiles=a,b` | 会话组：多条会话共用一个 MCP 端点 |
`POST /api/shell/cli/start` | 带密码启动（密码经子进程 **stdin**，不进 argv、不落盘） |
`POST /api/shell/cli/stop` | 收掉 CLI 子进程 |
`GET /api/shell/endpoint` | 当前薄壳状态：termUrl / mcpUrl / token / **pid** / writeError |
`GET /api/shell/fs-probe` | 诊断用：从应用进程视角逐个目录试写（排查权限问题） |

界面上对应两个入口：档案行的 **⚡**（单条）、侧边栏的 **🗂 组**（多条）。
顶栏会显示 MCP 地址，并支持"复制客户端配置"。

### 新增：headless 模式

`BASTIONSHELL_HEADLESS=1` 时只起后端 + MCP，不开窗口 —— 用于没有桌面环境时验证 HTTP 链路。

### 修复

- **子进程不再抖黑窗**：spawn CLI 时使用 `HideWindow + CREATE_NO_WINDOW`
  （之前每点一次 ⚡ / 每次起会话组会闪一个控制台窗口）。
- **不再留孤儿 node**：CLI 带 `--parent-pid`，桌面被强杀后 5 秒内自行退出
  （之前强杀桌面会留下一堆 `node out/cli/main.js serve`）。
- **CLI 入口自动探测**：环境变量 → exe 同级 `bastion-cli/out/cli/main.js` → 往上 3 层找仓库。
  找不到时报错会**列出试过的每个路径**。
- **主机指纹校验**（CLI 侧）：默认按 `known_hosts` 的 TOFU 语义，指纹不一致直接拒绝连接。

### 打包

桌面发行包内置 `bastion-cli/`（约 10MB，**不含 Node 运行时**），换台机器只需 Node ≥ 22。
`npm pack` 出来的 CLI 包约 396KB，可 `npm install -g` 当命令用。

### 已知限制

- `bastion_connect`（AI 新建连接）在 CLI 侧**有意不支持**：连接必须由人在命令行发起。
- 桌面原有功能（多标签 / 分屏 / SFTP / 部署 / 端口转发）仍跑 Go 引擎，行为未变。
- ⚡/🗂 走的是 CLI 宿主，与桌面原会话是两条独立通道。
- 从**工作区目录内**启动本 GUI 会因 WebView2 权限问题报 `800700aa`（详见开发文档），
  发行副本请放在工作区之外。

## 0.9.2（2026-10-22）

- 同步 elicitation 门槛（没声明的客户端不给 InputRequiredResult）。

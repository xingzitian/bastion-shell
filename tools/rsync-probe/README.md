# rsync 传输桥探针（rsync over pty）

> 🚧 **状态：未完成（研究材料，欢迎接手）**
>
> - **本地**已经证明能跑通（真客户端 + `-e` 桥 + pty 上的 `rsync --server`，两个协议版本，含 512MB）；
>   真正跑通的实现现在只存在于 VS Code 扩展那侧
>   （[`bastion-vscode`](https://github.com/xingzitian/bastion-vscode)，实验性、默认关闭）。
> - **没跑通的是堡垒机那一腿**：真机上大文件传到 **~95%（最后几 MB）会停住**，远端不再回数据。
>   应用层查不出原因（同一文件走 rz 一直正常），怀疑在堡垒机代理对 pty 上持续双向流量的处理。
> - 这个目录是**当时用来把问题一段段二分**的探针与结论，含四轮实测的原始输出（`RESULTS.md`）。
>   桌面版（Wails）**没有实现过这个功能**，前端 `SyncDialog` 后面是空的。
> - 有结论请开 issue，附上扩展日志里每 3 秒一条的 `C2R/R2C` 字节数 —— 它正好能区分
>   「客户端不再发」和「远端不再收」。

**这是什么**：把「经堡垒机会话跑 rsync 目录同步」这件事的**调查工具**和**结论**，从废弃的 Electron 目录搬进有 git 的仓库。

**为什么搬**：当年（Electron 版）真机测试是通的；Wails 版前端还留着 `SyncDialog`，但 Go 侧**一行实现都没有**（`window.bastion.syncDirectory` 在后端不存在）。能力就这样丢了，而最值钱的那条结论只写在 `bridge_server.py` 的 docstring 里 —— 而那个目录**不在任何 git 仓库里**（`shell_tools` 下只有 `bastion-vscode` 和 `bastion-wails` 是 git 仓库）。壳一换，钥匙就留在旧房子里了。

---

## 一、最值钱的结论：根因

原文（`probes/bridge_server.py` 顶部，未改动）：

> **死锁根因**：rsync 3.2+ 的选项串 `-logDtpre.iLsfxCIvu` 里的 **'v'** 触发
> `CF_VARINT_FLIST_FLAGS` + `do_negotiated_strings`（vstring 协商），在手动桥接场景卡死。
> 注入时去掉 'v' → 服务端不设该位 → 客户端读到 compat_flags 无此位 → 双方都不做 vstring，
> 等效于老协议（生产目标机 rsync 3.1.x 本就没有该协商）。

另外两条（来自 `reference/engine.ts` 的头注释）：

1. rsync 3.2+ 握手是**二进制** `write_int(协议号)`，不是 `@RSYNCD:` 文本；
2. 用 `__RSYNC_DONE_<rc>__` 标记判定接收端退出码（发送端等第二次 `NDX_DONE` 是
   sender/generator 多进程细节，不影响正确性）→ 干净退出 + 会话保留。

> ⚠️ **这条根因已经被 2026-09-11 的本地实测推翻**：真正的杀手不是 `v`，而是
> **协议开始之前混进来的终端噪声**（`\x1b[?2004l\r\n`，bash/readline 关掉括号粘贴时吐的），
> 客户端把它当成协议版本 → `protocol version mismatch -- is your shell clean?`。
> 修法是**按握手形状找到协议起点**（字节 ∈ `0x1c..0x22` 且后面跟 `00 00 00`）再开始转发；
> 噪声正好 10 字节，Ubuntu 3.2.7（协议 31）和 AlmaLinux 3.4.4（协议 32）都一样。
> **剥不剥 `v` 与成败无关**（带 `v` 也跑通了）。详见 `RESULTS.md` 的 G1–G6。
> 引用旧结论之前请先看那份记录 —— 它是「当年的一条说法」，**今天已被证伪**。

### ⚠️ 旧结论里已经被实测推翻的一条

`bridge_server.py` 和 `engine.ts` 都写着「远端 3.1.x 是协议 31、**3.2+ 是 32**」。
本机实测（见 `RESULTS.md`）：

| 环境 | rsync | 自称协议号 |
|---|---|---|
| Ubuntu WSL | 3.2.7 | **31** |
| AlmaLinux-10 WSL | 3.4.4 | 32 |

**所以「3.2+ ⇒ 协议 32」不成立**（3.2.7 就是 31）。协议号必须**实测**，不能按版本号推 ——
`engine.ts` 里用 `findGreetingStart()` 去流里认协议号字节是对的，别改成按版本号猜。

---

## 二、每只探针证明什么

**全部不需要堡垒机/真机** —— 它们用本机 rsync 在管道或 pty 上模拟「目标机那一侧」。
这正是它们的价值：把「当年在真机上偶然通过」变成「本地随时可重跑」。

| 文件 | 干什么 | 证明了什么 |
|---|---|---|
| `probes/rsync_pipe_control.py` | 两个 `rsync --server` 走**干净双向管道**（无 pty），**带 `v`** | 对照组：协议本身没问题。若这里都失败，问题不在堡垒机 |
| `probes/rsync_spike_stage1.py` | 「远」端跑在 **raw pty** 上（无 shell），**剥掉 `v`** | rsync 二进制协议能不能在 pty 上活下来 |
| `probes/rsync_spike_stage1b.py` | 同上，但把**首字节 dump 出来** | pty 上到底从哪个字节开始坏（排错用） |
| `probes/rsync_spike.py` | 完整模拟：raw pty + 握手 + 完成标记 | 更接近「菜单堡垒机 → 目标机 shell」的形状 |
| `probes/bridge_server.py` | v4：协议 32 + **剥 `v`**，走 unix socket | 根因的修复方案（**根因就写在这里**） |
| `probes/bridge_client.py` | `rsync -e` 的 rsh 角色桥客户端 | 怎么把 rsync 的 stdio 接到桥上 |
| `probes/logrsh.sh` | 记录型 rsh：把 rsync 真正让远端执行的命令写进 `/tmp/cmd.txt` | **诊断利器**：rsync 到底发了什么 `--server` 选项串（选项串会随版本变） |
| `probes/rsync_win_smoke.js` | Windows 侧 `%LOCALAPPDATA%\rsync\rsync.exe` + node 桥 | 本机 Windows 那一腿能不能跑通 |
| `probes/vflag_matrix.py` | **新增**：`v` 标志 × 传输方式（管道/pty）四格对照矩阵 | 想验证根因：不剥 `v` 时 pty 那两格是否卡住。⚠️ **第一次实测的答案是「六格全失败、没有一格卡住」——没复现出根因，反而暴露出这套搭法本身有问题**（见 `RESULTS.md`） |
| `probes/pty_inject_smoke.py` | **新增**：开 pty → 起 bash → 写一行命令，看有没有回显/执行 | 把「注入机制」和「rsync」分开。实测：两个发行版都**正常**，所以卡死不是终端换行翻译的问题 |

`reference/engine.ts` 是当年**唯一能跑通**的传输桥实现，逐字节搬运，未改动。
它不属于探针，但它是钥匙本身，所以一起搬过来了。

---

## 三、怎么跑（本地 / WSL）

前置：某个发行版里装了 `rsync`。

> ⚠️ **当前默认的 Debian 发行版里没有 rsync**（`command -v rsync` 为空）。
> Ubuntu（3.2.7）和 AlmaLinux-10（3.4.4）里有。当年说明写的是「本机 WSL 已装 rsync」——
> 环境已经变了，这本身就是「结论会随环境失效」的例子。

```bash
# 单跑（会自动记录环境）—— 把 <repo> 换成你自己的仓库路径（WSL 里看 Windows 盘是 /mnt/c/...）
wsl -d Ubuntu          -- bash /mnt/c/<repo>/tools/rsync-probe/run-local.sh
wsl -d AlmaLinux-10    -- bash /mnt/c/<repo>/tools/rsync-probe/run-local.sh
```

结果写进 `RESULTS.md`（带发行版、rsync 版本、协议号、每个探针的 rc 与 match）。

---

## 四、还没证明的部分（诚实清单）

**本地这一半已经证明完了**（2026-09-11，见 `RESULTS.md` 第三轮）：真客户端 + `-e` 桥 +
管道/raw pty 上的交互式 bash + `rsync --server`，**两个 rsync 版本（3.2.7/31、3.4.4/32）
全部跑通**：文件校验一致、客户端 `rc=0`、远端退出码 0。让本地跑通的是两件事（都跟 `v` 无关）：

1. **按握手形状找到协议起点** —— 丢掉 `\x1b[?2004l\r\n` 这 10 字节终端噪声（bash/readline 关括号粘贴时吐的）；
2. **收尾要传下去** —— 桥的 shim 在对面结束时必须**自己退出**，否则 rsync 等子进程、子进程等 rsync，双方互等。

还剩这些**本地证明不了**：

1. **堡垒机那一腿。** 真实链路是：
   `本机 → ssh 到堡垒机 → 会话内过菜单 → 目标机 shell(pty) → 注入 rsync --server`。
   本地只能模拟**最后那一段 pty**。菜单注入、堡垒机对二进制流的清洗/审计/超时策略、
   目标机的 rsync 版本，全都得真机。
2. **Windows 侧 rsync.exe 当客户端的那一跑。** 真机上客户端是
   `%LOCALAPPDATA%\rsync\rsync.exe`（**3.5.0**，比两个 WSL 里的都新）。
   `probes/rsync_win_smoke.js` 是旧写法，还没按上面那两条修法更新 —— 这条迟早要补，
   因为真机上跑的就是它。
3. 「目标机 rsync 版本会不会又变出新花样」—— 只能靠真机 + 每次记录版本号（这就是 `RESULTS.md` 的用途）。
4. rsync 3.2.7 报协议 31 这件事说明：**旧笔记里的版本推断不可信**，一律实测。

---

## 五、那条没走通的「隧道」（当年提过，最后没做出来）

思路：**别在 pty 上跑 rsync 协议**。用 SSH 端口转发从堡垒机开一条到目标机 `22` 的**干净 TCP 通道**，
本地起监听，然后让 rsync 走这条隧道 —— 直接字节流，没有 pty、没有 CR/LF 转换、没有 vstring 死锁。

- 现成的一半：端口转发我们已经有了（`bastion-vscode/src/forward.ts` 用 `conn.rawClient.forwardOut`），
  客户端这一侧不用从零写。
- **以下三条是待验证的假设，不是结论**（没有实机数据，我不写死）：
  1. 堡垒机**是否允许** `forwardOut` 到目标机的 22（多数堡垒机策略上是禁的）；
  2. 选完菜单后，我们**怎么知道目标机地址**（会话输出里能拿到，但需要可靠解析）；
  3. 这条隧道上的认证：rsync 走的是「堡垒机 → 目标机」的 TCP，它拿什么身份进目标机 —— 
     复用堡垒机会话身份？还是要另外的凭据？**必须实测**。

如果隧道能走通，它比 pty 注入干净得多，也就不用维护「剥 `v`」这类版本相关的手艺 ——
但**在拿到真机数据之前，这只是个假设**。

---

## 六、搬运记录

- 源：`shell_tools/bastion-shell/`（Electron 版，已废弃，**不在任何 git 仓库里**）
- 搬运时间：2026-09-11
- 方式：`Copy-Item` 逐字节复制 + SHA256 双端核对（9 个文件全部一致），**未改一字**
- 新增（不是搬运的）：`README.md`、`RESULTS.md`、`run-local.sh`、`probes/vflag_matrix.py`
- 前提没变：**真机那一腿仍然只能真机测**，这里的价值是把「不用真机就能验的那半」固定下来

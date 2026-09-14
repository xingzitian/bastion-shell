# 实测记录（RESULTS）

每次实跑都把**环境**和**原始输出**记在这里。结论跟环境绑定，所以环境必须和结论写在一起。
判读时请把「事实」和「推断」分开看 —— 下面分成两块写。

---

## 2026-09-11 第一次本地实测（WSL，无堡垒机）

### 环境（这一栏是事实）

| 位置 | rsync | 自称协议号 | 备注 |
|---|---|---|---|
| Windows（真实链路里的**本地端**） | **3.5.0-gc62c545f** | 32 | `%LOCALAPPDATA%\rsync\rsync.exe`，826 KB，2026-08-30 |
| Ubuntu 24.04 WSL | 3.2.7 | **31** | |
| AlmaLinux 10.2 WSL | 3.4.4 | 32 | |
| Debian WSL（**当前默认发行版**） | **没装** | — | 当年说明写「本机 WSL 已装 rsync」——环境已经变了 |
| 内核 | 6.18.33.2-microsoft-standard-WSL2 | | |

> 顺带修正一条旧笔记：`bridge_server.py` / `engine.ts` 都写「3.1.x 是协议 31、**3.2+ 是 32**」。
> 实测 **3.2.7 报的是 31**。所以「版本号 ⇒ 协议号」不成立，协议号要实测
> （`engine.ts` 用 `findGreetingStart()` 去流里认字节是对的，别改成按版本推）。

### 跑法

```bash
wsl -d Ubuntu       -- bash tools/rsync-probe/run-local.sh
wsl -d AlmaLinux-10 -- bash tools/rsync-probe/run-local.sh
```

### 原始输出（Ubuntu 3.2.7 与 AlmaLinux 3.4.4 **表现完全一致**）

**探针 1：`rsync_pipe_control.py`（号称的「干净管道对照组」，带 `v`）**

```
local_rc= 4
local_stderr= over-long vstring received (511 > 255)
rsync error: requested action not supported (code 4) at compat.c(405) [Receiver=3.2.7]
remote_stderr= over-long vstring received (511 > 255)   ... [sender=3.2.7]
dst big.bin missing
```

**探针 2：`rsync_spike_stage1.py`（raw pty，剥掉 `v`）**

```
rc=12 remote_poll=12 remote_to_local=130 local_to_remote=9
local_stderr= unexpected tag -100 [Receiver/inc]
rsync error: error in rsync protocol data stream (code 12) at io.c(1704) [Receiver=3.2.7]
```

**探针 3：`vflag_matrix.py`（`v` × 传输方式 六格）**

| 选项串 | 传输 | rc | 耗时 | 错误 |
|---|---|---|---|---|
| full `Ivu` | pipe | 4 | 0.1s | over-long vstring (511 > 255) |
| full `Ivu` | **pty** | 4 | 0.1s | 同上 |
| no_v `Iu` | pipe | 12 | 0.1s | error in rsync protocol data stream |
| no_v `Iu` | **pty** | 12 | 0.1s | 同上 |
| no_vu `I` | pipe | 12 | 0.1s | 同上 |
| no_vu `I` | **pty** | 12 | 0.1s | 同上 |

**六格全失败、全部 0.1 秒内失败、没有任何一格卡住**；`MATCH` 六格全 False。

**探针 4：`bridge_server.py`（真实客户端 `rsync -a -e 'python3 bridge_client.py'` + pty 上的 bash + 剥 `v` + 完成标记）**

```
REMOTE_CMD= rsync --server -logDtpre.iLsfxCIvu . /tmp/rs-dst/     ← 客户端确实发了带 v 的选项串
LEFT_OVER_HEX=                                                    ← 客户端发完命令后没有握手字节
FORCED_CMD= rsync --server -logDtpre.iLsfxCIu . /tmp/rs-dst/      ← 已按设计剥掉 v
LOCAL_RC= TIMEOUT => FINAL_RC= TIMEOUT
fwd_bytes= 0 l2r_bytes= 0 mark_found= False
REMOTE_STDERR= (空)   STTY_STATE=(no file)
```

**探针 5：`pty_inject_smoke.py`（新增；把「注入机制」和「rsync」分开）**

```
BANNER=b'bash: cannot set terminal process group ... no job control in this shell ...'
CR_ECHO_SEEN=True CR_RUN_SEEN=True
CRLF_ECHO_SEEN=True CRLF_RUN_SEEN=True
```

两个发行版结果相同 → **pty 注入机制本身没问题**（写 `\r` 就能提交，bash 会执行，连括号粘贴模式都在）。

---

### 事实（能直接下结论的）

- **F1**：`rsync_pipe_control.py` 是当年用来当「对照组」的探针，按设计它应当**通过**。
  今天在 3.2.7 和 3.4.4 上它**失败**（code 4，over-long vstring）。→ 这套探针的**基线不成立**了。
- **F2**：文档里那套「选项串带 `v` → vstring 协商 → **卡死**」的**症状没有复现**：
  带 `v` 得到的是**立刻报错**（code 4），不是超时。
- **F3**：「剥掉 `v` 就能跑通」这条修复结论也**没有复现**：剥了之后变成另一种错误（code 12，protocol data stream）。
- **F4**：真实的桥（探针 4）今天**卡死**，但卡在**协议开始之前**：两个方向 0 字节，
  连 `2>/tmp/rsync_remote_err.txt` 这个重定向文件都没建 → 注入的那条命令**根本没被执行**。
- **F5**：注入机制本身没问题（探针 5）→ F4 的卡死**不是**「\r 提交不了」这类终端问题。
- **F6**：真实链路里的本地端 rsync 已经是 **3.5.0**，比两个 WSL 里的都新。
  也就是说**连本地端都换过版本了**。

### 推断（标清楚，别当结论用）

- **H1**：探针 1/3 的失败很可能是**搭法本身的问题** —— 它们用「两头都是 `rsync --server`」
  来模拟一对 rsync。真实场景是一端**真客户端**、一端 `--server`。两头都 `--server` 时，
  vstring 协商/握手没有真正的一方去对齐，于是报 `over-long vstring` / `unexpected tag`。
  → 也就是说：**这些年「通过/不通过」可能一直测的不是堡垒机，而是这个搭法**。
- **H2**：探针 4 的 0 字节，最可能是**桥自己的等待顺序**问题：
  客户端 `-e` 发了命令后没有立刻写握手字节（`LEFT_OVER_HEX` 为空），
  而桥可能在等握手才注入命令 → 双方对等互等。**这只是推测，需要按探针 5 的办法继续二分。**
- **H3**：当年真机「通过」可能来自**真机那条路径**（Windows 3.5.0 客户端 + 目标机 rsync + 堡垒机 pty），
  而不是这套本地探针 —— 所以本地复现不出来，不代表当年那份真机证据是假的。**两件事不能互相否定。**

### 这次实测改变了什么

1. 不能再引用「剥掉 `v` 就好了」当结论 —— 它今天在本地**不成立**，也从没被写成可重跑的断言。
2. 「本机 WSL 已装 rsync」这类前提会过期（Debian 里已经没有了）。
3. **这些脚本是探针，不是测试**：没有断言、没有基线、环境一变结论就失效，而且**不会有任何东西变红**。
   这次能发现，纯粹是因为把「应当通过」的探针 1 当成断言跑了一遍 —— 它红了。
4. 要回答「钥匙还在不在」，必须先把**探针 4 的 0 字节**查清楚（本地就能做），
   或者改走**隧道**那条路（`forwardOut`，见 README 第五节）。

### 下一步（本地可做，不需要真机）

- 二分探针 4：先只跑「真客户端 + 干净管道」（不要 pty），确认桥在管道上是否能通；
  再只跑「真客户端 + pty」。
  - 管道能通、pty 不通 → 问题定位在 pty（那就回到 `stty`/二进制透传那条线，或者干脆走隧道）；
  - 管道也不通 → 桥的客户端/握手顺序有问题，与堡垒机无关。

---

## 2026-09-11 第二轮：路线 A（真客户端二分）—— 根因定位并修正

新仪器：`probes/bisect_server.py` + `probes/bisect_shim.py`。
和旧脚本的关键区别：**一端是真客户端**（`rsync -a -e <shim>`），另一端可在
「干净管道 / raw pty」之间切换 —— 旧脚本从来没把这两个变量分开过。
跑法：`bash run-bisect.sh` / `bash run-bisect2.sh`。

### 结果矩阵（Ubuntu 3.2.7；AlmaLinux 3.4.4 见下方 G3）

| 传输 | 远端怎么起 | 前导噪声过滤 | 结果 | 文件 |
|---|---|---|---|---|
| pipe | — | — | `TIMEOUT`（客户端不退出） | **MATCH=True** |
| pipe（剥 `v`） | — | — | `TIMEOUT` | **MATCH=True** |
| pty | 交互式 bash（注入） | — | `rc=2` `protocol version mismatch -- is your shell clean?` | False |
| pty | 交互式 bash | 有（**当时有 bug**） | `rc=2` 同上 | False |
| **pty** | **非交互 exec**（无 readline） | — | `TIMEOUT` | **MATCH=True** |
| **pty** | 非交互 exec（剥 `v`） | — | `TIMEOUT` | **MATCH=True** |
| **pty** | **交互式 bash（堡垒机的形状）** | 修好后 | `TIMEOUT` `mark=True`（远端 rc=0） | **MATCH=True** |

### 事实

- **G1｜pty 本身没问题。** `--shell exec`（pty 上直接 exec rsync，没有交互式 shell、
  没有 readline）**两个选项串都把文件传完了**（`MATCH=True`）。
  → 「rsync 的二进制协议过不了 pty」这个说法**是错的**。
- **G2｜真凶是协议开始之前的终端噪声。** 客户端最先收到的不是协议，而是
  `\x1b[?2004l\r\n` —— bash/readline **关掉括号粘贴模式**时吐的控制序列。
  客户端把它当成协议版本号，于是报出 rsync 自己那句诊断：
  `protocol version mismatch -- is your shell clean?`（这句报错一直摆在那儿，就是答案）。
- **G3｜修法：按「握手形状」找协议起点。** 不是"丢掉像噪声的字符"，而是扫描到
  「字节 ∈ `0x1c..0x22` 且后面跟 `00 00 00`」——rsync 的二进制握手开头——再开始转发。
  实测噪声正好 **10 字节**，Ubuntu（协议 31）和 AlmaLinux（协议 **32**）**都是 10 字节**，
  修好后两边都 `MATCH=True` + `mark=True`（远端退出码 0）。
- **G4｜`v` 与成败无关。** exec 模式下带 `v` / 剥 `v` 都成功；交互式 bash + 过滤那格
  **也没有剥 `v`**，同样成功。→ 旧结论「剥 `v` 是钥匙」**在本地被证伪**（至少 3.2.7 / 3.4.4 上）。
- **G5｜我自己的两个 filter bug，都源于"按值猜噪声"**：
  1. CSI 结束字节只在 `0x40..0x7e` 里找 → `[`(0x5b) 自己就"结束"了序列，
     于是 `\x1b[?2004l` 被处理成留下 `?2004l`；
  2. 把 `0x20` 当空白丢掉 → **吃掉了协议号 32**（AlmaLinux 3.4.4 报协议 32 = `0x20`，
     和空格一模一样）。
  → 教训：**不能按字节值猜噪声，必须按协议形状认**。
  （`reference/engine.ts` 里的 `findGreetingStart()` 就是这个思路 —— 当年已经懂了，
  只是没变成可重跑的东西。）
- **G6｜还没解决：客户端不退出。** 每一格成功的都是 `TIMEOUT`，但**文件已 `MATCH=True`、
  远端 rsync 已 `rc=0`（`mark=True` 收到）**。所以这是「收尾/再见」的问题，不是传输问题。
  `engine.ts` 里那句「发送端等**第二次** `NDX_DONE` 是 sender/generator 多进程细节」
  说的正是这块。

### 推断（标清楚）

- **H4｜旧记录里「两个方向 0 字节的死锁」观测本身不可靠。** 我这边同样的 pty 形状能拿到数据；
  而我自己的仪器就犯过两个会导致「0 字节 / 卡住」的错（socket 不能用 `os.write`、EOF 不传下去）。
  所以与其说当年是"协议死锁"，不如先怀疑**仪器**。要坐实得重读当年那份 `engine.ts` 并逐行复跑，
  目前只能标成推断。

### 这一轮改变了什么

1. 根因从「剥 `v`」改成「**协议起点被终端噪声污染 + 收尾没有 EOF**」，两条都可本地复现、可断言。
2. **本机（WSL，两个 rsync 版本）上已经能端到端跑通 rsync over pty** —— 不需要堡垒机。
   也就是说：剩下没验的只有「堡垒机菜单注入 + 堡垒机对流的处理 + 认证」这一腿。
3. 旧的「探针」升级成了至少**可重复的实验**：一格一个结论，环境写在输出里。

---

## 2026-09-11 第三轮：G6（客户端不退出）—— 现场快照定位并修好

### 现场快照（超时不杀，先拍现场）

`--transport pipe` 超时那一刻：

```
SNAP[remote] pid=385 已退出 rc=0                             ← 接收端早就干净退出了
SNAP[client] pid=382 State: S (sleeping) wchan=hrtimer_nanosleep
  FDS: 0->pipe  1->/dev/null  2->pipe  4->socket  5->socket
  CHILD pid=383 State: S (sleeping) wchan=poll_schedule_timeout
    CMD: python3 bisect_shim.py host rsync --server ... . <dst>/
    FDS: 0->socket  2->pipe  3->socket                      ← 注意：**没有 fd 1**（stdout 已关）
```

→ **rsync 在等它的 rsh 子进程（shim），shim 在等 rsync 关 stdin** —— 收尾时双方互等，谁也不退。

### 关键对照：`--transport local`（没有 python shim、没有 unix socket）

`-e` 指向一个只做 `exec rsync --server <opts> . <dst>/` 的脚本：

```
RESULT=rc=0 secs=0.1
MATCH=True
CLIENT_STDERR=(空)
```

→ **rsync 本身完全正常**（真客户端 + 本地 `--server`，0.1 秒干净退出）。
所以「客户端不退出」**100% 是我们自己那层（python shim + unix socket + 泵）的问题**，
跟 rsync、跟 pty、跟堡垒机都无关。

### 修法

`bisect_shim.py`：**对面结束时，shim 自己也要结束**（真实 `ssh` 就是这个行为）——
不只是关掉 stdout 让 rsync 读到 EOF，进程本身必须退出，否则 rsync 会一直等这个子进程。

### 修好后的完整矩阵（两台发行版、两个 rsync 版本）

| transport | Ubuntu 3.2.7（协议 31） | AlmaLinux 3.4.4（协议 32） |
|---|---|---|
| `local`（无 shim/socket 对照） | `rc=0` `MATCH=True` | `rc=0` `MATCH=True` |
| `pipe` | `rc=0` `MATCH=True`（0.3s） | `rc=0` `MATCH=True` |
| `pipe --opts nov`（剥 `v`） | `rc=0` `MATCH=True` | `rc=0` `MATCH=True` |
| `pty --shell exec` | `rc=0` `MATCH=True` | `rc=0` `MATCH=True` |
| **`pty --shell bash --filter-prefix`（堡垒机的形状）** | **`rc=0` `MATCH=True` `mark=True`** | **`rc=0` `MATCH=True` `mark=True`** |

**全部 5 格 × 2 环境 = 10 格通过**，每格 0.3 秒；`PREFIX_DROPPED=10 bytes`（两个版本一致的噪声长度）；
交互式 bash 那格**没有剥 `v`**。

### 事实

- **G7｜本地这一条链已经端到端跑通**：真客户端 → `-e` shim → socket →（管道 / raw pty 上的
  交互式 bash）→ `rsync --server`，文件校验一致、客户端 `rc=0`、远端退出码 0。
- **G8｜让本地跑通的是两件事，都不是 `v`**：
  1. **按握手形状找到协议起点**（丢掉 `\x1b[?2004l\r\n` 那 10 字节终端噪声）；
  2. **收尾要传下去**（shim 在对面结束时自己退出，别和 rsync 互等）。
- **G9｜`local` 对照把责任划清了**：不退出是**我们那层**的问题，不是 rsync / pty 的。

### 还没做的

- 堡垒机那一腿（菜单注入、堡垒机对二进制流的处理、认证）—— **本地测不了**，只能真机。
- `%LOCALAPPDATA%\rsync\rsync.exe` 3.5.0 当**客户端**的那一跑（`rsync_win_smoke.js` 是旧写法，
  还没按这两条修法更新）。真机上客户端就是它，所以这条迟早要补。

---

## 2026-09-11 第四轮：把桥接进 VS Code 扩展，被真机逼出来的三个 bug

扩展里新增「用 rsync 增量同步到当前会话」（右键菜单）。装到真机上**连续三次失败**，
每次都只有真机才暴露得出来。按发现顺序记（**不含任何主机/账号信息**）：

### 真机环境补充（只有版本，不记身份）

| 位置 | rsync | 协议 |
|---|---|---|
| 真实目标机 | **3.1.2** | **31** |
| 本地客户端（Windows） | 3.5.0 | 32 |

→ 客户端 32 / 服务端 31 的**向下协商**是通的（协议阶段确实建立起来了），
说明版本差不是问题；问题全在「这条链路的手艺」上。

### B1｜过滤器接错方向 → `protocol version mismatch -- is your shell clean?`

**现象**：客户端报上面那句，终端里还能看到远端把我们注入的命令行**回显**了出来。
**原因**：我用**同一个过滤器实例喂了两个方向**。客户端自己发的第一批字节就是它的协议版本握手
（`1f/20 00 00 00`），和"远端握手"**长得一模一样**，它先到 → 过滤器被提前置成"已找到握手"
→ 远端那份带终端回显噪声的数据不再被过滤 → 噪声直冲客户端。
（Python 仪器里客户端方向根本没接过滤器，所以从没犯过；**移植时接错了**。）
**修**：过滤器严格单向（只管「通道 → 客户端」）；客户端方向原样转发。
**已钉住**：测试「喂了客户端的握手之后，远端噪声就过滤不掉了」。

### B2｜给「文件」加末尾斜杠 → `change_dir ... Invalid argument (22)`（退出码 23）

**现象**：`[sender] change_dir "…/bundle.tar.gz" failed: Invalid argument (22)`。
**原因**：我给所有源路径无条件加了 `/`（本意是让目录语义明确）。文件带斜杠之后
rsync 会把它当目录去 `change_dir`，Windows 客户端直接 EINVAL。
**复现**：本机用同一个 `rsync.exe`（3.5.0）A/B 对照 —— 带斜杠有错、不带斜杠没有。
**修**：斜杠只给目录加（`dir/` = 同步目录里的内容），文件原样；多选时全部一起给 rsync
（原来还把多选悄悄缩成了第一个）。
**已钉住**：`文件源后面不能有斜杠` 等三条。

### B3｜还没 raw 就放客户端字节 → 远端一个字节都不吐，界面永远卡住

**现象**：日志里只有远端回显（`…PS1=; ` + `rsync`），**之后再无任何 R2C 字节**，一直卡住。
**原因（推断，但机理清楚）**：注入行 `stty raw -echo -iexten; PS1=; rsync --server …` 里，
`stty raw` 生效之前 pty 还是 **cooked**（ICRNL 翻译、控制字符当信号、按行缓冲）。
客户端的第一批协议字节可能正好落在这个窗口里，被当成"下一行输入"搅乱 → 协议再也对不上。
**修**：注入行里加一步「打印 `__BASTION_RSYNC_RAW__`」——远端确认 raw 之后**才放行**
先前扣住的客户端字节。
**顺带修掉一个更难看的 bug**：看门狗原来判的是「有没有收到**任何**字节」，
而远端回显也算字节 → 条件永不成立 → **出问题时界面不报错、干等到 10 分钟**。
现在判「协议有没有真正开始」（过滤器是否找到握手），20 秒给明确结论。
**日志也补了 C2R**（原来只有 R2C，等于只能看见单边）。

**回归**：加了 raw 标记之后，`pty + 交互式 bash + 过滤` 这一格在两个发行版上仍然
`rc=0` `MATCH=True`，并且 `raw_mark=True`；前导噪声从 10 字节变成 25 字节
（多出来的是标记本身）—— 数量对得上。

### B4｜远端 `rsync --server` 立刻退出 rc=1（调查中）

**现象**：raw 握手正常、客户端 4 字节版本也放行了，但远端立刻回
`__BASTION_RSYNC_DONE_1__`，客户端报 `connection unexpectedly closed (0 bytes received so far)`。
rc=1 = 用法/语法错，而远端**一个字节的协议数据都没发**（说明它没走到协议阶段）。

**已排除**（本地实测）：

| 假设 | 结论 |
|---|---|
| 新客户端（3.5.0）的选项串被老服务端（3.1.2）拒绝 | **排除**：连故意塞一个不存在的字母 `Z`（`-ogDtpre.ifxCIvuZ`）3.2.7/3.4.4 都照样往下走协议，只报 data-stream 错，**不是 rc=1** |
| 远端目录 `./` 不行 | **排除**：WSL 里真实客户端 + 本地 `--server` + `host:./` → `rc=0`，文件到位 |
| 注入行本身有语法错/重定向错 | **排除**：新增 `probes/check-inject-line.sh` 在两个发行版跑通，raw 标记 + 完成标记 + stderr 落盘全对 |

**剩下的两个嫌疑**（都会让 rc=1，且输出都在**我们看不到的地方**）：
1. **bash 在重定向阶段失败**（`2>/tmp/...` 写不了）→ bash 直接退出 1，`rsync` 根本没跑；
2. rsync 在目标机上真的报用法错（报错被重定向进了文件，pty 上看不到）。

**这一轮补的诊断**（就是为了让下一次自己说出答案）：
- 注入行改成**先探测 stderr 落哪个文件**（`/tmp` 写不了就退到 `$HOME`）—— 顺手把嫌疑 1 兜住；
- 失败时**自动把远端 stderr `cat` 到用户终端**里（两个候选路径都 cat）；
- 日志里把**协议开始前那段噪声按文本打出来**（远端 shell 的报错全在里面；
  之前只记前 4 块 R2C，真相被截断了 —— 这是我自己的诊断盲区）；
- 失败信息带上**远端退出码**，并记录**实际注入的那一行**。

### 结局：B4 的真凶是 **rc=127 —— 目标机上没装 rsync**

真机日志里给出了答案：

```
远端完成标记 rc=127
-bash: rsync: command not found
```

不是桥的问题（桥的每一步都按设计工作了）。这也说明**"把远端报错捞回来"这条诊断值得**：
它把一句猜不出原因的 `connection unexpectedly closed` 变成了 `command not found`。
（顺带确认：用户先前看到的 `rsync --version → 3.1.2` 是**另一台**目标机。）

### 于是搭了本地端到端测试台（不再靠用户当测试仪）

三个 bug 全都**只有真机才暴露**，而每轮都让用户在自己机器上试一遍代价太高。所以在
`bastion-vscode/tools/rsync-e2e/` 搭了一套：

```
setup-wsl.sh   在 WSL 里起一个只听 127.0.0.1:2222 的 sshd + 测试用户(仅密钥) + rsync
run-e2e.ps1    一条命令：编译 + 跑端到端
src/test/rsyncE2E.test.ts   跑了什么见下
```

**跑的是扩展真正在跑的 `runBridge`**（把会话通道换成 ssh2 的 shell 通道），
客户端是 Windows 上的 `rsync.exe` 3.5.0，远端是 WSL 里的真 sshd（rsync 3.2.7 / 协议 31）：

| 用例 | 结果 |
|---|---|
| 真 SSH + 真 pty：单个文件 | ✔（`remoteRc=0`，sftp 读回内容一致） |
| 真 SSH + 真 pty：整个目录 | ✔（目录里的内容同步过去，子目录也在） |
| 目标机没有 rsync 的场景 | ✔（给出可照做的说法，不再静默卡住） |

**已验证它有牙齿**：把「文件带末尾斜杠」那条 bug 退回去，用例如期变红，且报错与真机一致：

```
change_dir "…/C:/Users/…/a.txt" failed: Invalid argument (22)
rsync error: some files/attrs were not transferred (see previous errors) (code 23)
```

踩到的两个搭台坑（都记在脚本注释里）：`passwd -l` 会让 sshd 直接拒绝
（"account is locked"，连密钥登录都不让）；精简 sshd 配置里不写 `Subsystem sftp` 就没有 sftp
（测试要用它把远端文件读回来校验）。

### 验证矩阵（在测试台上跑，两套协议版本）

`bastion-vscode/src/test/rsyncE2E*.test.ts` + `tools/rsync-e2e/`。客户端是 Windows 上的
`rsync.exe` 3.5.0，远端分别是 WSL 的 Ubuntu（rsync 3.2.7 / **协议 31**）和
AlmaLinux 10（rsync 3.4.4 / **协议 32**）。

| # | 用例 | 协议 31 | 协议 32 |
|---|---|---|---|
| 1 | 单个文件（文件不带末尾斜杠） | ✔ | ✔ |
| 2 | 整个目录（目录带斜杠 = 同步内容，含子目录） | ✔ | ✔ |
| 3 | 本机 rsync 不存在 → 有说法 | ✔ | ✔ |
| 4 | 1MB 随机二进制 **sha256 逐字节一致** | ✔ | ✔ |
| 5 | 8MB（重负载档 64MB）大文件一致 | ✔ | ✔ |
| 6 | 多级目录 + 空目录 | ✔ | ✔ |
| 7 | 中文名 / 空格 / 引号 / 美元符 文件名 | ✔ | ✔ |
| 8 | 空文件（0 字节） | ✔ | ✔ |
| 9 | 多选（两个文件 + 一个目录） | ✔ | ✔ |
| 10 | 远端多级目标目录不存在 → 自动建出来 | ✔ | ✔ |
| 11 | 远端目录不可写 → 报错看得懂 | ✔ | ✔ |
| 12 | 增量：第二次同步只传 0 个文件 | ✔ | ✔ |
| 13 | 连续同步（默认 3 次 / 重负载 10 次） | ✔ | ✔ |
| 14 | 同步后同一条会话仍可执行命令 | ✔ | ✔ |
| 15 | 会话被占用 → 第二次明确拒绝 | ✔ | ✔ |
| 16 | 中途取消 → 会话在 `--timeout` 窗口后恢复 | ✔ | ✔ |
| 17 | 4 条会话并发互不串台 | ✔ | ✔ |

另有 4 项**属性测试**（`rsyncBridgeFuzz.test.ts`，2700 组随机输入）钉住握手过滤器的三个不变量：
精确切在握手处、协议字节逐字节不变、分块与整块结果一致，以及"伪握手不能被骗"。

### 这一轮矩阵抓出并修掉的三个真问题

1. **目标目录不存在 → rsync 直接退 11**（实测）。单个文件的同步要求目标目录已存在。
   → 注入行改成先 `mkdir -p "<目标目录>"`（错误一起写进 stderr 文件）。
2. **会话被占用时，第二次同步会把正在跑的通道抢掉**（表现为第一次传输卡死 120 秒）。
   原因：`acquire()` 失败时收尾**照样**调了 `term.release()`，把别人手里的通道放了。
   → 加 `ownsChannel`：只有自己占上的才归还；没占上只清自己的 TCP 环回/子进程/槽位。
3. **取消之后远端 `rsync --server` 一直挂着读 pty，而 pty 还是 raw（ISIG 关掉）**
   → 整条会话看起来"冻住"（敲什么都没反应）。实测 **OpenSSH 不认客户端发的 signal 请求**
   （`stream.signal('INT')` 毫无作用），所以光靠信号不行。
   → 改用客户端侧 `--timeout=N`：我们不再喂数据后，远端静默 N 秒自己退出 →
   注入行里的 `stty sane` 才跑得到 → 会话恢复。另给远端套了一层 `timeout 1800` 兜底
   （扩展进程万一没了，也不会留一个永远挂着的 rsync）。

同样重要的是**三个真问题里有两个是"只有在真机上才现形"的类型** —— 这正是这套测试台存在的理由：
现在它们会先在本地变红，而不是让维护者在生产会话里撞上。

### 最终验证结论（2026-09-11 夜）

| 项 | 结果 |
|---|---|
| 验证矩阵 17 项 × 协议 31（rsync 3.2.7） | **17/17 通过** |
| 验证矩阵 17 项 × 协议 32（rsync 3.4.4） | **17/17 通过** |
| 重复 3 轮 × 2 协议 × 17 项 | **102 次执行，0 失败**（无抖动） |
| 全量测试（399 项，含 17 项端到端）× 3 轮 | **3 轮全绿** |
| 属性测试（握手过滤器，2700 组随机输入） | **通过**（精确切在握手处 / 协议字节逐字节不变 / 分块与整块一致 / 伪握手不被骗） |
| 大文件压力：**512MB** 随机二进制（两协议各一遍） | **通过，sha256 一致** |
| 大文件吞吐（pty 通道，loopback） | 512MB ≈ **108–110 秒 ≈ 4.7 MB/s**（pty + 我们那层转发的开销；不是 rsync 本身的极限） |
| 脱敏扫描（vsix / 源码 / 文档 / 测试 / 探针目录） | **0 命中**（并把两处与真机主机名相似的测试fixture改成了中性名字） |

已知的、**本地测不到**的只剩堡垒机那一腿：菜单注入、堡垒机对二进制流的清洗/审计/超时策略、
目标机的 rsync 版本（真机上目标机是 3.1.2 / 协议 31，属于本次已验证的协议号之一）。

性能那条值得单独记一笔：4.7 MB/s 是**当前实现**在 pty 上的实测值，瓶颈在 pty 与逐块转发，
不是 rsync。如果哪天要传几百 MB 的包，这里还有明确的优化空间（合并写、调大块大小），
但在"增量同步目录"这个主场景下够用（没变的内容根本不传）。

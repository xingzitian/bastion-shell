# trzsz-go 本地补丁说明

**这个目录不是原样 vendor，里面有本地改动。** 升级上游时请把下面的补丁重新应用一遍。

- 上游：https://github.com/trzsz/trzsz-go
- 基线版本：**v1.2.0**（`go.mod` 里 `require github.com/trzsz/trzsz-go v1.2.0` + `replace` 指向本目录）
- 许可：MIT（见同目录 `LICENSE`）

## 为什么需要补丁

BastionShell 是 GUI 程序（Wails + WebView2）。`rz` / `sz` 是**控制台程序**，
从 GUI 进程启动它们时会**弹出一个黑色控制台窗口**，传文件时闪一下很难看。
上游没有提供隐藏窗口的开关，所以本地加了一个。

## 改了什么（共 3 个文件）

| 文件 | 改动 |
|---|---|
| `trzsz/hide_windows.go` | **新增**。Windows 下把子进程的 `SysProcAttr.HideWindow` 置为 true |
| `trzsz/hide_other.go` | **新增**。非 Windows 平台的空实现（保证跨平台可编译） |
| `trzsz/zmodem.go` | **加一行**：启动 sz/rz 子进程处调用 `hideWindow(cmd)` |

## 怎么核对

和上游逐文件比对（用你自己机器的模块缓存路径替换 `$GOMODCACHE`）：

```bash
# 拿到上游解包后的源码
go mod download github.com/trzsz/trzsz-go@v1.2.0
# 逐文件比对，应当只有上面这 3 个文件有差异
diff -r "$GOMODCACHE/github.com/trzsz/trzsz-go@v1.2.0/trzsz" trzsz
```

## 更好的做法（以后可以升级到）

1. 把这 3 个文件提到上游（PR 一个 `HideWindow` 选项），上游合并后就能去掉 `replace`，直接用 module 依赖；
2. 或者在 GitHub 上 fork 一份自己维护，`replace` 指向 fork 的 tag —— 这样补丁是可 review、可追溯的，也不用把 61 个上游文件塞进本仓库。

## 另外

本目录还带着上游的打包/CI 文件（`.github/`、`debian/`、`.goreleaser.yaml`、`Makefile`），
对构建没用（`.github/workflows` 嵌套在这里不会被 GitHub 执行），只是噪音，可以删。

package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 传输结果的**校验**：远端读到了什么、这次到底成没成。
//
// 移植自 VS Code 扩展的 src/uploadVerify.ts，判据一字不差。两条硬规矩：
//
// 1. **绝对不要用 mtime 判断成功与否**（扩展侧 2026-09-14 踩过这个坑）：
//    ZMODEM 会把**源文件的 mtime 一起传过去**（ZFILE 帧里带），所以传完之后
//    远端文件的 mtime 就等于本机源文件的 mtime。曾经写过一版「mtime 太旧 → 判定失败」，
//    结果**凡是源文件几分钟没改过就误报**。
// 2. 唯一可靠的依据是**内容哈希**；拿不到哈希才退化成「大小一致」这个弱判据，
//    而且必须如实说明「为什么算不出哈希」。

// remoteFileInfo 远端一个文件的信息
type remoteFileInfo struct {
	Hash     string
	Size     int64
	HasSize  bool
	MTime    int64
	HasMTime bool
	Path     string
	Readable bool
}

// md5Hex 小写 hex 的 md5
func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

var reGlobSafe = regexp.MustCompile(`^[A-Za-z0-9._\x{4e00}-\x{9fa5}-]{1,120}$`)

// globSafe 文件名是否安全到可以拼进远端 glob（有特殊字符就跳过回读校验，不硬拼）
func globSafe(name string) bool {
	return reGlobSafe.MatchString(name)
}

// remoteInfoCommand 读远端文件信息的远端命令。
//
// ⚠️ 这里**刻意不依赖 awk/cut 之类的外部工具**（扩展侧 2026-09-14 真机教训）：
// 远端回读时 `md5sum | awk '{print $1}'` 什么都没吐出来（哈希变成 NA），
// 于是只能退化成「按大小判断」的弱证据 —— 而 awk 从来不在能力探测里。
// 现在改用 shell 自己的参数展开（`${h%% *}`），零外部依赖。
// 同时多回一个 `readable`：文件不可读时算不出哈希，要能区分「没工具」和「没权限」。
func remoteInfoCommand(shellWord string) string {
	return `for f in ` + shellWord + `; do [ -f "$f" ] || continue; ` +
		`r=$([ -r "$f" ] && echo yes || echo no); ` +
		`h=''; if [ "$r" = yes ]; then ` +
		`h=$(md5sum "$f" 2>/dev/null); h=${h%% *}; ` +
		`[ -n "$h" ] || { h=$(sha256sum "$f" 2>/dev/null); h=${h%% *}; } ` +
		`fi; ` +
		`[ -n "$h" ] || h=NA; ` +
		`s=$(stat -c %s "$f" 2>/dev/null || wc -c < "$f" 2>/dev/null || echo NA); ` +
		`m=$(stat -c %Y "$f" 2>/dev/null || echo NA); ` +
		`printf '%s|%s|%s|%s|%s\n' "$h" "$s" "$m" "$r" "$f"; done`
}

// remoteInfo 读远端文件信息：哈希（md5，退 sha256）+ 大小 + mtime + 路径 + 可读性。
// shellWord 必须**已经由调用方做好 shell 引用**（这样带空格/中文的绝对路径也能正确展开 glob）。
func remoteInfo(h transferHost, shellWord string) []remoteFileInfo {
	if strings.TrimSpace(shellWord) == "" {
		return nil
	}
	out, _ := h.Run(remoteInfoCommand(shellWord))
	var files []remoteFileInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		hash, sizeRaw, mtimeRaw, readableRaw := parts[0], parts[1], parts[2], parts[3]
		p := strings.TrimSpace(strings.Join(parts[4:], "|"))
		if p == "" || p == "NA" {
			continue
		}
		info := remoteFileInfo{Path: p, Readable: readableRaw == "yes"}
		if hash != "NA" {
			info.Hash = hash
		}
		if sizeRaw != "NA" {
			if v, err := strconv.ParseInt(sizeRaw, 10, 64); err == nil {
				info.Size, info.HasSize = v, true
			}
		}
		if mtimeRaw != "NA" {
			if v, err := strconv.ParseInt(mtimeRaw, 10, 64); err == nil {
				info.MTime, info.HasMTime = v, true
			}
		}
		files = append(files, info)
	}
	return files
}

// judgeResult 判定结果
type judgeResult struct {
	OK         bool
	Note       string
	RemotePath string
}

func stampText(t int64, ok bool) string {
	if !ok {
		return "未知"
	}
	return time.Unix(t, 0).UTC().Format("2006-01-02 15:04:05")
}

// judgeUpload 判断这次上传到底成没成 —— **纯函数**，可单测。
func judgeUpload(localHash string, remote *remoteFileInfo, localSize int64, mode string, beforeCount, afterCount int) judgeResult {
	if remote == nil {
		return judgeResult{
			OK:   false,
			Note: "上传后远端**找不到这个文件** —— 这次上传没有成功（可看程序日志里 `[transfer]` 那几行）",
		}
	}
	if mode == "rename" {
		if afterCount <= beforeCount {
			return judgeResult{
				OK: false,
				Note: fmt.Sprintf("改名模式下没看到新文件（远端原有 %d 个同名/同前缀文件，上传后还是 %d 个）—— 这次上传很可能没成功",
					beforeCount, afterCount),
				RemotePath: remote.Path,
			}
		}
	}
	// 首选：内容哈希一致 = 铁证
	if localHash != "" && remote.Hash != "" {
		if remote.Hash == localHash {
			return judgeResult{
				OK: true,
				Note: fmt.Sprintf("远端内容与本机**逐字节一致**（哈希 %s…，%s）；"+
					"远端 mtime %s 是**源文件的时间戳**（ZMODEM 会带过去），不代表文件旧",
					short(remote.Hash), fmtBytes(remote.Size), stampText(remote.MTime, remote.HasMTime)),
				RemotePath: remote.Path,
			}
		}
		return judgeResult{
			OK: false,
			Note: fmt.Sprintf("内容对不上：本机哈希 %s…，远端哈希 %s… —— 传输不完整或对端没换掉旧文件",
				short(localHash), short(remote.Hash)),
			RemotePath: remote.Path,
		}
	}
	// 退而求其次：远端算不出哈希，只能比大小（**弱判据**，而且要如实说清"为什么算不出"）
	if remote.HasSize && remote.Size == localSize {
		why := "远端没能算出哈希（目标机缺少 md5sum/sha256sum）"
		if !remote.Readable {
			why = "当前账号**读不了**这个文件（权限），所以算不出哈希"
		}
		return judgeResult{
			OK:         true,
			Note:       fmt.Sprintf("远端大小与本机一致（%s）—— %s，**只能按大小判断，属于弱证据**", fmtBytes(remote.Size), why),
			RemotePath: remote.Path,
		}
	}
	if !remote.HasSize {
		return judgeResult{
			OK:         false,
			Note:       "远端连大小都读不到（没权限或文件不在）—— 这次上传不能算成功",
			RemotePath: remote.Path,
		}
	}
	return judgeResult{
		OK: false,
		Note: fmt.Sprintf("大小对不上：本机 %s，远端 %s",
			fmtBytes(localSize), fmtBytes(remote.Size)),
		RemotePath: remote.Path,
	}
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// fmtBytes 人类可读大小（与扩展侧同一套格式：小于 10 才带小数）
func fmtBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}

// localMD5 本机文件 md5（读不到返回空串）
func localMD5(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return md5Hex(data)
}

// lastAbsolutePath 从命令输出里取最后一个绝对路径（`pwd` 的结果）
func lastAbsolutePath(out string) string {
	last := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "/") {
			last = l
		}
	}
	return last
}

// shellQuote 单引号引用（远端路径里可能有空格/引号）
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

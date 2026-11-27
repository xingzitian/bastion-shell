package main

// 凭据清单（**只有元数据**）：`<共享目录>/credentials.json`
//
// 与扩展侧 `src/core/credentialIndex.ts` 是同一份文件、同一套字段 —— 两个产品各自
// 把自己存过的凭据记进来，界面就能把"本机到底存了哪些密码、存在哪一边"并成一张表，
// 并且认出**孤儿**（改名/删除档案后留下的条目）。
//
// 这里**绝不写明文/密文**：只有 key / kind / owner / updatedAt。
// 文件坏了只影响"看得见"，不影响保存/读取密码本身 —— 所以读失败一律按空表处理。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const credentialIndexFile = "credentials.json"

type credIndexEntry struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`  // password | passphrase
	Owner     string `json:"owner"` // vscode | desktop
	UpdatedAt string `json:"updatedAt"`
}

type credIndexFile struct {
	Version int              `json:"version"`
	Entries []credIndexEntry `json:"entries"`
}

var credIndexMu sync.Mutex

func credentialIndexPath() string {
	return sharedFilePath(credentialIndexFile)
}

// loadCredIndex 读清单；文件不存在/坏掉/字段不对 → 空表（只影响显示）
func loadCredIndexLocked() []credIndexEntry {
	raw, err := os.ReadFile(credentialIndexPath())
	if err != nil {
		return nil
	}
	var f credIndexFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	out := make([]credIndexEntry, 0, len(f.Entries))
	for _, e := range f.Entries {
		k := strings.TrimSpace(e.Key)
		if k == "" {
			continue
		}
		if e.Kind != "password" && e.Kind != "passphrase" {
			continue
		}
		if e.Owner != "vscode" && e.Owner != "desktop" {
			continue
		}
		e.Key = k
		out = append(out, e)
	}
	return out
}

// 同 (key, kind, owner) 只留最新一条
func dedupeCredIndex(entries []credIndexEntry) []credIndexEntry {
	best := map[string]credIndexEntry{}
	order := make([]string, 0, len(entries))
	for _, e := range entries {
		id := e.Owner + "\x00" + e.Kind + "\x00" + e.Key
		prev, ok := best[id]
		if !ok {
			best[id] = e
			order = append(order, id)
			continue
		}
		if e.UpdatedAt > prev.UpdatedAt {
			best[id] = e
		}
	}
	out := make([]credIndexEntry, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
	}
	return out
}

// saveCredIndexLocked 先写临时文件再改名：中途崩了也不会留半个文件
func saveCredIndexLocked(entries []credIndexEntry) {
	f := credIndexFile{Version: 1, Entries: dedupeCredIndex(entries)}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	path := credentialIndexPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// recordCredIndex 记一条（同键同种类同属主 → 只刷新时间）
func recordCredIndex(key, kind, owner string) {
	k := strings.TrimSpace(key)
	if k == "" {
		return
	}
	credIndexMu.Lock()
	defer credIndexMu.Unlock()
	all := loadCredIndexLocked()
	next := make([]credIndexEntry, 0, len(all)+1)
	for _, e := range all {
		if e.Key == k && e.Kind == kind && e.Owner == owner {
			continue
		}
		next = append(next, e)
	}
	next = append(next, credIndexEntry{Key: k, Kind: kind, Owner: owner, UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
	saveCredIndexLocked(next)
}

// dropCredIndex 去掉一条（删凭据时用）
func dropCredIndex(key, kind, owner string) {
	k := strings.TrimSpace(key)
	if k == "" {
		return
	}
	credIndexMu.Lock()
	defer credIndexMu.Unlock()
	all := loadCredIndexLocked()
	next := make([]credIndexEntry, 0, len(all))
	changed := false
	for _, e := range all {
		if e.Key == k && e.Kind == kind && e.Owner == owner {
			changed = true
			continue
		}
		next = append(next, e)
	}
	if changed {
		saveCredIndexLocked(next)
	}
}

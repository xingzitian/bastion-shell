package main

// 「凭据」数据：给桌面的凭据弹窗用（与扩展面板读同一份清单 + 同一个档案表）。
//
// 桌面能看见两类东西：
//   1. 档案 × [密码 / 私钥口令]：有没有存、谁存的（桌面/扩展）、什么时候存的；
//   2. 孤儿：清单里有、档案表里已经没有的键（改名/删除留下的）。
// 桌面只能删**自己**那份（DPAPI）；扩展那份只能提示"到扩展里删"，
// 因为 SecretStorage 在 VS Code 进程里，桌面碰不到。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

type credSlotView struct {
	Has       bool     `json:"has"`
	Owners    []string `json:"owners"`
	UpdatedAt string   `json:"updatedAt"`
}

type credRowView struct {
	Name       string       `json:"name"`
	Target     string       `json:"target"`
	AuthMethod string       `json:"authMethod"`
	Password   credSlotView `json:"password"`
	Passphrase credSlotView `json:"passphrase"`
}

type credOrphanView struct {
	Key       string   `json:"key"`
	Owners    []string `json:"owners"`
	Kinds     []string `json:"kinds"`
	UpdatedAt string   `json:"updatedAt"`
}

type credentialsView struct {
	Rows      []credRowView    `json:"rows"`
	Orphans   []credOrphanView `json:"orphans"`
	IndexFile string           `json:"indexFile"`
	Note      string           `json:"note"`
}

func slotView(entries []credIndexEntry, key, kind string) credSlotView {
	owners := map[string]bool{}
	updated := ""
	for _, e := range entries {
		if e.Key != key || e.Kind != kind {
			continue
		}
		owners[e.Owner] = true
		if e.UpdatedAt > updated {
			updated = e.UpdatedAt
		}
	}
	out := credSlotView{Has: len(owners) > 0, Owners: []string{}, UpdatedAt: updated}
	for o := range owners {
		out.Owners = append(out.Owners, o)
	}
	sort.Strings(out.Owners)
	return out
}

// buildCredentialsView 汇总"档案 × 凭据 × 孤儿"。纯函数（便于测试）。
func buildCredentialsView(profiles []Profile, entries []credIndexEntry, dpapi map[string]string) credentialsView {
	rows := make([]credRowView, 0, len(profiles))
	names := map[string]bool{}
	for _, p := range profiles {
		names[p.Name] = true
		pw := slotView(entries, p.Name, "password")
		// DPAPI 里真的有条目 → 也算"桌面存过"（清单可能还没写过：老数据）
		if _, ok := dpapi[p.Name]; ok {
			pw.Has = true
			hasDesktop := false
			for _, o := range pw.Owners {
				if o == "desktop" {
					hasDesktop = true
				}
			}
			if !hasDesktop {
				pw.Owners = append(pw.Owners, "desktop")
				sort.Strings(pw.Owners)
			}
		}
		auth := p.AuthMethod
		if auth != "key" {
			auth = "password"
		}
		rows = append(rows, credRowView{
			Name:       p.Name,
			Target:     fmt.Sprintf("%s@%s:%d", p.Username, p.Host, itoaDefault(p.Port, 22)),
			AuthMethod: auth,
			Password:   pw,
			Passphrase: slotView(entries, p.Name, "passphrase"),
		})
	}

	orphanKeys := map[string]bool{}
	for _, e := range entries {
		if !names[e.Key] {
			orphanKeys[e.Key] = true
		}
	}
	keys := make([]string, 0, len(orphanKeys))
	for k := range orphanKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	orphans := make([]credOrphanView, 0, len(keys))
	for _, k := range keys {
		var mine []credIndexEntry
		for _, e := range entries {
			if e.Key == k {
				mine = append(mine, e)
			}
		}
		owners := map[string]bool{}
		kinds := map[string]bool{}
		updated := ""
		for _, e := range mine {
			owners[e.Owner] = true
			if e.Kind == "password" {
				kinds["密码"] = true
			} else {
				kinds["私钥口令"] = true
			}
			if e.UpdatedAt > updated {
				updated = e.UpdatedAt
			}
		}
		ov := credOrphanView{Key: k, Owners: []string{}, Kinds: []string{}, UpdatedAt: updated}
		for o := range owners {
			ov.Owners = append(ov.Owners, o)
		}
		sort.Strings(ov.Owners)
		for kind := range kinds {
			ov.Kinds = append(ov.Kinds, kind)
		}
		sort.Strings(ov.Kinds)
		orphans = append(orphans, ov)
	}

	return credentialsView{
		Rows:      rows,
		Orphans:   orphans,
		IndexFile: credentialIndexPath(),
		Note:      "密码加密保存在本机（桌面用 Windows DPAPI，扩展用 VS Code 钥匙串）。这里只显示有没有存、存在哪边；桌面只能删自己那份。",
	}
}

func itoaDefault(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// registerCredentialRoutes 挂 /api/credentials（GET）。
func registerCredentialRoutes(mux *http.ServeMux, profiles *profileStore) {
	mux.HandleFunc("/api/credentials", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}
		entries := loadCredIndexLocked()
		st := newSecretStore()
		st.load()
		dpapi := map[string]string{}
		for _, k := range st.keys() {
			dpapi[k] = "" // 只是"有没有"，不把明文/密文带出来
		}
		_ = json.NewEncoder(w).Encode(buildCredentialsView(profiles.snapshot(), entries, dpapi))
	})
}

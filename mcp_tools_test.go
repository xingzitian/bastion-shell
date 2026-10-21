package main

// 工具 schema 与实现的参数一致性。
//
// 为什么需要这一条：0.3.10 压测里 `bastion_exec` 的 waitMs/force/mode 只加进了分发、**没加进 schema**，
// 而 schema 是 additionalProperties:false —— 严格客户端根本传不进来。这类"代码加了、schema 忘了"
// 的漂移必须靠测试挡住。
//
// 桌面版这里用手写清单（Go 没有 Proxy 那种运行时拦截）；清单与 schema 不一致就红。

import (
	"strings"
	"testing"
)

func TestToolSchemasCoverDocumentedParams(t *testing.T) {
	want := map[string][]string{
		"bastion_exec":   {"command", "session", "terminal", "waitMs", "quietMs", "mode", "background", "force"},
		"bastion_wait":   {"jobId", "pattern", "waitMs", "sinceOffset"},
		"bastion_job":    {"jobId", "sinceOffset", "maxBytes"},
		"bastion_cancel": {"jobId"},
		"bastion_use":    {"session", "as", "unbind"},
		"bastion_tail":   {"terminal", "lines"},
	}
	byName := map[string]map[string]any{}
	for _, d := range mcpToolDefs {
		props, _ := d.InputSchema["properties"].(map[string]any)
		byName[d.Name] = props
	}
	for tool, params := range want {
		props, ok := byName[tool]
		if !ok {
			t.Fatalf("工具 %s 不存在", tool)
		}
		for _, p := range params {
			if _, ok := props[p]; !ok {
				var have []string
				for k := range props {
					have = append(have, k)
				}
				t.Errorf("%s 的 schema 缺少参数 %q（现有：%s）—— 分发读了它，客户端却传不进来",
					tool, p, strings.Join(have, ", "))
			}
		}
	}
}

func TestJobToolsAreReadOnlyExceptCancel(t *testing.T) {
	for _, d := range mcpToolDefs {
		switch d.Name {
		case "bastion_wait", "bastion_job":
			if !d.ReadOnly {
				t.Errorf("%s 应该标只读（客户端据此不弹确认框）", d.Name)
			}
		case "bastion_cancel", "bastion_exec":
			if d.ReadOnly {
				t.Errorf("%s 有副作用，不能标只读", d.Name)
			}
		}
	}
}

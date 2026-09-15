package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// JSONC（带注释的 JSON）支持。
//
// 为什么桌面版要自己写一层：**共享规则文件是两个实现读同一份的** ——
// `habits.jsonc` / `dangerRules.jsonc` / `menuHints.jsonc` 里手写的中文注释
// 必须能读懂；写回时也不能把注释冲掉（扩展侧用 jsonc-parser 干这件事）。
// Go 标准库只认严格 JSON，所以这里自己扫一遍。
// 不引第三方依赖：AGENT_RULES §4 要求引入依赖先说用途，而这个需求
// 两个纯函数就能覆盖（去注释 + 定位节点），不值得多一个供应链。

// stripJSONC 把 JSONC 文本变成标准 JSON：
// 去掉 `//` 与 `/* */` 注释（字符串里的不算）、去掉尾随逗号。
func stripJSONC(s string) string {
	out := make([]byte, 0, len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			// 字符串原样拷贝（里面的 // 和 /* 不是注释）
			out = append(out, c)
			i++
			for i < len(s) {
				ch := s[i]
				if ch == '\\' && i+1 < len(s) {
					out = append(out, ch, s[i+1])
					i += 2
					continue
				}
				out = append(out, ch)
				i++
				if ch == '"' {
					break
				}
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			} else {
				i = len(s)
			}
		default:
			out = append(out, c)
			i++
		}
	}
	return dropTrailingCommas(out)
}

// dropTrailingCommas 去掉 `,]` / `,}` 里那个多余的逗号（字符串外的）
func dropTrailingCommas(b []byte) string {
	out := make([]byte, 0, len(b))
	i := 0
	for i < len(b) {
		if b[i] == '"' {
			out = append(out, b[i])
			i++
			for i < len(b) {
				ch := b[i]
				if ch == '\\' && i+1 < len(b) {
					out = append(out, ch, b[i+1])
					i += 2
					continue
				}
				out = append(out, ch)
				i++
				if ch == '"' {
					break
				}
			}
			continue
		}
		if b[i] == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				i++ // 丢掉这个逗号
				continue
			}
		}
		out = append(out, b[i])
		i++
	}
	return string(out)
}

// parseJSONC 解析 JSONC 文本
func parseJSONC(text string, out any) error {
	return json.Unmarshal([]byte(stripJSONC(text)), out)
}

// ───────────────────────── 节点定位（写回时只动目标节点） ─────────────────────────
//
// 目标是「保留用户手写的注释」：整份重排会把注释冲掉，所以写回时先靠这层
// 找到目标值在原文里的字节范围，只替换那一小段。

type jsoncNode struct{ start, end int }

type jsoncScanner struct {
	s string
	i int
}

// skipSpace 跳空白和注释
func (p *jsoncScanner) skipSpace() {
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			p.i++
			continue
		}
		if c == '/' && p.i+1 < len(p.s) && p.s[p.i+1] == '/' {
			for p.i < len(p.s) && p.s[p.i] != '\n' {
				p.i++
			}
			continue
		}
		if c == '/' && p.i+1 < len(p.s) && p.s[p.i+1] == '*' {
			p.i += 2
			for p.i+1 < len(p.s) && !(p.s[p.i] == '*' && p.s[p.i+1] == '/') {
				p.i++
			}
			if p.i+1 < len(p.s) {
				p.i += 2
			} else {
				p.i = len(p.s)
			}
			continue
		}
		return
	}
}

// skipString 跳过一段字符串字面量
func (p *jsoncScanner) skipString() (int, int, bool) {
	start := p.i
	if p.i >= len(p.s) || p.s[p.i] != '"' {
		return 0, 0, false
	}
	p.i++
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '\\' {
			p.i += 2
			continue
		}
		p.i++
		if c == '"' {
			return start, p.i, true
		}
	}
	return 0, 0, false
}

// skipValue 跳过当前这个值，返回它的字节范围
func (p *jsoncScanner) skipValue() (int, int, bool) {
	p.skipSpace()
	start := p.i
	if p.i >= len(p.s) {
		return 0, 0, false
	}
	switch c := p.s[p.i]; {
	case c == '{':
		p.i++
		for {
			p.skipSpace()
			if p.i >= len(p.s) {
				return 0, 0, false
			}
			if p.s[p.i] == '}' {
				p.i++
				return start, p.i, true
			}
			if _, _, ok := p.skipString(); !ok {
				return 0, 0, false
			}
			p.skipSpace()
			if p.i >= len(p.s) || p.s[p.i] != ':' {
				return 0, 0, false
			}
			p.i++
			if _, _, ok := p.skipValue(); !ok {
				return 0, 0, false
			}
			p.skipSpace()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
			}
		}
	case c == '[':
		p.i++
		for {
			p.skipSpace()
			if p.i >= len(p.s) {
				return 0, 0, false
			}
			if p.s[p.i] == ']' {
				p.i++
				return start, p.i, true
			}
			if _, _, ok := p.skipValue(); !ok {
				return 0, 0, false
			}
			p.skipSpace()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
			}
		}
	case c == '"':
		if _, _, ok := p.skipString(); !ok {
			return 0, 0, false
		}
		return start, p.i, true
	default:
		for p.i < len(p.s) {
			ch := p.s[p.i]
			if ch == ',' || ch == '}' || ch == ']' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '/' {
				break
			}
			p.i++
		}
		if p.i == start {
			return 0, 0, false
		}
		return start, p.i, true
	}
}

// findJSONCValue 按路径找值的字节范围（空路径 = 根值）
func findJSONCValue(text string, path ...string) (jsoncNode, bool) {
	p := &jsoncScanner{s: text}
	p.skipSpace()
	start, end, ok := p.skipValue()
	if !ok {
		return jsoncNode{}, false
	}
	node := jsoncNode{start, end}
	for _, key := range path {
		child, ok := jsoncObjectMember(text, node, key)
		if !ok {
			return jsoncNode{}, false
		}
		node = child
	}
	return node, true
}

// jsoncObjectMember 在一个对象值里找某个键对应的值
func jsoncObjectMember(text string, obj jsoncNode, key string) (jsoncNode, bool) {
	p := &jsoncScanner{s: text, i: obj.start}
	p.skipSpace()
	if p.i >= len(text) || text[p.i] != '{' {
		return jsoncNode{}, false
	}
	p.i++
	for {
		p.skipSpace()
		if p.i >= len(text) {
			return jsoncNode{}, false
		}
		if text[p.i] == '}' {
			return jsoncNode{}, false
		}
		ks, ke, ok := p.skipString()
		if !ok {
			return jsoncNode{}, false
		}
		var name string
		_ = json.Unmarshal([]byte(text[ks:ke]), &name)
		p.skipSpace()
		if p.i >= len(text) || text[p.i] != ':' {
			return jsoncNode{}, false
		}
		p.i++
		vs, ve, ok := p.skipValue()
		if !ok {
			return jsoncNode{}, false
		}
		if name == key {
			return jsoncNode{vs, ve}, true
		}
		p.skipSpace()
		if p.i < len(text) && text[p.i] == ',' {
			p.i++
		}
	}
}

// ───────────────────────── 写回：只替换/插入目标节点 ─────────────────────────

// jsoncSetValue 把 path 指向的值设成字面量 literal；路径中间缺的对象会自动补出来。
// 返回新文本；路径无法落地（比如父节点是字符串）时返回 ok=false，调用方应放弃写入。
func jsoncSetValue(text string, literal string, path ...string) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	// 1) 路径已存在 → 只替换那个值的范围
	if n, ok := findJSONCValue(text, path...); ok {
		return text[:n.start] + literal + text[n.end:], true
	}
	// 2) 找最深的存在祖先，把缺的那截作为新成员插进去
	for i := len(path) - 1; i >= 1; i-- {
		parent, ok := findJSONCValue(text, path[:i]...)
		if !ok {
			continue
		}
		value, ok := nestedJSONCLiteral(path[i+1:], literal)
		if !ok {
			return "", false
		}
		return insertJSONCObjectMember(text, parent, path[i], value)
	}
	// 3) 根对象里插
	root, ok := findJSONCValue(text)
	if !ok {
		return "", false
	}
	value, ok := nestedJSONCLiteral(path[1:], literal)
	if !ok {
		return "", false
	}
	return insertJSONCObjectMember(text, root, path[0], value)
}

// nestedJSONCArrayItem 往 path 指向的数组末尾追加一项（数组必须已经存在）
func nestedJSONCArrayItem(text string, literal string, path ...string) (string, bool) {
	arr, ok := findJSONCValue(text, path...)
	if !ok {
		return "", false
	}
	if arr.start >= len(text) || text[arr.start] != '[' {
		return "", false
	}
	inner := arr.start + 1
	closeAt := arr.end - 1
	if jsoncSliceEmpty(text[inner:closeAt]) {
		return text[:inner] + literal + text[closeAt:], true
	}
	return text[:closeAt] + ", " + literal + text[closeAt:], true
}

// nestedJSONCLiteral 把「路径剩余部分 + 最终值」拼成字面量：
// nestedJSONCLiteral(["a","b"], `"x"`) → `{"a": {"b": "x"}}`
func nestedJSONCLiteral(rest []string, literal string) (string, bool) {
	if literal == "" {
		return "", false
	}
	value := literal
	for i := len(rest) - 1; i >= 0; i-- {
		key, err := json.Marshal(rest[i])
		if err != nil {
			return "", false
		}
		value = "{" + string(key) + ": " + value + "}"
	}
	return value, true
}

// insertJSONCObjectMember 往一个对象值里加一个成员（放在最后）
func insertJSONCObjectMember(text string, obj jsoncNode, key string, valueLiteral string) (string, bool) {
	if obj.start >= len(text) || text[obj.start] != '{' {
		return "", false
	}
	keyLit, err := json.Marshal(key)
	if err != nil {
		return "", false
	}
	inner := obj.start + 1
	closeAt := obj.end - 1
	sep := ", "
	if jsoncSliceEmpty(text[inner:closeAt]) {
		sep = ""
	}
	return text[:closeAt] + sep + string(keyLit) + ": " + valueLiteral + text[closeAt:], true
}

// jsoncSliceEmpty 一段对象/数组内部是不是空的（忽略空白和注释）
func jsoncSliceEmpty(s string) bool {
	p := &jsoncScanner{s: s}
	p.skipSpace()
	return p.i >= len(s)
}

// ───────────────────────── 文件级读写 ─────────────────────────

// readJSONCText 读文本（文件不存在返回 found=false）
func readJSONCText(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// writeJSONCTextAtomic 原子写回（先写临时文件再改名）。
//
// 为什么必须：习惯文件旧版在 Windows 上直接覆盖，目标文件被瞬时占用时
// 会静默失败，表现成「记了习惯却没生效」（扩展侧 2026-09-10 修过一次）。
// 这里带重试，失败会返回错误让调用方说出来，而不是假装成功。
func writeJSONCTextAtomic(path string, text string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-jsonc-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	var lastErr error
	for i := 0; i < 5; i++ {
		lastErr = os.Rename(tmpName, path)
		if lastErr == nil {
			return nil
		}
		time.Sleep(60 * time.Millisecond)
	}
	return fmt.Errorf("改名失败（目标文件可能被占用）：%w", lastErr)
}

// validJSONC 改完之后校验一遍：解析不过就放弃本次写入（原文件不动）
func validJSONC(text string) bool {
	var probe any
	return parseJSONC(text, &probe) == nil
}

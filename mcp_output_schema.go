package main

// exec 族工具的 outputSchema（与扩展侧 src/execSchema.ts 对齐）。
//
// 声明了 outputSchema 之后，规范要求 structuredContent 必须符合它 ——
// 所以这里只列"一定会出现"的字段，其余用 additionalProperties 放开。

var statusEnum = []string{"working", "input_required", "completed", "failed", "cancelled"}

func jobStateProps() map[string]any {
	return map[string]any{
		"status":      map[string]any{"type": "string", "enum": statusEnum},
		"jobId":       map[string]any{"type": "string"},
		"exitCode":    map[string]any{"type": "number"},
		"elapsedMs":   map[string]any{"type": "number"},
		"cursor":      map[string]any{"type": "number"},
		"cursorMoved": map[string]any{"type": "boolean"},
		"lastLine":    map[string]any{"type": "string"},
		"markerSeen":  map[string]any{"type": "boolean"},
		"prompt":      map[string]any{"type": "string"},
		"hint":        map[string]any{"type": "string"},
		"reason":      map[string]any{"type": "string"},
		"logPath":     map[string]any{"type": "string"},
		"matched":     map[string]any{"type": "boolean"},
		"matchedLine": map[string]any{"type": "string"},
	}
}

func execOutputSchema() map[string]any {
	props := jobStateProps()
	props["session"] = map[string]any{"type": "string"}
	props["timedOut"] = map[string]any{"type": "boolean"}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             []string{"status"},
		"additionalProperties": true,
	}
}

func waitOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           jobStateProps(),
		"required":             []string{"status"},
		"additionalProperties": true,
	}
}

func jobOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"jobId":      map[string]any{"type": "string"},
			"status":     map[string]any{"type": "string", "enum": statusEnum},
			"exitCode":   map[string]any{"type": "number"},
			"markerSeen": map[string]any{"type": "boolean"},
			"reason":     map[string]any{"type": "string"},
			"sinceOffset": map[string]any{"type": "number"},
			"nextOffset":  map[string]any{"type": "number"},
			"truncated":   map[string]any{"type": "boolean"},
			"totalBytes":  map[string]any{"type": "number"},
			"logPath":     map[string]any{"type": "string"},
			"chunk":       map[string]any{"type": "string"},
		},
		"required":             []string{"jobId", "status"},
		"additionalProperties": true,
	}
}

func cancelOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"jobId":  map[string]any{"type": "string"},
			"status": map[string]any{"type": "string", "enum": statusEnum},
			"reason": map[string]any{"type": "string"},
		},
		"required":             []string{"status"},
		"additionalProperties": true,
	}
}

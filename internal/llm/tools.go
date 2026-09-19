package llm

type ToolDefinition struct {
	Type     string         `json:"type"`
	Function FunctionSchema `json:"function"`
}

type FunctionSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func Definitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "media_search",
				Description: "搜索电影、电视剧或动画资源。只搜索，不会下载。需要把候选结果展示给用户并等待明确选择。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":      map[string]any{"type": "string", "description": "片名，可包含中文或原名"},
						"media_type": map[string]any{"type": "string", "enum": []string{"movie", "tv", "anime"}},
						"year":       map[string]any{"type": "integer"},
						"quality":    map[string]any{"type": "string", "description": "例如 1080p、2160p"},
						"subtitles":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"sources":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"pansou", "prowlarr"}}},
						"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
					},
					"required": []string{"query"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "media_acquire",
				Description: "获取用户已经明确选择的资源。只有用户明确确认后才能调用，candidate_id 必须来自最近一次搜索。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"candidate_id": map[string]any{"type": "string"},
						"confirmed":    map[string]any{"type": "boolean", "description": "用户是否明确确认该候选资源"},
					},
					"required": []string{"candidate_id", "confirmed"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "media_job_status",
				Description: "查询资源获取任务的实时状态。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"job_id": map[string]any{"type": "string"}},
					"required":   []string{"job_id"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "media_job_cancel",
				Description: "取消一个尚未完成的获取任务。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"job_id": map[string]any{"type": "string"}},
					"required":   []string{"job_id"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "media_capabilities",
				Description: "查看当前已配置的搜索平台、下载器和确认要求。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
	}
}

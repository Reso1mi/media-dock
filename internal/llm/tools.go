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
				Description: "获取用户已经明确选择的资源。只有用户明确确认后才能调用，candidate_id 必须来自最近一次搜索。BT 候选交给已配置的下载器；启用 OpenList 后，支持的网盘分享候选会转存到请求指定的 target_dir。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"candidate_id":    map[string]any{"type": "string"},
						"goal":            map[string]any{"type": "string", "enum": []string{"save_to_cloud", "download_to_local"}, "description": "save_to_cloud 用于 OpenList 网盘转存；download_to_local 用于已配置的下载执行端"},
						"target_profile":  map[string]any{"type": "string", "description": "media_capabilities 返回的 OpenList/下载器 profile"},
						"target_dir":      map[string]any{"type": "string", "description": "save_to_cloud 时传给 OpenList 的目标目录"},
						"confirmed":       map[string]any{"type": "boolean", "description": "用户是否明确确认该候选资源和目标"},
						"idempotency_key": map[string]any{"type": "string", "description": "重试同一获取请求时复用的稳定键"},
					},
					"required": []string{"candidate_id", "confirmed"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "media_copy",
				Description: "通过 OpenList COPY 一个用户明确指定的文件到目标目录。不要把它当作 OpenList 原始 API 代理；必须确认源文件和目标目录。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"target_profile":  map[string]any{"type": "string", "description": "media_capabilities 返回的 OpenList profile"},
						"source_path":     map[string]any{"type": "string", "description": "OpenList 中已确认的源文件绝对路径"},
						"target_dir":      map[string]any{"type": "string", "description": "OpenList 中已确认的目标目录绝对路径"},
						"overwrite":       map[string]any{"type": "boolean", "description": "是否覆盖目标文件，默认 false"},
						"skip_existing":   map[string]any{"type": "boolean", "description": "目标存在时跳过，默认 true"},
						"merge":           map[string]any{"type": "boolean", "description": "是否使用 OpenList merge 语义"},
						"confirmed":       map[string]any{"type": "boolean", "description": "用户是否明确确认源文件和目标目录"},
						"idempotency_key": map[string]any{"type": "string", "description": "重试同一 COPY 请求时复用的稳定键"},
					},
					"required": []string{"source_path", "target_dir", "confirmed"},
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
				Name:        "media_job_reconcile",
				Description: "核对结果不确定的获取任务。只查询服务端配置的外部目标，不会重新提交转存，可能仍需要人工核对。",
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
				Name:        "media_jobs_list",
				Description: "分页查找近期、进行中、失败或待核对的资源获取任务，适合聊天中断后重新发现任务。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
						"offset":   map[string]any{"type": "integer", "minimum": 0},
						"statuses": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
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

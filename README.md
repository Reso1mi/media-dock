# MediaDock

面向 AI 的轻量媒体组件编排服务。它通过 MCP 暴露稳定能力，连接已有搜索源、索引器和下载器，不重新实现这些组件。

这个项目不是重新实现 NAStool，也不是重新实现下载器。它把媒体获取过程收敛成一组适合 LLM 调用的窄接口：

```text
用户表达观影需求
  → 搜索 PanSou / Prowlarr
  → 聚合、去重、评分
  → 返回候选资源
  → 用户明确确认
  → 调用现有下载器
  → 后续接入文件监控、整理和 Jellyfin
```

## 当前已实现

- Go 1.23 模块化单体服务；
- PanSou 搜索适配器（`POST /api/search`）；
- Prowlarr 兼容索引器适配器（`GET /api/v1/search`）；
- 候选资源统一模型、去重、质量/字幕/做种评分；
- 候选资源句柄隔离：API 不返回原始分享链接、密码和 provider payload；
- 用户确认门：没有 `confirmed: true` 不会创建获取任务；
- 可配置的下载器适配器；当前包含 Transmission RPC，仅声明支持磁力和明确的 torrent 材料；
- 标准 MCP Server：官方 Go SDK + Streamable HTTP，端点为 `/mcp`；
- MCP 工具：`media_search`、`media_acquire`、`media_job_status`、`media_job_cancel`、`media_jobs_list`、`media_capabilities`；
- 保留 OpenAI 风格工具定义接口，兼容暂未支持 MCP 的旧客户端；
- 单元测试、官方 SDK 客户端协议测试和 Docker 镜像构建文件。

当前搜索和任务状态使用内存存储，服务重启后会丢失搜索候选和任务记录。这是第一条垂直链路，下一步应替换为 SQLite。获取链路支持 API-only 模式：未配置 `DOWNLOAD_INCOMING_DIR` 时，MediaDock 不要求挂载本地媒体盘，由远端下载器决定目标目录。

## 快速启动

```powershell
Copy-Item .env.example .env
$env:PANSOU_BASE_URL = "http://127.0.0.1:80"
$env:AUTH_DISABLED = "true" # 仅本地开发；生产环境使用 AUTH_TOKEN_FILE
$env:DOWNLOADERS = "transmission"
$env:TRANSMISSION_RPC_URL = "http://127.0.0.1:9091/transmission/rpc"
go run ./cmd/media-dock
```

检查服务：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
Invoke-RestMethod http://127.0.0.1:8080/api/v1/providers
Invoke-RestMethod http://127.0.0.1:8080/api/v1/downloaders
Invoke-RestMethod http://127.0.0.1:8080/api/v1/llm/tools
```

标准 MCP 客户端连接：

```text
http://127.0.0.1:8080/mcp
```

REST 业务接口和 MCP 端点都需要发送 `Authorization: Bearer <token>`；`/healthz` 仅作为公开存活探针。服务默认生成并持久化 token（推荐设置 `AUTH_TOKEN_FILE`）；只有显式设置 `AUTH_DISABLED=true` 才进入无认证开发模式。`MCP_AUTH_TOKEN` 仍作为兼容的固定 token 配置。MCP 客户端负责执行标准的 `initialize`、`tools/list` 和 `tools/call`，不需要再调用下面的 REST 工具定义接口。

搜索：

```powershell
$body = @{
  query = "信号"
  media_type = "tv"
  quality = "1080p"
  subtitles = @("zh-CN")
  limit = 10
} | ConvertTo-Json

Invoke-RestMethod -Method Post `
  -Uri http://127.0.0.1:8080/api/v1/search `
  -ContentType "application/json" `
  -Body $body
```

确认并获取候选资源：

```powershell
$body = @{ candidate_id = "candidate_xxx"; confirmed = $true } | ConvertTo-Json
Invoke-RestMethod -Method Post `
  -Uri http://127.0.0.1:8080/api/v1/acquisitions `
  -ContentType "application/json" `
  -Body $body
```

查询任务：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/api/v1/jobs/job_xxx
```

## 配置

复制 `.env.example` 为 `.env` 后配置：

| 变量                                          | 作用                                                                               |
| --------------------------------------------- | ---------------------------------------------------------------------------------- |
| `STORAGE_SQLITE_PATH` / `SQLITE_PATH`         | SQLite 数据库路径，默认 `./data/mediadock.db`；容器建议使用 `/config/mediadock.db` |
| `MCP_ENABLED`                                 | 是否启用标准 MCP Streamable HTTP 端点，默认 `true`                                 |
| `MCP_PATH`                                    | MCP 端点路径，默认 `/mcp`                                                          |
| `AUTH_TOKEN` / `MCP_AUTH_TOKEN`               | REST 与 MCP 共用的固定 Bearer Token；后者为兼容旧配置                              |
| `AUTH_TOKEN_FILE`                             | token 文件路径；默认 `./data/auth-token`，不存在时自动生成并以 `0600` 保存         |
| `AUTH_DISABLED`                               | 显式允许无认证开发模式，默认 `false`                                               |
| `PANSOU_BASE_URL`                             | PanSou / pansou-web 地址                                                           |
| `PROWLARR_BASE_URL`                           | 可选，Prowlarr 地址                                                                |
| `PROWLARR_API_KEY`                            | Prowlarr API Key                                                                   |
| `DOWNLOADERS`                                 | 可选，逗号分隔的下载器名称；留空表示搜索模式                                       |
| `TRANSMISSION_RPC_URL`                        | Transmission RPC 地址                                                              |
| `TRANSMISSION_USER` / `TRANSMISSION_PASSWORD` | Transmission 认证                                                                  |
| `DOWNLOAD_INCOMING_DIR`                       | 可选的本地下载临时目录；留空时使用 API-only 模式，不要求 MediaDock 挂载媒体盘      |

只有配置了 `DOWNLOAD_INCOMING_DIR` 时，服务和 Transmission 才必须使用双方共享卷中的同一个容器路径；不能直接使用宿主机路径替代容器内路径。默认 API-only 模式不需要该目录。

`DOWNLOADERS` 留空时服务仍可正常搜索，但确认获取会返回 `downloader_unavailable`。这样可以先部署搜索平台，再按需启用下载器。后续新增 OpenList、aria2 等适配器时，只需增加适配器并在此配置中启用。

## LLM 接口边界

首选接口是标准 MCP Streamable HTTP：

```text
POST /mcp
GET /mcp
DELETE /mcp
```

服务端使用官方 MCP Go SDK 管理会话和 JSON-RPC，不自定义 `tools/list` 或 `tools/call` 协议。MCP 暴露六个工具：

- `media_search`：搜索资源，不下载；
- `media_acquire`：用户确认后创建获取任务；
- `media_job_status`：查询任务状态；
- `media_job_cancel`：取消任务；
- `media_jobs_list`：分页重新发现近期、进行中或失败任务；
- `media_capabilities`：查看当前实际可用的搜索平台和下载器能力。

`GET /api/v1/capabilities` 提供与 `media_capabilities` 对应的 REST 能力说明；`GET /api/v1/llm/tools` 是兼容旧客户端的 OpenAI 风格函数定义发现接口，不是 MCP 协议实现。

LLM 不直接执行 shell、访问原始分享链接或操作文件系统。它只使用候选 ID 和任务 ID，真实材料只在服务内部交给对应适配器。候选类型会区分 `magnet`、`torrent`、`http_file`、`cloud_share` 和 `unknown`；未知的普通 HTTP 地址不会被 Transmission 宣称为可获取。

`media_acquire` 的副作用边界由两层共同保证：MCP 工具说明要求先展示候选并取得用户明确选择，应用服务还会强制校验 `confirmed: true`。即使调用方绕过 MCP 直接访问 REST 接口，也不能跳过确认门。

## 下一阶段

按用户需求，后续优先级是：

1. 引入 SQLite 存储搜索会话、候选、任务和事件；
2. 增加进程内后台追踪、幂等和任务归属；
3. 完善能力发现、统一错误模型和任务列表；
4. 支持结构化连接配置与诊断命令；
5. 再按需求增加 qBittorrent、通用搜索或其他媒体服务适配器。

完整架构说明见 [`docs/architecture.md`](docs/architecture.md)。

# MediaDock

面向 AI 的轻量媒体组件编排服务。它通过 MCP 暴露稳定能力，连接已有搜索源、索引器和下载器，不重新实现这些组件。

这个项目不是重新实现 NAStool，也不是重新实现下载器。它把媒体获取过程收敛成一组适合 LLM 调用的窄接口：

```text
用户表达观影需求
  → 搜索 PanSou / Prowlarr
  → 聚合、去重、评分
  → 返回候选资源
  → 用户明确确认
  → MediaDock 根据 goal 和服务端 target_profile 选择适配器
  → BT 候选交给 Transmission / qBittorrent，网盘分享交给 OpenList HTTP API 转存
  → 已知 OpenList 文件通过独立 media_copy 任务调用 OpenList COPY
  → 转存、COPY 和本地下载分别记录阶段；不把网盘内操作误报为 MediaDock 本地文件
```

## 当前已实现

- Go 1.23 模块化单体服务；
- PanSou 搜索适配器（`POST /api/search`）；
- Prowlarr 兼容索引器适配器（`GET /api/v1/search`）；
- 候选资源统一模型、去重、质量/字幕/做种评分；
- 候选资源句柄隔离：API 不返回原始分享链接、密码和 provider payload；
- 用户确认门：没有 `confirmed: true` 不会创建获取任务；
- 可配置的获取适配器：Transmission RPC、qBittorrent Web API，以及通过 HTTP API 适配 Reso1mi/OpenList fork 的网盘分享转存；
- 显式获取目标：`save_to_cloud` 或 `download_to_local`；服务端 profile 固定适配器和凭据范围，OpenList 业务请求显式传入目标目录；
- 独立的 OpenList 文件 COPY：`media_copy` / `POST /api/v1/copies`，保存 source path、目标目录、选项和远端 operation 状态；
- SQLite 持久化获取阶段、幂等请求摘要和 OpenList 目标/文件引用；不猜测转存产生的具体文件，也不把签名下载 URL 写入任务输出；
- 标准 MCP Server：官方 Go SDK + Streamable HTTP，端点为 `/mcp`；
- MCP 工具：`media_search`、`media_acquire`、`media_copy`、`media_job_status`、`media_job_reconcile`、`media_job_cancel`、`media_jobs_list`、`media_capabilities`；
- 内嵌轻量 WebUI：概览能力、搜索候选、确认获取、查看和取消任务，入口为 `/`；
- 保留 OpenAI 风格工具定义接口，兼容暂未支持 MCP 的旧客户端；
- SQLite 持久化候选和任务，进程内 Worker 支持后台提交、状态追踪和重启后的保守恢复；
- 单元测试、官方 SDK 客户端协议测试和 Docker 镜像构建文件。

MediaDock 默认使用本机 SQLite 保存搜索候选和任务记录，服务重启后不会丢失有效状态。获取链路支持 API-only 模式：未配置 `DOWNLOAD_INCOMING_DIR` 时，MediaDock 不要求挂载本地媒体盘，由远端下载器决定目标目录。

## 快速启动

### 直接运行（macOS/Linux）

```sh
cp .env.example .env
# 编辑 .env 配置已部署的 PanSou、Prowlarr 和/或 Transmission 地址。
# 仅本地开发时可以设置 AUTH_DISABLED=true；生产环境请使用默认生成的 token。
set -a
. ./.env
set +a
go run ./cmd/media-dock
```

即使没有配置任何外部组件，MediaDock 也可以启动为搜索/获取能力为空的核心服务；完整链路需要配置可达的 provider 和 downloader。

### Docker Compose

Docker Desktop 启动后：

```sh
cp .env.example .env
# 在 .env 中填写组件地址；本地开发可临时设置 AUTH_DISABLED=true
docker compose up --build
```

Compose 默认只构建和启动 MediaDock，数据保存在 `./data/config`，容器内对应 `/config`。PanSou、Prowlarr、Transmission 不会被自动安装，需单独部署或在另一个 Compose 项目中运行，再将容器实际可达的地址写入 `.env`。

如果希望在本机一次启动 PanSou、Prowlarr 和 qBittorrent，请使用 [`deploy/README.md`](deploy/README.md) 与 [`deploy/docker-compose.full.yml`](deploy/docker-compose.full.yml)；它们是完整本地验证栈，默认只把管理端口绑定到 `127.0.0.1`。

检查服务：

```sh
curl http://127.0.0.1:8080/healthz
# 浏览器访问 http://127.0.0.1:8080/
```

标准 MCP 客户端连接：

```text
http://127.0.0.1:8080/mcp
```

WebUI 入口为 `http://127.0.0.1:8080/`。首次进入时在右上角输入 `AUTH_TOKEN_FILE` 对应文件中的 token；仅在显式开发模式 `AUTH_DISABLED=true` 时可以留空。WebUI 只调用已有 REST 接口，不提供任意 URL、Shell 或容器管理能力。

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
$body = @{ candidate_id = "candidate_xxx"; goal = "save_to_cloud"; target_profile = "openlist_default"; target_dir = "/夸克网盘/MediaDock"; confirmed = $true; idempotency_key = "request_xxx" } | ConvertTo-Json
Invoke-RestMethod -Method Post `
  -Uri http://127.0.0.1:8080/api/v1/acquisitions `
  -ContentType "application/json" `
  -Body $body
```

已知 OpenList 文件 COPY：

```powershell
$body = @{ target_profile = "openlist_default"; source_path = "/夸克网盘/Incoming/movie.mkv"; target_dir = "/夸克网盘/MediaDock"; skip_existing = $true; confirmed = $true; idempotency_key = "copy_xxx" } | ConvertTo-Json
Invoke-RestMethod -Method Post `
  -Uri http://127.0.0.1:8080/api/v1/copies `
  -ContentType "application/json" `
  -Body $body
```

查询任务：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/api/v1/jobs/job_xxx
```

## 配置

复制 `.env.example` 为 `.env` 后配置：

| 变量                                          | 作用                                                                                    |
| --------------------------------------------- | --------------------------------------------------------------------------------------- |
| `STORAGE_SQLITE_PATH` / `SQLITE_PATH`         | SQLite 数据库路径，默认 `./data/mediadock.db`；容器建议使用 `/config/mediadock.db`      |
| `MCP_ENABLED`                                 | 是否启用标准 MCP Streamable HTTP 端点，默认 `true`                                      |
| `MCP_PATH`                                    | MCP 端点路径，默认 `/mcp`                                                               |
| `AUTH_TOKEN` / `MCP_AUTH_TOKEN`               | REST 与 MCP 共用的固定 Bearer Token；后者为兼容旧配置                                   |
| `AUTH_TOKEN_FILE`                             | token 文件路径；默认 `./data/auth-token`，不存在时自动生成并以 `0600` 保存              |
| `AUTH_DISABLED`                               | 显式允许无认证开发模式，默认 `false`                                                    |
| `PANSOU_BASE_URL`                             | PanSou / pansou-web 地址                                                                |
| `PROWLARR_BASE_URL`                           | 可选，Prowlarr 地址                                                                     |
| `PROWLARR_API_KEY`                            | Prowlarr API Key                                                                        |
| `DOWNLOADERS`                                 | 可选，逗号分隔的获取适配器：`transmission`、`qbittorrent`、`openlist`；留空表示搜索模式 |
| `TRANSMISSION_RPC_URL`                        | Transmission RPC 地址                                                                   |
| `TRANSMISSION_USER` / `TRANSMISSION_PASSWORD` | Transmission 认证                                                                       |
| `QBITTORRENT_URL`                             | qBittorrent Web API 地址；当前适配器追踪磁力链接                                        |
| `QBITTORRENT_USER` / `QBITTORRENT_PASSWORD`   | qBittorrent WebUI/API 认证                                                              |
| `OPENLIST_BASE_URL`                           | 可选，OpenList fork 的服务根地址，例如 `http://openlist:5244`                           |
| `OPENLIST_AUTH_TOKEN`                         | OpenList API 的原始 `Authorization` 值；不要添加 `Bearer ` 前缀                         |
| `OPENLIST_DEST_DIR`                           | 兼容 fallback；新请求的 `target_dir` 优先，留空时不提供固定目标                         |
| `OPENLIST_TARGET_PROFILE`                     | 暴露给 MediaDock 调用方的目标 profile ID，默认 `openlist_default`；不是路径             |
| `DOWNLOAD_INCOMING_DIR`                       | 可选的本地下载临时目录；留空时使用 API-only 模式，不要求 MediaDock 挂载媒体盘           |

只有配置了 `DOWNLOAD_INCOMING_DIR` 时，服务和 Transmission 才必须使用双方共享卷中的同一个容器路径；不能直接使用宿主机路径替代容器内路径。默认 API-only 模式不需要该目录。

`DOWNLOADERS` 留空时服务仍可正常搜索，但确认获取会返回 `downloader_unavailable`。qBittorrent 适配器只接收带有可解析 info hash 的磁力链接；Transmission 按其适配器能力处理 BT 候选，默认对应 `download_to_local`。配置并启用 `openlist` 后，MediaDock 会将受支持的夸克、AliyunDrive 和 BaiduNetdisk 分享候选通过 `/api/fs/transfer` 转存，并把已知 OpenList 文件通过独立的 `/api/fs/copy` 任务复制到调用方传入的 `target_dir`。OpenList 自己负责目标权限、源文件存在性和实际数据流；MediaDock 不再调用 `/api/fs/list` 预检。`OPENLIST_DEST_DIR` 仅是未传 `target_dir` 时的兼容 fallback。

OpenList 的 API Token 通过 `Authorization` 请求头原样发送；请使用 OpenList 专用且权限受限的身份。转存和 COPY 提交请求会带 `Idempotency-Key` 和 `X-MediaDock-Job-ID`。COPY 复用 OpenList 原生任务接口：提交 `/api/fs/copy` 后读取 `data.tasks[0].id`，再查询 `/api/task/copy/info?tid=...`，取消时调用 `/api/task/copy/cancel?tid=...`；MediaDock 不要求 Fork 增加 `/api/fs/copy/status`。分享转存完成记录为 `saved`/`transferred`；COPY 完成记录为 `phase=downloaded`、`status=downloaded`，含义是文件已到 OpenList 目标目录，不表示文件经过 MediaDock 或已落到本地 NAS。启用方式是在填好 OpenList 配置后，将 `openlist` 加入 `DOWNLOADERS`，例如 `DOWNLOADERS=qbittorrent,openlist`。协议详见 [`docs/openlist-mediadock-protocol.md`](docs/openlist-mediadock-protocol.md)。

## LLM 接口边界

首选接口是标准 MCP Streamable HTTP：

```text
POST /mcp
GET /mcp
DELETE /mcp
```

服务端使用官方 MCP Go SDK 管理会话和 JSON-RPC，不自定义 `tools/list` 或 `tools/call` 协议。MCP 暴露八个工具：

- `media_search`：搜索资源，不下载；
- `media_acquire`：用户确认后创建候选获取任务，分享转存时传入 `target_dir`；
- `media_copy`：用户确认后通过 OpenList COPY 已知源文件到目标目录；
- `media_job_status`：查询任务状态；
- `media_job_reconcile`：核对不确定的外部转存结果，不会盲目重提交；
- `media_job_cancel`：取消任务；
- `media_jobs_list`：分页重新发现近期、进行中或失败任务；
- `media_capabilities`：查看当前实际可用的搜索平台和下载器能力。

`GET /api/v1/capabilities` 提供与 `media_capabilities` 对应的 REST 能力说明；`GET /api/v1/llm/tools` 是兼容旧客户端的 OpenAI 风格函数定义发现接口，不是 MCP 协议实现。

LLM 不直接执行 shell、访问原始分享链接或操作文件系统。普通搜索响应和 MCP 只使用候选 ID 和任务 ID，真实材料只在服务内部交给对应适配器；已认证的人工 WebUI 可以通过 `GET /api/v1/searches/{id}/sources` 在当前搜索列表中查看原始链接和分享密码。候选类型会区分 `magnet`、`torrent`、`http_file`、`cloud_share` 和 `unknown`；启用 OpenList 后，只有域名和链接结构属于 fork 支持范围的 `cloud_share` 候选才可获取。

`media_acquire` 和 `media_copy` 的副作用边界由两层共同保证：MCP 工具说明要求先展示候选或已知文件并取得用户明确选择，应用服务还会强制校验 `confirmed: true`。`goal` 只能是 `save_to_cloud` 或 `download_to_local`；`target_profile` 只能是 `media_capabilities`/`/api/v1/capabilities` 返回的服务端 profile。OpenList 的 `target_dir`、COPY 的 `source_path` 和覆盖选项是明确的业务参数，但仍必须经过用户确认并由 OpenList 再做权限校验。即使调用方绕过 MCP 直接访问 REST 接口，也不能跳过确认门；如需可验证的人类审批，应在可信 WebUI/客户端增加独立 approval，而不能把模型自报的布尔值当作强授权凭证。

## 下一阶段

当前获取入口支持 BT `download_to_local`（交给已配置下载器）、OpenList `save_to_cloud`（分享转存）和 OpenList `media_copy`（网盘内 COPY）。后续如接入独立下载执行端，再扩展 cloud-share 的 `resolving → downloading → downloaded` 阶段；在此之前不会在 MediaDock 内实现文件传输引擎。后续优先级是：

1. 结构化 YAML 配置、`*_file` 凭据读取和旧环境变量迁移；
2. 真实组件健康检查，区分未配置、就绪、不可达、未授权和不兼容；
3. 候选 TTL 清理、数据库迁移/备份和更完整的任务事件记录；
4. 验证 MediaDock 到 OpenList fork 的真实集成链路，并补充各外部服务的契约覆盖；
5. 在不把组件 API 密钥或服务凭据写入网页的前提下，扩展 WebUI 的连接诊断和配置引导。

完整架构说明见 [`docs/architecture.md`](docs/architecture.md)。

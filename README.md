# Media Scout

面向 LLM 的自部署媒体资源搜索与获取编排服务。

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

- Go 1.22 模块化单体服务；
- PanSou 搜索适配器（`POST /api/search`）；
- Prowlarr 兼容索引器适配器（`GET /api/v1/search`）；
- 候选资源统一模型、去重、质量/字幕/做种评分；
- 候选资源句柄隔离：API 不返回原始分享链接、密码和 provider payload；
- 用户确认门：没有 `confirmed: true` 不会创建获取任务；
- 可配置的下载器适配器；当前包含 Transmission RPC，支持磁力和 torrent/HTTP 下载链接；
- 面向 LLM 的 OpenAI 风格工具定义接口；
- 基础测试和 Docker 镜像构建文件。

当前搜索和任务状态使用内存存储，服务重启后会丢失搜索候选和任务记录。这是第一条垂直链路，下一步应替换为 SQLite 或 PostgreSQL。

## 快速启动

```powershell
Copy-Item .env.example .env
$env:PANSOU_BASE_URL = "http://127.0.0.1:80"
$env:DOWNLOADERS = "transmission"
$env:TRANSMISSION_RPC_URL = "http://127.0.0.1:9091/transmission/rpc"
go run ./cmd/media-scout
```

检查服务：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
Invoke-RestMethod http://127.0.0.1:8080/api/v1/providers
Invoke-RestMethod http://127.0.0.1:8080/api/v1/downloaders
Invoke-RestMethod http://127.0.0.1:8080/api/v1/llm/tools
```

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

| 变量 | 作用 |
| --- | --- |
| `PANSOU_BASE_URL` | PanSou / pansou-web 地址 |
| `PROWLARR_BASE_URL` | 可选，Prowlarr 地址 |
| `PROWLARR_API_KEY` | Prowlarr API Key |
| `DOWNLOADERS` | 可选，逗号分隔的下载器名称；留空表示搜索模式 |
| `TRANSMISSION_RPC_URL` | Transmission RPC 地址 |
| `TRANSMISSION_USER` / `TRANSMISSION_PASSWORD` | Transmission 认证 |
| `DOWNLOAD_INCOMING_DIR` | 下载临时目录，必须是服务和下载器都能看到的路径 |

如果服务和 Transmission 在不同容器中运行，`DOWNLOAD_INCOMING_DIR` 必须使用双方共享卷中的同一个容器路径；不能直接使用宿主机路径替代容器内路径。

`DOWNLOADERS` 留空时服务仍可正常搜索，但确认获取会返回 `downloader_unavailable`。这样可以先部署搜索平台，再按需启用下载器。后续新增 OpenList、aria2 等适配器时，只需增加适配器并在此配置中启用。

## LLM 接口边界

`GET /api/v1/llm/tools` 返回四个工具定义：

- `media_search`：搜索资源，不下载；
- `media_acquire`：用户确认后创建获取任务；
- `media_job_status`：查询任务状态；
- `media_job_cancel`：取消任务。

LLM 不直接执行 shell、访问原始分享链接或操作文件系统。它只使用候选 ID 和任务 ID，真实链接只在服务内部交给对应适配器。

## 下一阶段

按用户需求，后续优先级是：

1. 持久化搜索会话、候选和任务；
2. 增加 OpenList/云盘转存适配器；
3. 下载完成检测和文件稳定性检查；
4. 视频、字幕、压缩包识别与目录整理；
5. Jellyfin 刷新和入库验证；
6. 通知适配器和失败重试。

完整架构说明见 [`docs/architecture.md`](docs/architecture.md)。

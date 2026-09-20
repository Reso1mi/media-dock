# MediaDock 架构与边界

## 目标

MediaDock 是面向 AI 的轻量媒体组件编排服务。它通过 MCP 暴露统一能力，连接已有搜索源、索引器、下载器和其他媒体服务，不重新实现这些组件。

核心目标是让 AI 能安全地完成：

```text
搜索媒体资源 → 让用户选择 → 调用现有下载器 → 监控 → 整理 → 进入 Jellyfin → 通知
```

第一版只聚焦搜索平台、候选资源模型、确认门和下载器对接。NAStool 和 mediary-scout 是设计参考，不是运行时依赖。
对 LLM 暴露的主协议是官方 MCP Streamable HTTP；REST API 作为管理、调试和旧客户端兼容层保留。

## 分层

```text
┌──────────────────────────────────────────────┐
│ Hermes / Web / Bot / LLM                     │
│ 解析需求、展示候选、获得确认、查询进度        │
└──────────────────────┬───────────────────────┘
                       │ MCP Streamable HTTP / REST
┌──────────────────────▼───────────────────────┐
│ Protocol Adapters + Application Services      │
│ MCP JSON-RPC / REST / 管理接口                │
│ SearchService / AcquisitionService / 状态门    │
└───────────────┬─────────────────┬─────────────┘
                │                 │
┌───────────────▼────────┐ ┌──────▼────────────┐
│ Search Providers        │ │ Downloaders       │
│ PanSou / Prowlarr       │ │ 可配置适配器       │
│ 后续可接更多索引器       │ │ Transmission/...  │
└────────────────────────┘ └───────────────────┘
                │                 │
                └────────┬────────┘
                         ▼
              /volume2/Media/Downloads
                         │
                  后续媒体流水线
                         │
        /volume2/Media/TV /Anime /Movie
                         │
                      Jellyfin
```

## MCP 协议边界

服务端使用 `github.com/modelcontextprotocol/go-sdk` 提供标准 MCP Server，端点默认为 `/mcp`，采用 Streamable HTTP 传输。MCP 客户端通过标准生命周期完成：

```text
initialize → notifications/initialized → tools/list → tools/call
```

工具调用最终进入同一套 `SearchService` 和 `AcquisitionService`，因此 MCP 与 REST 不会形成两套业务规则。当前工具为：

- `media_search`：并行调用已配置的搜索 provider，返回不含原始链接的候选句柄；
- `media_acquire`：只接受候选 ID，并要求 `confirmed=true`；
- `media_job_status` / `media_job_cancel`：查询或取消获取任务；
- `media_jobs_list`：分页重新发现近期、进行中或失败任务；
- `media_capabilities`：发现当前 provider/downloader 能力。

`GET /api/v1/capabilities` 返回同一能力契约的 REST 表达，`GET /api/v1/llm/tools` 仅返回 OpenAI 风格函数定义，用于兼容旧客户端，不替代 MCP。MCP 的工具 schema、结构化输出和错误结果由官方 SDK 负责序列化。

MCP 输出刻意不包含 `RawURL`、分享密码、provider 原始 payload、下载器远程 ID 或临时目录。候选句柄只在服务端 Store 中解析，LLM 无法构造任意下载地址或目标路径。

## 搜索模型

搜索 provider 只需要实现：

```go
type Provider interface {
    Name() string
    Search(context.Context, domain.SearchRequest) ([]domain.Candidate, error)
}
```

provider 返回的资源都归一化为 `domain.Candidate`，然后由 `SearchService` 完成：

- 查询词清理；
- 并行调用多个 provider；
- 记录每个 provider 的健康状态；
- 识别画质、编码、字幕和完整性；
- 按 URL/资源特征去重；
- 依据用户偏好排序；
- 生成本次搜索专属的候选 ID。

### PanSou

使用 PanSou 的 `/api/search` 接口，以 `kw` 和 `res=all` 查询。一个搜索结果可能包含多个链接，因此每个链接都会成为独立候选：

- 115、夸克、123 等分享链接归类为 `cloud_share`；
- `magnet:` 归类为 `magnet`；
- 明确的 torrent 地址归类为 `torrent`；
- 有明确媒体/归档文件扩展名的直链归类为 `http_file`；
- 无法确认材料类型的普通 HTTP 地址归类为 `unknown`，不会直接宣称可获取。

### Prowlarr

使用 Prowlarr 的 `/api/v1/search` 接口，并将 `magnetUrl`、`downloadUrl` 或 `guid` 转换为下载候选。Prowlarr 适合承接 NAStool 中“索引器聚合”的职责；具体站点管理仍由 Prowlarr 完成。

## 候选句柄与副作用隔离

搜索 API 只返回：

- 候选 ID；
- 标题、来源和资源类型；
- 大小、画质、字幕、做种数和评分；
- 是否需要确认。

不返回：

- 原始分享链接；
- 分享密码；
- provider 的完整原始 payload。

真实内容保存在服务端的候选注册表中。用户确认后，获取服务根据候选 ID 找到真实链接，再交给下载器。

这样可以避免 LLM 复述、篡改或臆造下载链接，也使“搜索结果”和“可执行获取计划”绑定在同一份快照上。

## 获取接口

下载器只需要实现：

```go
type Downloader interface {
    Name() string
    Supports(domain.Candidate) bool
    Start(context.Context, string, domain.Candidate, string) (Handle, error)
    Status(context.Context, string) (RemoteStatus, error)
    Cancel(context.Context, string) error
}
```

当前实现 Transmission：

- 通过 `torrent-add` 添加 magnet 或明确的 torrent 地址；
- 处理 Transmission 的 409 session challenge，并校验 RPC `result` 必须为 `success`；
- 通过 `torrent-get` 查询进度；
- 通过 `torrent-remove` 取消任务；
- 配置本地临时目录时使用 `DOWNLOAD_INCOMING_DIR/<job_id>`，留空时不发送 `download-dir`，进入 API-only 模式。

下载器不是必选组件。通过 `DOWNLOADERS` 使用逗号分隔的名称启用，例如：

```text
DOWNLOADERS=transmission
```

留空时服务运行在搜索模式；`/api/v1/downloaders` 可查看实际启用的适配器。未知名称只会记录警告并忽略，不会阻止搜索服务启动。

云盘搜索结果目前可以被安全地展示和选择，但尚未内置网盘转存。下一步应增加独立的 `CloudAcquirer`/`Downloader` 适配器，不要把 OpenList 或某个网盘 API 写进搜索服务。

## 任务状态

当前获取任务：

```text
queued → acquiring → downloading → downloaded
                         ├→ failed
                         ├→ cancelled
                         └→ unsupported
```

完整媒体流程后续扩展为：

```text
downloaded
  → verifying
  → identifying
  → organizing
  → jellyfin_refreshing
  → completed
```

每个状态都应由后台任务持久化，不能依赖聊天记录。MCP 会话只是调用入口，不承载业务状态；任务和候选由 `store.Store` 管理，默认实现为本机 SQLite。候选的 `RawURL`、密码和原始 payload 作为服务端私有执行材料单独保存，不能直接把对外 JSON 当作数据库快照。SQLite 文件应放在本机配置卷中，不放在远程 SMB/NFS 媒体挂载中。

## 安全和可靠性要求

- 搜索必须经过显式确认；
- REST 业务接口和 MCP 端点使用同一 Bearer Token；默认生成并持久化 token，只有显式开发模式才允许无认证；`/healthz` 只暴露最少存活信息；
- Streamable HTTP 服务不能设置会截断长期 SSE 响应的全局写超时；
- 下载器只接受白名单候选类型；
- 临时目录和正式媒体库隔离；
- 不允许 LLM 直接传入任意目标路径；
- 后续文件移动必须校验目标路径位于 `/volume2/Media` 内；
- 搜索会话和候选必须设置 TTL；
- provider 可以部分失败，不能因为一个索引器异常阻断其他结果；
- 下载完成后要重新读取真实文件状态，不能仅凭下载器返回值标记入库；
- 删除和覆盖操作默认关闭，优先进入 quarantine。

## 为什么先做模块化单体

当前部署目标是单用户 NAS，搜索、获取和后续媒体整理之间需要共享候选句柄和任务状态。第一阶段使用一个 Go 服务加后台 Worker 更合适；默认获取只调用下载器 API，不要求 MediaDock 挂载媒体盘：

- 调试链路短；
- 适合 Docker Compose 部署；
- provider 和 downloader 已经通过接口隔离；
- 将内存 Store 替换为 SQLite/PostgreSQL 后，仍可保持相同业务边界。

没有必要在搜索平台尚未稳定之前拆成多个微服务。

# MediaDock ↔ OpenList fork 窄 HTTP 协议

本文定义 MediaDock 获取服务与 OpenList fork 之间的**最小 HTTP 合同**。它不是 OpenList 全量 API 的代理，也不要求 MediaDock 调用 OpenList MCP。

## 1. 边界和调用方向

```text
AI / WebUI / REST 客户端
          │
          ▼
MediaDock MCP / REST
          │  候选、确认、幂等、任务、恢复
          ▼
MediaDock OpenList adapter
          │  窄 HTTP API
          ▼
OpenList fork
          │  权限、驱动、网盘任务、文件 COPY
          ▼
OpenList storage
```

- MediaDock 是媒体业务入口：搜索候选、要求确认、创建任务、持久化状态并在重启后恢复。
- OpenList fork 只需要适配两个媒体获取动作：分享转存和已知文件 COPY。登录、Cookie、网盘驱动、ACL、实际数据流仍由 OpenList 负责。
- MediaDock **不调用 OpenList MCP**，也不复制 `fs.list`、`fs.get`、`fs.link` 等通用网盘工具。
- OpenList 自己的 MCP 可以继续给独立场景使用，例如浏览目录、查看文件和获取访问链接。
- 文件内容默认不经过 MediaDock。MediaDock 保存任务和受约束的 OpenList 文件引用；需要下载时，使用 OpenList 的 COPY，而不是把链接下载到 MediaDock。

MediaDock 对外暴露两个不同的业务动作：

| MediaDock 动作                                | 业务含义                                 | OpenList 操作           |
| --------------------------------------------- | ---------------------------------------- | ----------------------- |
| `media_acquire` / `POST /api/v1/acquisitions` | 用户确认搜索得到的分享候选               | `POST /api/fs/transfer` |
| `media_copy` / `POST /api/v1/copies`          | 用户确认一个已知 OpenList 文件和目标目录 | `POST /api/fs/copy`     |

## 2. 认证、请求头和目录语义

所有 OpenList 请求使用 MediaDock 配置的 OpenList `Authorization` 原值：

```http
Authorization: <OpenList token value>
Content-Type: application/json
Accept: application/json
```

转存和 COPY 的提交请求额外带：

```http
Idempotency-Key: <MediaDock job ID>
X-MediaDock-Job-ID: <MediaDock job ID>
```

OpenList fork 可以用这两个值做远端幂等关联和审计。MediaDock 不会因为网络超时而更换 key 盲目重试。

### 目标目录

目录由 MediaDock 的业务请求传入：

- `media_acquire` 的 `target_dir` 作为分享转存的 `dst_dir`；
- `media_copy` 的 `target_dir` 作为 COPY 的 `dst_dir`；
- `media_copy` 的 `source_path` 是 OpenList 中已经确认的源文件绝对路径。

`target_profile` 只选择服务端配置的 OpenList 实例、凭据和能力，不是路径。路径是 OpenList 虚拟文件系统路径，不是 MediaDock 容器的本地路径。OpenList 仍必须对源和目标执行自己的权限、存在性和驱动检查。

旧部署可以保留 `OPENLIST_DEST_DIR` 作为未提供 `media_acquire.target_dir` 时的兼容 fallback；它不再是新接口的主语义，也不适用于 `media_copy`。新客户端应始终传入目录。

MediaDock adapter 会拒绝相对路径、`.`、`..`、反斜杠和 NUL；OpenList 仍是最终权限判断者。MediaDock 不通过 `/api/fs/list` 预检目录：预检会产生 TOCTOU 窗口，而且会把通用浏览 API 扩大成依赖。实际 `transfer`/`copy` 请求自己负责返回权限和参数错误。

## 3. 分享转存接口

### 3.1 提交：`POST /api/fs/transfer`

```json
{
  "url": "https://pan.quark.cn/s/example",
  "dst_dir": "/夸克网盘/MediaDock",
  "valid_code": "1234"
}
```

- `url` 和 `valid_code` 来自 MediaDock 服务端保存的候选材料，不从普通搜索响应或 MCP 输出泄露给模型。
- `dst_dir` 来自本次 `media_acquire` 请求；只有兼容旧客户端时才使用 `OPENLIST_DEST_DIR` fallback。
- OpenList 负责识别支持的分享驱动、校验提取码、检查目标权限并执行转存。

### 3.2 同步和异步响应

响应使用 OpenList 的外层结构：

```json
{
  "code": 200,
  "message": "accepted",
  "data": {
    "operation_id": "ol-transfer-01H",
    "status": "transferring",
    "progress": 0.25,
    "message": "copying share contents",
    "error": "",
    "files": []
  }
}
```

字段约定：

| 字段           | 类型   | 约定                                                           |
| -------------- | ------ | -------------------------------------------------------------- |
| `code`         | number | `200` 表示请求被 OpenList 接受或同步完成                       |
| `operation_id` | string | 异步操作的稳定 ID；`task_id` 可作为兼容别名                    |
| `status`       | string | `queued`、`accepted`、`transferring`、`completed`、`failed` 等 |
| `progress`     | number | `0` 到 `1`；省略时按 `0` 处理                                  |
| `message`      | string | 非敏感进度信息                                                 |
| `error`        | string | 不得包含 token、Cookie 或分享密码                              |
| `files`        | array  | 可选的目标文件引用，见第 6 节                                  |

现有同步 fork 可以返回：

```json
{
  "code": 200,
  "message": "success",
  "data": null
}
```

`code=200` 且 `data=null` 表示 OpenList 已报告本次转存完成。MediaDock 将任务置为 `status=transferred`、`phase=saved`，但不会猜测文件名或生成下载 URL。

明确的参数、认证或权限拒绝（HTTP 4xx，或 JSON `code` 为 4xx）记录为普通失败；它说明 OpenList 没有接受本次操作。HTTP 5xx、连接中断、超时或无法解析响应无法证明操作是否已被接受，MediaDock 才会标记为 uncertain，并禁止盲目重提交。异步进行中的响应如果没有 `operation_id` 也按 uncertain 处理；只有文档规定的 `data:null` 才能表示同步完成。

### 3.3 查询：`POST /api/fs/transfer/status`

请求：

```json
{
  "operation_id": "ol-transfer-01H"
}
```

响应使用相同的外层结构。异步 fork 应返回相同的 `operation_id`（或可由 `task_id` 映射），并提供非空 `status`；状态查询返回 `data:null` 或缺少状态时，MediaDock 将其视为状态查询失败而不是完成。MediaDock 只查询，不会因为状态查询失败再次调用 `/api/fs/transfer`。

## 4. OpenList COPY 接口

COPY 是第二条独立的 MediaDock 业务链，用于把 OpenList 已存在的文件复制到另一个 OpenList 目录。它不需要候选句柄，也不把分享 URL 重新转存一次。

### 4.1 提交：`POST /api/fs/copy`

MediaDock 将单文件路径拆分为 OpenList 的目录和文件名：

```json
{
  "src_dir": "/夸克网盘/Incoming",
  "dst_dir": "/夸克网盘/MediaDock",
  "names": ["movie.mkv"],
  "overwrite": false,
  "skip_existing": true,
  "merge": false
}
```

约定：

- 第一版 `names` 只包含一个经过确认的文件名；OpenList 可以在服务端拒绝目录或批量输入。
- `src_dir` 和 `dst_dir` 都是 OpenList 虚拟目录；MediaDock 不把它们当作本地 OS 路径。
- `overwrite` 与 `skip_existing` 不能同时为 `true`。MediaDock 默认使用 `skip_existing=true`，除非明确请求覆盖。
- OpenList 负责源文件存在性、目标目录权限、同名处理、驱动能力和实际 COPY 数据流。

上游 OpenList 的异步 COPY 响应使用已有任务系统，而不是在 `/api/fs` 下提供另一套状态资源：

```json
{
  "code": 200,
  "message": "success",
  "data": {
    "message": "Successfully created 1 copy task(s)",
    "tasks": [
      {
        "id": "ol-copy-task-01H",
        "state": 0,
        "status": "uploading",
        "progress": 25,
        "error": ""
      }
    ]
  }
}
```

MediaDock 第一版只提交一个已经确认的文件，因此要求返回至多一个任务；`tasks[0].id` 是 MediaDock 保存的远端 operation ID。OpenList 原生 `state` 使用 `tache` 状态值，MediaDock 将成功、取消和失败状态分别映射为完成、取消和失败；`progress` 同时兼容 OpenList 常见的 `0..100` 和 fork 返回的 `0..1`。

如果源、目标和驱动支持同步 COPY，上游 OpenList 会返回类似下面的结果，而不会创建任务：

```json
{
  "code": 200,
  "message": "success",
  "data": {
    "message": "Copy operations completed immediately"
  }
}
```

### 4.2 查询：`POST /api/task/copy/info?tid=<task_id>`

这是 OpenList 已有的通用任务接口，MediaDock 不要求 Fork 为 COPY 另建 `/api/fs/copy/status`。请求体为空（实现可以发送 JSON `null`）：

```http
POST /api/task/copy/info?tid=ol-copy-task-01H
```

响应的 `data` 是 OpenList `TaskInfo`：

```json
{
  "id": "ol-copy-task-01H",
  "state": 2,
  "status": "uploading",
  "progress": 100,
  "error": "",
  "end_time": "2026-09-23T12:00:00Z"
}
```

`state=2` 表示成功、`state=4` 表示已取消、`state=7` 表示最终失败；其他状态仍视为进行中。MediaDock 仍会校验返回的任务 ID，并且只查询，不会因为状态查询失败重新提交 COPY。

当前 MediaDock 对旧的实验性 Fork 还兼容回退到 `POST /api/fs/copy/status`，但这不是上游 OpenList 合同，也不是 Fork 的必需实现。

### 4.3 取消：`POST /api/task/copy/cancel?tid=<task_id>`

取消同样复用 OpenList 原生任务接口：

```http
POST /api/task/copy/cancel?tid=ol-copy-task-01H
```

OpenList 会按当前认证用户校验任务归属，并只取消仍可取消的任务。当前 MediaDock 对旧 Fork 兼容回退到 `/api/fs/copy/cancel`，但新 Fork 不需要实现该路径。分享转存没有统一取消合同；MediaDock 不会伪造取消。

同步 COPY 没有远端任务 ID，MediaDock 直接记录 COPY 完成。异步 COPY 保存 `tasks[0].id`，后续通过上述原生任务接口查询或取消。

## 5. MediaDock 状态和恢复语义

对外兼容的 `status` 与更精确的 `phase` 分开保存：

```text
media_acquire / 分享转存：
queued → acquiring → downloading(phase=transferring)
                    → transferred(phase=saved)
                    → transfer_uncertain(phase=uncertain)

media_copy / OpenList COPY：
queued → acquiring → downloading(phase=copying)
                    → downloaded(phase=downloaded)
                    → submission_uncertain(phase=uncertain)
```

这里的 `downloaded` 对 COPY 的含义是：文件已经 COPY 到指定的 OpenList 目标目录；它不表示文件经过 MediaDock，也不表示 NAS 本地路径已经产生。

- 提交返回 pending：保存远端 `operation_id`，Worker 后续查询对应的 transfer 或 COPY status。
- 提交同步成功：保存目标目录引用并进入终态。
- 请求超时、连接中断、响应无法解析，或进程在外部提交后崩溃：结果可能已被 OpenList 接受，MediaDock 标记为不确定，不自动重新提交。
- 重启后处于 `acquiring` 的任务也不重发；Worker 将其标为 `transfer_uncertain` 或 `submission_uncertain`。
- `media_job_reconcile` 只查询已保存的远端 operation，不重新执行 transfer/COPY。没有可查询 operation 的旧任务需要人工检查目标。
- 已完成或取消的任务不会再次轮询，也不会再提交。

SQLite 至少保存：`operation`、`source_path`、`target_dir`、COPY 选项、OpenList operation ID、目标引用、请求摘要、状态和恢复动作。幂等键同时绑定完整意图（候选/源文件、目标目录、profile 和选项）；同 key 的不同意图返回冲突。

## 6. 文件引用

OpenList 操作可以返回：

```json
{
  "path": "/夸克网盘/MediaDock/movie.mkv",
  "name": "movie.mkv",
  "size_bytes": 7340032,
  "etag": "provider-etag",
  "is_dir": false
}
```

MediaDock 只接受目标目录本身或其子路径内的绝对引用，并过滤越界、相对和无法规范化的路径。它保存的是 `instance`、`profile_id`、operation、目标路径、operation ID 和文件定位信息，不保存签名 URL、Cookie、临时请求头或分享密码作为文件引用。

如果以后需要链接访问，应由 OpenList 自己的 HTTP API/MCP 根据保存的文件引用解析访问方式；不要把裸链接当成 COPY 的替代，也不要让文件数据默认穿过 MediaDock。

## 7. MCP 边界

MediaDock MCP 保持媒体业务工具：

- `media_search`：产生候选句柄；
- `media_acquire`：确认后执行分享转存或其他配置的媒体获取；
- `media_copy`：确认后执行 OpenList 文件 COPY；
- `media_job_status`、`media_job_reconcile`、`media_job_cancel`、`media_jobs_list`：管理 MediaDock 自己创建的任务；
- `media_capabilities`：发现可用 profile 和 operation。

OpenList MCP 仍可独立暴露网盘浏览和链接能力。一次性的、不需要 MediaDock 任务管理的网盘操作可以直接走 OpenList MCP；但“搜索 → 选择 → 转存 → COPY → 跟踪”主流程只使用 MediaDock MCP，避免一个操作同时产生两套未关联的任务记录。

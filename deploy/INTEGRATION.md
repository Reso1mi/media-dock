# WSL / NAS 联调记录

验证日期：2026-09-22。WSL 工作目录：`/home/resolmi/alpha/media-dock`。

## 已部署

使用 `deploy/docker-compose.integration.yml`，与原有完整栈和 NAS 部署隔离。

| 服务          | 本机入口                         | 版本                                     |
| ------------- | -------------------------------- | ---------------------------------------- |
| MediaDock     | http://127.0.0.1:8080            | 当前工作区；包含 OpenList 获取适配器代码 |
| OpenList fork | http://127.0.0.1:5244            | Reso1mi/OpenList `05408c0d`，前端 v4.2.6 |
| PanSou        | http://127.0.0.1:8888/api/health | Compose 固定镜像 digest                  |
| Prowlarr      | http://127.0.0.1:9696            | 2.6.5.5623                               |
| qBittorrent   | http://127.0.0.1:8081            | 5.2.3                                    |

管理端口均绑定回环地址；qBittorrent 6881 TCP/UDP 用于 BT peers。
这里的地址供当前 Windows/WSL 电脑使用，尚未配置 NAS Hermes 到 WSL 的网络接入。

```sh
docker compose -f deploy/docker-compose.integration.yml up -d
docker compose -f deploy/docker-compose.integration.yml ps
docker compose -f deploy/docker-compose.integration.yml stop
```

`data/integration/secrets/credentials.json` 保存本地服务账号和 MediaDock token。
`data/integration/secrets/mediadock.env` 是 Compose 使用的连接配置。
NAS 网盘凭据保存在 `data/integration/secrets/nas-cloud-storages.json`；只导出 cloud storage 记录，没有克隆 NAS 用户数据库或 SSH 私钥。
这些文件位于 Git 忽略的 `data/` 下，凭据文件权限为 0600，私有目录为 0700。不要把它们加入版本控制或粘贴进日志。

两个 Quark 挂载已导入并可列目录、转存。AliyundriveOpen 已导入但在测试实例禁用，因为该驱动不支持本次转存能力；BaiduNetdisk 的原始 errno 20016 认证错误仍存在，也保持禁用。

## 实测结果

| 链路                               | 结果                                                                                                  |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------- |
| PanSou → MediaDock                 | 真实查询返回资源；一次原始响应 391 条结果，经链接展开后 MediaDock 报告 2933 个候选材料并按 limit 返回 |
| Prowlarr 外部索引器                | Internet Archive 查询 Ubuntu 返回 100 条，Nyaa.si 返回 1 条；外网延迟会变化                           |
| Prowlarr → MediaDock → qBittorrent | 自生成 1 MiB 文件通过真实 BT peer 下载，SHA-256 匹配                                                  |
| MCP                                | initialize、tools/list、media_job_status 正常；当前 MediaDock MCP 暴露八个工具                        |
| 重复提交 / 重启                    | 同一幂等键复用 job；MediaDock 重启后任务保持 downloaded，远端 torrent 只有一份                        |
| WSL fork 分享转存                  | 用户提供的夸克分享成功转存至 `/夸克网盘/MediaDock-Integration-20260922`                               |
| WSL fork 网盘 → Local 直接链接复制 | **历史失败**：选取的 6,964,263 字节文件下载返回 HTTP 412；这不是新的 OpenList `/api/fs/copy` 合同验证 |
| NAS 原 OpenList 下载同一样本       | HTTP 206 范围读取正常，随后完整下载 6,964,263 字节并记录 SHA-256                                      |

分享中实测 100 个文件，总计 2,239,164,184 字节；按 fork 语义整份转存，下载测试只取其中最小文件。
NAS 样本保存在 `/volume2/Media/Downloads/media-dock-integration/20260922-quark/sample.bin`。
其 SHA-256 为 `19f2de7973c3c8be083ce66c3f9c2d0b6fce0583d3c159fb974de2c7c332cb72`。

这说明转存可用，NAS 原下载路径也可用；**不表示 WSL fork 已完成 MediaDock COPY 合同验证**。表中的转存由 OpenList fork 独立实测完成；MediaDock 的 OpenList adapter 现在通过窄的 transfer/COPY HTTP 合同工作，但还需用这套私有部署配置走通 `media_acquire` 和 `media_copy` 端到端调用后，才能把 MediaDock → OpenList 记作已实测。
WSL 生成的 CDN URL 用同一组请求头在 WSL 与 NAS 均得到 412，NAS 原 OpenList 生成的链接可用。根因尚未定位到出口、签名、Cookie 状态或版本差异中的某一项。
为对照出口而创建 SSH SOCKS 隧道的尝试被自动审批检查拒绝（`blocked by policy`），没有创建隧道。

## 本次修复

- SEARCH_TIMEOUT 传给实际 provider HTTP client，避免配置 90 秒却仍在 30 秒截断。
- 解析 Prowlarr 同源 magnetUrl 代理的磁力重定向，保留 tracker；不跟随外部重定向、不向外部主机发送 API key。
- 支持 qBittorrent 5.2.3 的 JSON 添加结果，核对 added_torrent_ids，避免远端已接受任务而 MediaDock 标记失败。
- 新增对应 API 回归测试，`go test ./...` 通过。

## 保留的验证材料

`data/integration/artifacts/` 包含 `bt-result.json`、`mcp-result.json`、`nas-result.json`、`cloud-inspect.json` 和复制任务信息。构建和测试日志位于 `data/integration/logs/`。
`data/integration/` 下保留了本次初始化、组件探测及联调脚本。脚本中的路径和分享仅用于本次环境，重新使用前应检查目标和大小。

`fixtures` Compose profile 提供本地 Torznab/tracker 和临时 qBittorrent seeder；这是自有文件的可重复接口/传输验证，不代表验证了任意公网 BT 资源的可下载性。测试结束后 fixture 索引器禁用、辅助容器停止，外部索引器恢复启用。

下一步分别验证 `media_acquire` / `POST /api/v1/acquisitions`（传入 `target_dir`）和 `media_copy` / `POST /api/v1/copies`（传入 `source_path`、`target_dir`）到 OpenList fork 的实际调用。当前接口和任务状态由同一 `AcquisitionService` 提供；转存完成记录为 `phase=saved`、兼容 `status=transferred`，COPY 完成记录为 `phase=downloaded`、`status=downloaded`，结果不明则记录为 `phase=uncertain` 并要求先核对对应 OpenList operation/目标。网盘内 COPY 不经过 MediaDock 文件流，也不把 cloud-share 转存宣称为本地下载。

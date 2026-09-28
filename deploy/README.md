# MediaDock 完整本地组件栈

这个目录提供一个适合本地验证的完整 Compose：

```text
MediaDock
  ├── PanSou       网盘搜索 API
  ├── Prowlarr     BT/索引器聚合
  └── qBittorrent  磁力获取和任务追踪
```

MediaDock 通过内部 Docker 网络访问这些组件，不需要把它们的内部地址改成宿主机地址。

## 组件和端口

| 服务                | 容器内地址                     | 宿主机地址              | 用途                     |
| ------------------- | ------------------------------ | ----------------------- | ------------------------ |
| MediaDock           | `http://media-dock:8080`       | `http://127.0.0.1:8080` | WebUI、REST、MCP         |
| PanSou              | `http://pansou:8888`           | `http://127.0.0.1:8888` | 网盘资源搜索 API         |
| Prowlarr            | `http://prowlarr:9696`         | `http://127.0.0.1:9696` | 索引器管理和搜索         |
| qBittorrent         | `http://qbittorrent:8080`      | `http://127.0.0.1:8081` | WebUI 和下载 API         |
| OpenList fork       | 外部配置的 `OPENLIST_BASE_URL` | 外部部署                | transfer / COPY HTTP API |
| qBittorrent torrent | `6881/tcp+udp`                 | `6881/tcp+udp`          | DHT/peer 流量，可选映射  |

当前 qBittorrent 适配器只接收可解析 info hash 的磁力链接。普通 `.torrent` URL 不会被宣称为可获取，这是为了保证任务重启后可对账、且不会误取消外部任务。

## 1. 准备配置

在项目根目录执行：

```sh
cp deploy/mediadock.env.example deploy/mediadock.env
```

`deploy/mediadock.env` 不要提交到 Git。至少需要在第一次启动后修改：

- `PROWLARR_API_KEY`：从 Prowlarr WebUI 获取；
- `QBITTORRENT_PASSWORD`：qBittorrent WebUI 的真实密码；
- 对外提供服务前把 `AUTH_DISABLED=false`，然后使用 MediaDock 自动生成的 token。

完整 Compose 当前显式设置 PanSou 的 `AUTH_ENABLED=false`，因为 MediaDock 的 PanSou 适配器尚未实现 PanSou JWT 登录。PanSou 管理端口只绑定到宿主机 `127.0.0.1`，仅适合本机验证；不要在未实现认证接入前把它暴露到公网或不可信局域网。

## 2. 启动完整栈

Docker Desktop 已启动时：

```sh
docker compose \
  -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  config

docker compose \
  -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  up -d --build
```

如果 Zed 终端里 `docker` 或 Docker Desktop credential helper 不在 PATH，先使用 Docker Desktop 自带目录：

```sh
export PATH="/Applications/Docker.app/Contents/Resources/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
docker compose \
  -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  up -d --build
```

查看状态和日志：

```sh
docker compose -f deploy/docker-compose.full.yml --env-file deploy/mediadock.env ps
docker compose -f deploy/docker-compose.full.yml --env-file deploy/mediadock.env logs -f media-dock
```

## 3. 初始化 Prowlarr

打开：

```text
http://127.0.0.1:9696
```

首次进入后：

1. 在 `Settings -> General` 找到 API Key；
2. 把 API Key 写入 `deploy/mediadock.env` 的 `PROWLARR_API_KEY`；
3. 在 `Indexers` 中添加并测试至少一个可用索引器；
4. 重新创建 MediaDock，使它读取新的 API Key：

```sh
docker compose -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  up -d --force-recreate --no-deps media-dock
```

Prowlarr 的下载客户端配置不是 MediaDock 获取链路的必需项。当前架构是：MediaDock 直接把磁力链接提交给 qBittorrent，Prowlarr 只负责索引器聚合和搜索。

判断 Prowlarr 是否真正参与搜索：在 MediaDock 搜索页查看“本次搜索执行情况”。`Prowlarr：已连接，但返回 0 条` 表示 MediaDock、API Key 和 Prowlarr API 都正常，但 Prowlarr 没有配置可用 indexer，或本次关键词没有匹配；`请求失败` 才表示连接、认证或版本等问题。Prowlarr 不会因为容器启动就自动拥有搜索源，必须在它的 `Indexers` 页面添加并测试至少一个 indexer。

## 4. 初始化 qBittorrent

打开：

```text
http://127.0.0.1:8081
```

LinuxServer qBittorrent 镜像首次启动时通常会生成临时 WebUI 密码。查看日志：

```sh
docker compose -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  logs qbittorrent | grep -i password
```

使用 `admin` 和临时密码登录后，建议在 WebUI 中修改为固定密码，并把同一个密码写入：

```env
QBITTORRENT_USER=admin
QBITTORRENT_PASSWORD=这里填写固定密码
```

然后重启 MediaDock：

```sh
docker compose -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  up -d --force-recreate --no-deps media-dock
```

这个 Compose 将宿主机 `8081` 映射到 qBittorrent 容器内的 WebUI `8080`。启动时挂载的 `deploy/qbittorrent-init/10-host-header.sh` 会设置 `WebUI\\HostHeaderValidation=false`，避免 qBittorrent 因端口映射后的 Host Header 拒绝浏览器请求；由于管理端口只绑定到 `127.0.0.1`，不要把这个配置当作公网暴露方案。

确认 qBittorrent 的默认保存目录位于容器内的 `/downloads`。Compose 已把它映射到项目的：

```text
./data/downloads
```

这是 API-only 模式：MediaDock 不读取下载文件，也不需要挂载 `/downloads`。

## 5. 检查 PanSou

PanSou API 在 Compose 内部的地址是：

```text
http://pansou:8888
```

宿主机检查：

```sh
curl http://127.0.0.1:8888/api/health
```

MediaDock 使用：

```env
PANSOU_BASE_URL=http://pansou:8888
```

当前完整 Compose 的 `AUTH_ENABLED=false` 是有意设置的兼容选项：MediaDock 直接调用 PanSou `/api/search`，尚未执行 PanSou JWT 登录。请保持 PanSou 端口只绑定到本机，除非先为适配器补上认证支持。

PanSou 的网盘候选可以搜索和展示，但 qBittorrent 不会获取 `cloud_share` 类型候选。配置独立的 OpenList fork 并将 `openlist` 加入 `DOWNLOADERS` 后，`media_acquire` 会调用 OpenList 的 `/api/fs/transfer`，而已确认的 OpenList 文件可以通过 `media_copy` / `POST /api/v1/copies` 调用 `/api/fs/copy`。请求中的 `target_dir`、`source_path` 和覆盖策略必须由用户确认；OpenList 负责权限和实际数据流。MediaDock WebUI 会在每条候选的“原始搜索结果”区域显示原始分享链接和分享密码，供已认证的人工用户复制或打开；MCP 和普通搜索响应仍保持脱敏。完整协议见 [`../docs/openlist-mediadock-protocol.md`](../docs/openlist-mediadock-protocol.md)。

## 6. 使用 MediaDock WebUI

打开：

```text
http://127.0.0.1:8080/
```

本地开发配置中 `AUTH_DISABLED=true`，可以直接使用。生产或局域网共享前设置 `AUTH_DISABLED=false`，然后读取 token：

```sh
cat data/config/auth-token
```

在 WebUI 右上角输入这个 token。

WebUI 提供：

- 能力和组件概览；
- 搜索候选；
- 明确确认后提交获取；
- 查看和取消 MediaDock 自己创建的任务；
- MCP 接入地址说明。

## 7. 命令行验证

开发模式下可以直接搜索：

```sh
curl -X POST http://127.0.0.1:8080/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"Ubuntu","media_type":"movie","limit":10}'
```

查看能力：

```sh
curl http://127.0.0.1:8080/api/v1/capabilities
```

查看任务：

```sh
curl http://127.0.0.1:8080/api/v1/jobs
```

如果关闭了开发模式，需要增加：

```sh
-H "Authorization: Bearer $(cat data/config/auth-token)"
```

## 8. 停止、更新和清理

停止但保留数据：

```sh
docker compose -f deploy/docker-compose.full.yml --env-file deploy/mediadock.env down
```

更新镜像并重新启动：

```sh
docker compose -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  pull pansou prowlarr qbittorrent

docker compose -f deploy/docker-compose.full.yml \
  --env-file deploy/mediadock.env \
  up -d --build
```

数据目录包括：

```text
data/config/       MediaDock SQLite 和 token
data/pansou-cache/ PanSou 缓存
data/prowlarr/     Prowlarr 配置和索引器设置
data/qbittorrent/  qBittorrent 配置
data/downloads/    qBittorrent 下载内容
```

不要在没有备份的情况下删除 `data/`。Compose 中使用了 `latest` 标签，生产部署建议在验证后固定镜像版本或 digest。

## 9. 服务器完整部署：MediaDock + PanSou + Prowlarr + qBittorrent + OpenList Fork

`docker-compose.server.yml` 部署完整媒体链路：PanSou 和 Prowlarr 提供搜索；MediaDock 负责候选、任务及状态；qBittorrent 获取磁力链接；Reso1mi/OpenList Fork 负责分享转存及 OpenList 文件 COPY。Prowlarr 不需要配置 qBittorrent 下载客户端：MediaDock 会直接向 qBittorrent 提交磁力链接。

OpenList Fork 固定在 `05408c0d6d1f4419d3dc895e738613e939702ad3`，前端固定为 `v4.2.6`；Prowlarr 镜像版本为 `2.6.5.5623-ls162`，qBittorrent 为 `5.2.3_v2.0.14-ls477`，二者均固定镜像 digest。Web 管理端口均绑定服务器回环地址，PanSou 不映射到宿主机。qBittorrent peer 端口默认映射到所有网卡，以便接受 BT 入站连接。

| 服务              | SSH 隧道后的地址                    | 用途                            |
| ----------------- | ----------------------------------- | ------------------------------- |
| MediaDock         | `http://127.0.0.1:8080`             | WebUI、REST、MCP                |
| OpenList          | `http://127.0.0.1:5244`             | 网盘管理及 OpenList API         |
| Prowlarr          | `http://127.0.0.1:9696`             | 索引器管理与搜索                |
| qBittorrent       | `http://127.0.0.1:8081`             | 磁力下载管理                    |
| PanSou            | 仅 Docker 内部 `http://pansou:8888` | 网盘搜索 API                    |
| qBittorrent peers | 服务器 `6881/tcp+udp`               | BT peer 流量，不是 Web 管理端口 |

### 首次部署

以下命令在服务器项目仓库根目录执行。若 `deploy/mediadock.server.env` 已存在，**不要覆盖它**；将示例中的新增键合并进去并保留现有 API token。`cp -n` 在目标文件已存在时不会替换它。

```sh
# Only on a fresh deployment; keep the checkout pinned to the tested Fork commit.
git clone https://github.com/Reso1mi/OpenList.git data/server/openlist-src
git -C data/server/openlist-src checkout --detach 05408c0d6d1f4419d3dc895e738613e939702ad3
cp deploy/Dockerfile.openlist-integration data/server/openlist-src/Dockerfile.openlist-integration

mkdir -p data/server/openlist data/server/openlist-downloads \
  data/server/pansou-cache data/server/mediadock data/server/prowlarr \
  data/server/qbittorrent data/server/downloads
chown -R 1000:1000 data/server/openlist data/server/openlist-downloads \
  data/server/pansou-cache data/server/mediadock data/server/prowlarr \
  data/server/qbittorrent data/server/downloads
cp -n deploy/mediadock.server.env.example deploy/mediadock.server.env
chmod 600 deploy/mediadock.server.env
```

编辑 `deploy/mediadock.server.env`，确认 PUID/PGID、端口和时区；首次配置时保留空的 `PROWLARR_API_KEY`、`QBITTORRENT_PASSWORD` 和 `OPENLIST_AUTH_TOKEN`，待各服务初始化后再填入真实值。然后验证并启动：

```sh
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml config --quiet

# Use a Go module proxy reachable from the server if proxy.golang.org is blocked.
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml pull pansou prowlarr qbittorrent
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml build \
  --build-arg GOPROXY=https://goproxy.cn,direct openlist
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml build \
  --build-arg GO_BUILD_IMAGE=golang:1.27.1 \
  --build-arg GOPROXY=https://goproxy.cn,direct media-dock
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml up -d
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml ps
```

MediaDock and OpenList runtime images use `scratch`; CA certificates, timezone data, and non-root identities are copied from their Go builders. The OpenList build fetches the matching `OpenList-Frontend` release into `public/dist` before compiling, because the backend embeds those files. `--log-std` keeps OpenList in the foreground. The `GOPROXY` build argument affects only Go module downloads and can be changed to a proxy reachable from the server; `GO_BUILD_IMAGE` can select a compatible cached Go builder.

### 安全访问与初始化

另开一个本地终端建立 SSH 隧道：

```sh
ssh -N \
  -L 5244:127.0.0.1:5244 \
  -L 8080:127.0.0.1:8080 \
  -L 9696:127.0.0.1:9696 \
  -L 8081:127.0.0.1:8081 \
  root@<server-ip>
```

1. **qBittorrent**：打开 `http://127.0.0.1:8081`。LinuxServer 镜像首次启动会生成临时 WebUI 密码；在服务器本地运行下面的日志命令获取它，**不要把日志或密码贴到聊天中**。登录后设置专用密码，再把用户名和密码写入 `deploy/mediadock.server.env` 的 `QBITTORRENT_USER` / `QBITTORRENT_PASSWORD`。下载目录应使用容器路径，例如 `/downloads/Movies`；它对应宿主机 `data/server/downloads/Movies`。

   ```sh
   docker compose --env-file deploy/mediadock.server.env \
     -f deploy/docker-compose.server.yml logs qbittorrent
   ```

2. **Prowlarr**：打开 `http://127.0.0.1:9696`，完成初始化，在 `Settings -> General` 获取 API Key，并至少添加、测试一个 indexer。把 key 写入 `PROWLARR_API_KEY`。Prowlarr 只负责搜索，MediaDock 直接调用 qBittorrent，因此无需在 Prowlarr 中配置下载客户端。
3. **OpenList**：打开 `http://127.0.0.1:5244`，完成初始化并配置网盘存储。创建权限受限的 API token，将原始 `Authorization` 值（不加 `Bearer ` 前缀）写入 `OPENLIST_AUTH_TOKEN`。分享转存使用 `media_acquire`；已确认文件通过 `media_copy` 调用 Fork 的 `/api/fs/copy`。
4. **MediaDock**：设置好上述凭据后，在服务器重新创建 MediaDock，让它读取新配置：

```sh
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml \
  up -d --force-recreate --no-deps media-dock
```

MediaDock 首次启动会在 `data/server/mediadock/auth-token` 生成访问 token；只在自己的终端读取，不要将 token 或 OpenList/qBittorrent/Prowlarr 凭据提交到 Git 或贴入聊天。初次验证可执行 `curl http://127.0.0.1:8080/healthz`。Prowlarr/qBittorrent/OpenList WebUI 和 API 管理端口不要直接暴露公网。BT `6881/tcp+udp` 是唯一默认映射到所有网卡的端口；若要接受入站 peer，需在云防火墙/主机防火墙放行；不需要入站连接时可移除 Compose 中两条 peer 端口映射。

### 更新与停止

```sh
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml pull pansou prowlarr qbittorrent
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml up -d
```

停止服务但保留绑定挂载的数据：

```sh
docker compose --env-file deploy/mediadock.server.env \
  -f deploy/docker-compose.server.yml down
```

持久数据位于 `data/server/`：OpenList、Prowlarr、qBittorrent 配置，MediaDock SQLite/token、PanSou 缓存和 qBittorrent 下载文件。清理或迁移前先备份该目录。

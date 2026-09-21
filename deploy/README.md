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

| 服务                | 容器内地址                | 宿主机地址              | 用途                    |
| ------------------- | ------------------------- | ----------------------- | ----------------------- |
| MediaDock           | `http://media-dock:8080`  | `http://127.0.0.1:8080` | WebUI、REST、MCP        |
| PanSou              | `http://pansou:8888`      | `http://127.0.0.1:8888` | 网盘资源搜索 API        |
| Prowlarr            | `http://prowlarr:9696`    | `http://127.0.0.1:9696` | 索引器管理和搜索        |
| qBittorrent         | `http://qbittorrent:8080` | `http://127.0.0.1:8081` | WebUI 和下载 API        |
| qBittorrent torrent | `6881/tcp+udp`            | `6881/tcp+udp`          | DHT/peer 流量，可选映射 |

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

PanSou 的网盘候选目前可以搜索和展示，但 qBittorrent 不会获取 `cloud_share` 类型候选。要获取这类网盘资源，需要未来单独接入网盘转存/下载适配器。

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

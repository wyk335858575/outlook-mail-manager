# 宝塔 Docker 镜像部署与运维

本项目使用公开的 GitHub Container Registry（GHCR）多架构镜像。宝塔负责 Docker、域名、HTTPS 和 Nginx 反向代理；应用端口只绑定服务器回环地址，不直接暴露到公网。生产运行时不包含 Node.js，也不需要上传 Windows `.exe`。

当前正式镜像：

```text
ghcr.io/wyk335858575/outlook-mail-manager:1.0.3
```

公开镜像无需 GitHub 账号或 token。服务器建议至少 2 核 CPU、2 GiB 内存和 10 GiB 可用磁盘。

## 1. 安装并检查 Docker

1. 登录宝塔面板，打开左侧“Docker”。
2. 尚未安装时，按面板提示安装 Docker 和 Docker Compose。
3. 打开宝塔“终端”，确认命令可用：

```bash
docker version
docker compose version
```

## 2. 准备项目目录

推荐从公开仓库取得 Compose、安装脚本和文档：

```bash
cd /www/wwwroot
git clone https://github.com/wyk335858575/outlook-mail-manager.git
cd outlook-mail-manager
cp .env.example .env
chmod 600 .env
```

目录已经存在时不要重复克隆，直接进入 `/www/wwwroot/outlook-mail-manager`。升级时保留服务器原有 `.env` 和 Docker 数据卷。

在宝塔“文件”中编辑 `/www/wwwroot/outlook-mail-manager/.env`：

```dotenv
APP_BASE_URL=https://mail.example.com
APP_LISTEN_ADDR=:8080
APP_DATA_DIR=/data
APP_TIMEZONE=Asia/Shanghai
APP_LOG_LEVEL=info
APP_VERSION=1.0.3
APP_UPDATE_REPOSITORY=wyk335858575/outlook-mail-manager
APP_IMAGE=ghcr.io/wyk335858575/outlook-mail-manager:1.0.3
# 可选：首次启动默认值，也可登录后在“设置”中保存
MS_CLIENT_ID=
```

必须把 `APP_BASE_URL` 改为实际 HTTPS 域名。不要把管理员密码、邮箱密码、TOTP secret、OAuth token、通知 secret 或 API token 写入 `.env`。

`MS_CLIENT_ID` 可以留空。首次登录后可在“设置” > “Microsoft OAuth”中保存 Client ID，无需重启；数据库设置优先于环境变量。注册步骤见 [注册并配置 Microsoft Client ID](microsoft-client-id.md)。

## 3. 使用终端拉取镜像（推荐）

直接拉取固定版本，不使用 `latest`：

```bash
docker pull ghcr.io/wyk335858575/outlook-mail-manager:1.0.3
```

核对镜像来源和 digest：

```bash
docker image inspect \
  ghcr.io/wyk335858575/outlook-mail-manager:1.0.3 \
  --format '{{index .RepoDigests 0}}'
```

输出应包含 `ghcr.io/wyk335858575/outlook-mail-manager@sha256:...`。将完整值与 [v1.0.3 Release](https://github.com/wyk335858575/outlook-mail-manager/releases/tag/v1.0.3) 中签名的 `release-manifest.json` 比对。

检查 Compose 最终配置并使用已拉取镜像启动：

```bash
cd /www/wwwroot/outlook-mail-manager
docker compose config
docker compose up -d --no-build app
docker compose ps
docker compose logs --tail=100 app
```

`--no-build` 明确禁止宝塔服务器本地构建，确保使用 GHCR 正式镜像。Compose 会把应用绑定到 `127.0.0.1:8080`，设置 768 MiB 内存上限、只读根文件系统、非 root 用户、健康检查和日志轮转。

## 4. 使用宝塔界面拉取镜像

宝塔版本不同，按钮可能显示为“拉取镜像”或“线上镜像”，操作含义相同：

1. 打开“Docker” > “镜像”。
2. 点击“拉取镜像”或“线上镜像”。
3. 镜像地址填写 `ghcr.io/wyk335858575/outlook-mail-manager:1.0.3`，不要添加 `https://`。
4. 仓库用户名和密码留空；当前 GHCR 包是公开镜像。
5. 等待下载完成，在本地镜像列表确认名称和 `1.0.3` 标签。
6. 打开“Docker” > “容器编排”，添加编排项目。
7. 项目目录选择 `/www/wwwroot/outlook-mail-manager`，使用其中的 `docker-compose.yml`。
8. 确认同目录已存在 `.env`，然后创建或启动编排。

若面板拉取成功后没有立即显示镜像，先刷新镜像列表；仍不显示时使用上一节的终端命令拉取和检查。

## 5. 健康检查

```bash
curl --fail http://127.0.0.1:8080/healthz
docker compose ps
docker compose logs --tail=200 app
```

容器应显示 `healthy`。`/healthz` 只返回服务和数据库是否可用，不泄露账号数据；账号、队列、磁盘、通知和备份详情需要管理员登录。

首次安装在管理员尚未创建时，应先通过宝塔防火墙或站点访问限制只允许管理员 IP。完成管理员账号、密码和身份验证器设置后再开放站点。

## 6. 宝塔 HTTPS 反向代理

1. 在宝塔“网站”中新建站点并绑定域名。
2. 申请 SSL 证书并开启强制 HTTPS。
3. 添加反向代理，目标 URL 设置为 `http://127.0.0.1:8080`。
4. 不启用 WebSocket；本项目不使用它。
5. 不要在云安全组或宝塔防火墙中开放公网 `8080`。
6. 在站点 Nginx 配置中加入：

```nginx
client_max_body_size 2m;
proxy_connect_timeout 10s;
proxy_send_timeout 60s;
proxy_read_timeout 60s;
proxy_request_buffering on;
proxy_buffering off;

add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
add_header X-Content-Type-Options "nosniff" always;
add_header Referrer-Policy "no-referrer" always;
```

附件下载由应用流式转发，`proxy_buffering off` 避免 Nginx 将完整附件先缓冲到磁盘。

## 7. 常见拉取问题

### `unauthorized` 或 `denied`

当前镜像公开，不需要登录。先确认地址和标签完全正确，再清除可能失效的旧凭据：

```bash
docker logout ghcr.io
docker pull ghcr.io/wyk335858575/outlook-mail-manager:1.0.3
```

### `i/o timeout`、`TLS handshake timeout` 或 `context deadline exceeded`

这通常是服务器到 GHCR 的 DNS、出口网络或代理问题：

```bash
curl -I https://ghcr.io/v2/
getent hosts ghcr.io
```

注册表返回 `401 Unauthorized` 说明网络已连通，Docker 随后会通过匿名令牌完成公共镜像拉取。若无法连接，检查服务器 DNS、出口 TCP 443、防火墙和可信代理配置；不要使用来源不明的第三方镜像。

### 端口 `8080` 已占用

```bash
ss -lntp | grep ':8080'
```

停止冲突服务，或同时修改 `docker-compose.yml` 的宿主机端口和宝塔反向代理目标端口。

### 容器反复重启或 `unhealthy`

```bash
docker compose ps
docker compose logs --tail=300 app
docker inspect outlook-mail-manager-app-1 --format '{{json .State.Health}}'
```

重点检查 `.env` 是否存在、`APP_BASE_URL` 是否为实际 HTTPS 地址、数据卷和磁盘是否可写，以及服务器内存是否充足。容器名可能因 Compose 项目名不同而变化，以 `docker compose ps` 为准。

## 8. 备份与恢复

在线备份：

```bash
docker compose run --rm app backup
```

备份保存在 Docker 数据卷的 `/data/backups`。数据库包含加密后的 OAuth token、TOTP secret 和通知凭据，复制到异机或对象存储时必须限制访问权限。

离线恢复：

```bash
docker compose stop app
docker compose run --rm app restore /data/backups/outlook-manager-YYYYMMDDTHHMMSSZ.db
docker compose up -d --no-build app
```

恢复命令先执行 SQLite 完整性、业务单例检查和 WAL checkpoint，并保留恢复前数据库安全快照。启动后登录管理台检查“健康与备份”、至少一个 Microsoft 账号和最近同步时间。

## 9. 后续手动更新

更新前先创建一致性备份。以 `1.0.3` 为例：

```bash
cd /www/wwwroot/outlook-mail-manager
docker compose run --rm app backup
docker pull ghcr.io/wyk335858575/outlook-mail-manager:1.0.3
```

然后把 `.env` 中的 `APP_VERSION` 和 `APP_IMAGE` 改为：

```dotenv
APP_VERSION=1.0.3
APP_IMAGE=ghcr.io/wyk335858575/outlook-mail-manager:1.0.3
```

启动并检查：

```bash
docker compose up -d --no-build app
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
docker compose logs --tail=200 app
```

不要执行 `docker compose down -v`，否则会删除管理员、OAuth token、邮件索引和规则所在的数据卷。不要在生产环境使用 `latest`。

健康页可以检测 GitHub Release。出现新版本后，在宝塔 root 终端执行一条单次升级命令即可；不需要安装 systemd 服务，也不会把 Docker Socket 暴露给 Web 容器。脚本会验证当前 Release 标签对应的 Cosign/GitHub Actions OIDC 身份、签名 manifest 和固定镜像 digest，失败时自动回滚。完整步骤见 [单次升级与回滚](online-update.md)。

## 10. 官方参考

- [宝塔 Docker 官方文档](https://docs.bt.cn/category/docker)
- [Docker Compose pull](https://docs.docker.com/reference/cli/docker/compose/pull/)
- [Docker Compose up](https://docs.docker.com/reference/cli/docker/compose/up/)
- [GitHub Container Registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)

## 11. 日常检查

- 每周确认数据库完整性、备份时间和备份文件大小。
- 处理 `reauth_required` 账号，不要反复尝试失效授权。
- 磁盘达到 70% 时扩容或清理旧备份；90% 时程序进入仅同步元数据模式。
- 定期测试至少一个系统通知通道和一个受限 API token。

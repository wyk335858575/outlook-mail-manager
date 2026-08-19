# 宝塔面板部署与运维

本项目使用单个应用容器，端口只绑定到服务器回环地址，由宝塔 Nginx 提供公网 HTTPS。生产运行时不包含 Node.js。

## 1. 准备目录与配置

在服务器创建项目目录，把源代码放入目录后创建 `.env`：

```dotenv
APP_BASE_URL=https://mail.example.com
APP_LISTEN_ADDR=:8080
APP_DATA_DIR=/data
APP_TIMEZONE=Asia/Shanghai
APP_LOG_LEVEL=info
APP_VERSION=1.0.0
APP_UPDATE_REPOSITORY=wyk335858575/outlook-mail-manager
APP_IMAGE=ghcr.io/wyk335858575/outlook-mail-manager:1.0.0
# 可选：首次启动默认值，也可登录后在“设置”中保存
MS_CLIENT_ID=
```

`MS_CLIENT_ID` 是可选的公开应用标识。也可以在首次登录后打开“设置” > “Microsoft OAuth”保存 Client ID，保存后无需重启，数据库设置优先于环境变量。注册步骤见 [注册并配置 Microsoft Client ID](microsoft-client-id.md)。不要把管理员密码、TOTP secret、OAuth token、通知 secret 或 API token 写入 `.env`。

首次安装在管理员尚未创建时，应先在宝塔防火墙或站点访问限制中仅允许管理员 IP。完成管理员账号、密码和身份验证器设置后再开放站点。

## 2. 启动容器

```bash
docker compose build --pull
docker compose up -d
docker compose ps
docker compose logs --tail=100 app
```

Compose 将应用绑定到 `127.0.0.1:8080`，设置 768 MiB 内存上限、只读根文件系统、非 root 用户、健康检查和日志轮转。

## 3. 宝塔 HTTPS 反向代理

1. 在宝塔创建站点并绑定域名。
2. 申请并强制启用 HTTPS 证书。
3. 添加反向代理，目标 URL 设置为 `http://127.0.0.1:8080`。
4. 不启用 WebSocket；本项目不使用它。
5. 在站点 Nginx 配置中加入以下代理和安全参数：

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

应用附件下载直接流式转发，`proxy_buffering off` 避免 Nginx 将完整附件先缓冲到磁盘。

## 4. 健康检查

```bash
curl --fail http://127.0.0.1:8080/healthz
docker compose ps
docker compose logs --tail=200 app
```

`/healthz` 仅返回服务和数据库是否可用。账号、队列、磁盘、通知和备份详情需要管理员登录，或使用带 `system:read` 的受限 API token。

## 5. 备份与恢复

在线备份：

```bash
docker compose run --rm app backup
```

备份保存在 Docker 数据卷的 `/data/backups`。将备份复制到异机或对象存储时，应限制访问权限；数据库包含加密后的 OAuth token、TOTP secret 和通知凭据。

离线恢复：

```bash
docker compose stop app
docker compose run --rm app restore /data/backups/outlook-manager-YYYYMMDDTHHMMSSZ.db
docker compose up -d app
```

恢复命令先执行 SQLite 完整性检查，并保留恢复前数据库。启动后登录管理台，检查“健康与备份”、至少一个 Microsoft 账号和最近同步时间。

## 6. 升级与回滚

升级前先创建备份。将新版**源代码**上传并覆盖到 `/www/wwwroot/outlook-mail-manager`；Windows `.exe` 不能在宝塔 Linux 服务器运行，不需要上传。保留服务器原有 `.env`，不要删除 Docker 数据卷，也不要执行 `docker compose down -v`。

当前 `docker-compose.yml` 使用名为 `outlook_data` 的持久化卷，覆盖项目源代码不会删除管理员、OAuth token、邮件索引或规则。然后使用固定版本镜像更新：

```bash
cd /www/wwwroot/outlook-mail-manager
docker compose run --rm app backup
docker compose build --pull --build-arg APP_VERSION=1.0.0
docker compose up -d
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
docker compose logs --tail=200 app
```

数据库迁移前程序还会自动创建一致性备份。若新版本启动失败，停止容器，恢复升级前备份，再用上一个固定版本镜像启动。不要在生产环境使用 `latest` 标签。

1.0.0 起可选安装宿主机在线更新助手。助手不在 Web 容器中运行，也不把 Docker Socket 暴露给应用；它只通过权限受控的 Unix Socket 接受固定更新任务，并执行签名 Release manifest 检查、SQLite 备份、固定 digest 拉取、精确标签的 Cosign/GitHub OIDC 验证、健康检查和失败回滚。Fork 部署前必须替换上面的仓库与镜像。完整安装步骤见 [在线更新助手安装与回滚](online-update.md)。

## 7. 日常检查

- 每周确认数据库完整性、备份时间和备份文件大小。
- 处理 `reauth_required` 账号，不要反复尝试失效授权。
- 磁盘达到 70% 时扩容或清理旧备份；90% 时程序进入仅同步元数据模式。
- 定期测试至少一个系统通知通道和一个受限 API token。

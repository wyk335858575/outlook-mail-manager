# 在线更新助手安装与回滚

在线更新由宿主机 root 服务执行，Web 容器只通过权限受控的 Unix Socket 请求固定流程。不要把 `/var/run/docker.sock` 挂载到应用容器。

## 前提

- Linux 宝塔服务器，已安装 Docker Compose v2。
- 已安装 Cosign，并可通过 `cosign version` 验证。
- 项目位于 `/www/wwwroot/outlook-mail-manager`。
- GitHub 仓库已经发布正式 `v1.0.N` Release，GHCR 镜像必须公开。
- Release 包含签名的 `release-manifest.json`、`SHA256SUMS`、对应架构 updater 二进制及各自 `.bundle`。

## 配置项目

在项目 `.env` 中填写自己的仓库和镜像，不能保留 `owner`：

```dotenv
APP_VERSION=1.0.0
APP_UPDATE_REPOSITORY=wyk335858575/outlook-mail-manager
APP_IMAGE=ghcr.io/wyk335858575/outlook-mail-manager:1.0.0
APP_UPDATE_SOCKET=/run/outlook-mail-manager-updater/updater.sock
```

从同一个 Release 下载以下四个文件到项目根目录：

- `outlook-mail-manager-updater-linux-amd64` 或 `outlook-mail-manager-updater-linux-arm64`
- 上述二进制对应的 `.bundle`
- `SHA256SUMS`
- `SHA256SUMS.bundle`

安装脚本从项目 `.env` 读取仓库、镜像和版本，使用精确的 `release.yml@refs/tags/v1.0.N` 身份验证二进制和校验和。任何文件缺失、哈希不符或签名来自其他标签时都会在安装 systemd 服务前停止。

```bash
cd /www/wwwroot/outlook-mail-manager
chmod +x deploy/install-updater.sh
sudo ./deploy/install-updater.sh /www/wwwroot/outlook-mail-manager
```

安装脚本创建 `outlook-mail-manager` 系统组，将 updater 安装到 `/usr/local/libexec`，安装 systemd 单元，并把 Unix Socket 组 ID 写入项目 `.env`。它不会修改管理员密码、数据库或 OAuth token。

## 配置更新助手

编辑 `/etc/outlook-mail-manager/updater.env`：

```dotenv
APP_UPDATE_REPOSITORY=wyk335858575/outlook-mail-manager
APP_IMAGE=ghcr.io/wyk335858575/outlook-mail-manager
UPDATER_DEPLOY_DIR=/www/wwwroot/outlook-mail-manager
UPDATER_STATE_DIR=/var/lib/outlook-mail-manager-updater
UPDATER_SOCKET_PATH=/run/outlook-mail-manager-updater/updater.sock
UPDATER_COMPOSE_SERVICE=app
UPDATER_HEALTH_URL=http://127.0.0.1:8080/healthz
UPDATER_COSIGN_OIDC_ISSUER=https://token.actions.githubusercontent.com
```

安装脚本会自动生成该配置。签名身份不能通过环境变量放宽：程序始终根据配置仓库和目标 Release 标签精确匹配 `.github/workflows/release.yml@refs/tags/v1.0.N`。Fork 必须先在项目 `.env` 中改成自己的仓库和 GHCR 镜像，再重新运行安装脚本。

```bash
sudo systemctl daemon-reload
sudo systemctl restart outlook-mail-manager-updater
sudo systemctl status outlook-mail-manager-updater
docker compose up -d app
```

登录管理台打开“健康与备份”。页面应显示当前版本、最新稳定版、检查时间和“一键更新”。按钮不存在时检查仓库配置、Socket 文件和容器的补充组 ID：

```bash
ls -l /run/outlook-mail-manager-updater/updater.sock
grep UPDATER_SOCKET_GID .env
docker compose exec app id
journalctl -u outlook-mail-manager-updater -n 100 --no-pager
```

## 更新流程

1. updater 读取配置仓库的最新非草稿、非预发布 `v1.0.N` Release。
2. 在解析 manifest 前验证其 Cosign bundle 和当前标签的精确 GitHub Actions OIDC 身份。
3. 验证 manifest 的仓库、镜像、tag、版本和 SHA-256 digest，再创建 SQLite 一致性备份。
4. 使用相同的精确标签身份验证 `image@digest`。
5. 拉取固定 digest，原子修改 `.env` 中的 `APP_IMAGE` 与 `APP_VERSION`。
6. `docker compose up -d --no-build app`，持续轮询 `/healthz`。
7. 成功后保留升级前备份；失败则恢复旧 `.env`、旧镜像和升级前数据库。

更新任务存放在 `/var/lib/outlook-mail-manager-updater/jobs`，因此应用容器重启期间健康页可以重新连接并继续显示结果。若宿主机 updater 本身重启，未完成任务会明确标记为已中断，不会永久停留在处理中；开始更新还会取得跨进程文件锁，禁止两个助手同时修改部署。

## 从 GitHub 一键发布

维护者先运行 `node scripts/version.mjs bump` 并填写 CHANGELOG，提交到 `main` 后打开 GitHub 的 Actions 页面，选择 `prepare release`，点击 `Run workflow`。该工作流验证版本连续性和完整测试，创建注释标签后在标签引用上触发 `release` 工作流。后者构建多架构镜像、Cosign 签名、SBOM、updater 和 Release 资产。

第一次发布镜像后，在 GitHub 个人主页的 Packages 中打开 `outlook-mail-manager`，进入 Package settings，将可见性改为 Public。在线更新助手不保存 GitHub token，因此私有 GHCR 包无法用于默认安装方式。

## 手动回滚

自动回滚失败时先停止服务，不要执行 `docker compose down -v`：

```bash
cd /www/wwwroot/outlook-mail-manager
docker compose stop app
# 将 .env 中 APP_IMAGE 和 APP_VERSION 改回上一个已知正常版本
docker compose run --rm app restore /data/backups/outlook-manager-YYYYMMDDTHHMMSSZ.db
docker compose up -d --no-build app
curl --fail http://127.0.0.1:8080/healthz
```

数据库恢复会保留恢复前文件。恢复后登录管理台，检查 schema、管理员登录、至少一个账号授权、delta 同步状态和最近备份。

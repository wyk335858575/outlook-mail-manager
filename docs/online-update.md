# 单次升级与回滚

在线更新采用“单次升级脚本 + GitHub Release”模式。服务器不安装常驻 updater 或 systemd 服务，Web 容器也不挂载 Docker Socket；只有管理员在宝塔 root 终端执行命令时，临时更新器才会运行。

## 前提

- Linux 宝塔服务器已安装 Docker Compose v2。
- 项目目录为 `/www/wwwroot/outlook-mail-manager`，其中存在 `.env`。
- GitHub Release 和 GHCR 镜像均为公开状态。
- `.env` 中的 `APP_UPDATE_REPOSITORY`、`APP_IMAGE` 指向同一个项目；官方部署可直接使用示例默认值。

## 一键升级

建议先登录管理台，在“健康与备份”确认数据库状态正常。随后打开宝塔“终端”，以 root 身份执行：

```bash
curl -fsSL https://github.com/wyk335858575/outlook-mail-manager/releases/latest/download/update.sh | bash
```

项目不在默认目录时使用：

```bash
curl -fsSL https://github.com/wyk335858575/outlook-mail-manager/releases/latest/download/update.sh \
  | DEPLOY_DIR=/你的项目目录 bash
```

脚本不会安装持久化程序。Cosign、updater 和验证文件全部放在权限受限的临时目录，命令结束后自动删除。

## 安全流程

1. 从 GitHub 最新正式 Release 读取严格的 `v1.0.N` 标签。
2. 根据 amd64 或 arm64 架构下载临时 Cosign，并按脚本内固定 SHA-256 校验。
3. 下载 `SHA256SUMS`、release manifest、对应架构 updater 及其 Cosign bundle。
4. 将所有签名严格绑定到当前仓库的 `release.yml@refs/tags/v1.0.N` GitHub Actions OIDC 身份。
5. 校验 manifest 中的仓库、镜像、版本、标签和固定镜像 digest。
6. 通过应用内置备份命令创建 SQLite 一致性备份。
7. 验证并拉取 `image@sha256:...`，原子更新 `.env` 后重启应用。
8. 轮询 `/healthz`；失败时恢复旧配置、旧镜像和升级前数据库。

脚本使用部署目录中的跨进程文件锁，重复执行时不会同时启动两个升级任务。它不读取或输出管理员密码、邮箱 OAuth token、API token 或数据库内容。

## 查看结果

```bash
cd /www/wwwroot/outlook-mail-manager
grep -E '^(APP_VERSION|APP_UPDATE_REPOSITORY|APP_IMAGE)=' .env
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
```

健康页应显示新的当前版本。升级前备份仍保存在 Docker 数据卷的 `/data/backups` 中。

## 常见错误

- `找不到 .env`：确认项目是否位于默认目录，或通过 `DEPLOY_DIR` 指定实际目录。
- `需要 Docker Compose v2`：先在宝塔 Docker 管理器中升级 Compose 插件。
- `签名验证失败`：不要绕过验证；确认使用官方 Release，Fork 则必须发布自己的签名 Release 并修改 `.env`。
- `GitHub 返回 404`：确认仓库、Release 和 GHCR Package 均为 Public。
- `当前已是最新稳定版`：无需操作，命令会安全退出。

## 手动回滚

自动回滚失败时先停止应用，不要执行 `docker compose down -v`：

```bash
cd /www/wwwroot/outlook-mail-manager
docker compose stop app
# 把 .env 中 APP_IMAGE 和 APP_VERSION 改回上一个正常版本
docker compose run --rm app restore /data/backups/outlook-manager-YYYYMMDDTHHMMSSZ.db
docker compose up -d --no-build app
curl --fail http://127.0.0.1:8080/healthz
```

恢复后登录管理台，检查管理员登录、数据库 schema、至少一个账号授权和最近同步状态。

## Fork 发布

Fork 用户需要在 `.env` 中填写自己的仓库和 GHCR 镜像。`release.yml` 会把 `update.sh`、两个架构的 updater、manifest、SBOM、`SHA256SUMS` 和对应 Cosign bundle 一起发布。健康页生成的命令会自动使用配置仓库。

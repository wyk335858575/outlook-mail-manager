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

1. 从 GitHub 最新正式 Release 读取受支持的 `vMAJOR.MINOR.PATCH` 标签；次版本和补丁版本必须为单个 `0` 到 `9` 的数字。
2. 根据 amd64 或 arm64 架构下载临时 Cosign，并按脚本内固定 SHA-256 校验。
3. 下载 `SHA256SUMS`、release manifest、对应架构 updater 及其 Cosign bundle。
4. 将所有签名严格绑定到当前仓库的 `release.yml@refs/tags/vMAJOR.MINOR.PATCH` GitHub Actions OIDC 身份。
5. 校验 manifest 中的仓库、镜像、版本、标签和固定镜像 digest。
6. 验证并拉取 `image@sha256:...`，原子更新 `.env` 后停止旧应用。
7. 由已验证的新镜像离线执行 WAL checkpoint、SQLite 完整性检查、业务单例检查和逐表行数核对，再保留一致性备份。
8. 轮询 `/healthz`；失败时恢复旧配置、旧镜像和升级前数据库。

新备份不会调用旧容器中的备份代码，因此从早期版本升级时也不会依赖旧实现。若新服务未通过健康检查，终端会先显示容器日志和 Compose 状态，再由已验证的新镜像恢复数据库，最后切回旧镜像和旧配置。

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
- `expected one application settings row`：当前数据库虽能通过 SQLite 结构检查，但业务数据不完整。不要继续更新，按下一节辨认并恢复升级前安全快照。

## 升级后账号或设置消失的恢复

若健康页显示账号为 0、设置页显示“无法读取设置”，或者备份文件明显小于升级前数据库，请停止写入并先辨认候选文件。不要创建管理员、重新导入邮箱，也不要执行 `docker compose down -v`。

下面的命令只读挂载生产数据卷，只输出完整性、schema 和记录数量，不输出管理员、邮箱或 token 内容：

```bash
cd /www/wwwroot/outlook-mail-manager
CID=$(docker compose --env-file .env ps -q app)
DATA_VOLUME=$(docker inspect "$CID" --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')

docker run --rm --user 0 --entrypoint sh \
  -v "$DATA_VOLUME:/data:ro" \
  ghcr.io/wyk335858575/outlook-mail-manager:1.1.0 -c '
    apk add --no-cache sqlite >/dev/null
    for db in /data/outlook-manager.db /data/outlook-manager.db.before-restore-* /data/backups/*.db; do
      [ -f "$db" ] || continue
      echo "=== $db ==="
      ls -ln "$db"
      sqlite3 -readonly "$db" "
        PRAGMA quick_check;
        SELECT COALESCE(MAX(version),0) FROM schema_migrations;
        SELECT COUNT(*) FROM admins;
        SELECT COUNT(*) FROM app_settings;
        SELECT COUNT(*) FROM accounts;
        SELECT COUNT(*) FROM account_tokens;
        SELECT COUNT(*) FROM messages;
      " 2>&1 || true
      echo "顺序：quick_check / schema / admins / settings / accounts / tokens / messages"
    done
  '
```

候选文件必须显示 `ok`，settings 对应的计数必须为 `1`，并且账号、token 和邮件数量符合升级前实际情况。文件大不等于一定正确，不要只按大小选择。确定文件后，使用当前正式版镜像的 `restore` 命令恢复；恢复命令会再次执行完整性和业务数据检查，并保留当前数据库安全快照。

## 手动回滚

自动回滚失败时先停止应用，不要执行 `docker compose down -v`：

```bash
cd /www/wwwroot/outlook-mail-manager
docker compose stop app
# 把 .env 中 APP_IMAGE 和 APP_VERSION 改回上一个正常版本
docker compose run --rm --no-deps app restore /data/backups/outlook-manager-YYYYMMDDTHHMMSSZ.db
docker compose up -d --no-build --force-recreate app
curl --fail http://127.0.0.1:8080/healthz
```

恢复后登录管理台，检查管理员登录、数据库 schema、至少一个账号授权和最近同步状态。

## Fork 发布

Fork 用户需要在 `.env` 中填写自己的仓库和 GHCR 镜像。`release.yml` 会把 `update.sh`、两个架构的 updater、manifest、SBOM、`SHA256SUMS` 和对应 Cosign bundle 一起发布。健康页生成的命令会自动使用配置仓库。

# Outlook / Hotmail 邮箱管理台

[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

自托管的 Outlook/Hotmail 多账号收件管理器，提供安全 OAuth、增量同步、验证码提取、本地规则、通知和可恢复清理。项目面向单管理员和约 1000 个个人 Microsoft 邮箱，适合部署在 2 核 2 GiB 的 Linux VPS 或宝塔面板。

面向单管理员的 Outlook/Hotmail 收件管理项目。当前支持个人 Microsoft 账号的设备码 OAuth、加密 token 自动刷新、增量收件同步、SQLite FTS5 搜索、本地分类、两阶段安全清理、Telegram/PushPlus/HMAC Webhook 通知、只读外部 API、附件流式下载、磁盘保护和一致性备份。

项目永不收集邮箱密码，不申请 `Mail.Send`，不提供发信或永久删除功能。

## 管理员安全机制

- 首次打开管理台时直接创建唯一管理员，设置账号、至少 12 个字符的密码，并绑定 TOTP 身份验证器。
- 登录必须同时提供管理员账号、密码和身份验证器中的六位验证码。
- 不使用初始化凭据、恢复码、`APP_MASTER_KEY` 或磁盘 `master.key` 文件。
- 程序生成随机数据密钥，使用管理员密码经 Argon2id 派生的密钥加密后存入 SQLite；TOTP secret 和 OAuth token 再由数据密钥加密。
- 服务重启后数据密钥保持锁定。管理员首次登录成功后只在进程内存中解锁，并恢复后台邮件同步。
- 忘记管理员密码或丢失身份验证器后无法恢复原密钥，只能重置管理员并重新授权 Microsoft 邮箱。

由于首次设置页面没有额外初始化口令，空实例可能被第一个访问者创建管理员。远程安装时应先用防火墙或宝塔访问限制仅允许管理员 IP，完成管理员设置后再开放站点。

## 本地运行

需要 Go 1.26、Node.js 24 和 npm。

1. 安装并构建前端：

   ```powershell
   npm --prefix web install
   npm --prefix web run build
   ```

2. 设置运行参数：

   ```powershell
   $env:APP_DATA_DIR = ".\data\runtime"
   $env:APP_BASE_URL = "http://localhost:8080"
   $env:APP_LISTEN_ADDR = "127.0.0.1:8080"
   # 可选：首次启动时用环境变量预填，也可以登录后在“设置”中填写
   $env:MS_CLIENT_ID = "你的 Microsoft Application (client) ID"
   ```

3. 启动并访问 `http://localhost:8080`：

   ```powershell
   go run ./cmd/server
   ```

直接运行仓库根目录中的 Windows EXE 时，未设置 `APP_DATA_DIR` 也会默认使用 `data\runtime`，避免误连到新的空数据库。

首次打开后依次设置管理员账号、密码和身份验证器，不需要准备任何初始化密钥或恢复码。

## Microsoft 应用配置

1. 在 Microsoft Entra 管理中心创建应用注册，支持的账号类型推荐选择“仅个人 Microsoft 账户”。
2. 在“身份验证”中允许公共客户端流。设备码流程不需要也不使用 client secret。
3. 在 Microsoft Graph 委托权限中配置 `User.Read` 和 `Mail.ReadWrite`。应用还会请求 `openid profile offline_access`，不要添加 `Mail.Send`。
4. 登录管理台，在“设置” > “Microsoft OAuth”中粘贴“应用程序（客户端）ID”并保存，无需重启。
5. 登录管理台，在“账号管理”导入纯邮箱地址或 `email,group,tags,notes` CSV，再逐个完成设备码授权。

完整的注册步骤、权限说明、Client ID 更换规则和错误排查见 [注册并配置 Microsoft Client ID](docs/microsoft-client-id.md)。`MS_CLIENT_ID` 仍可作为首次启动的可选默认值；设置页保存后的数据库值优先。

基础账号导入不得包含邮箱密码。为兼容现有数据，OAuth 凭据模式识别 `邮箱----密码----client_id----refresh_token`、Tab、逗号和分号格式；密码只在浏览器内用于判断字段位置，解析后立即丢弃，不会进入 HTTP 请求、日志、数据库、审计或导入结果。服务端只接收邮箱、Client ID 和 refresh token。

OAuth 导入最多 1000 行、4 个并发验证。服务端使用 refresh token 换取新 token，检查必要权限并调用 `/me` 校验稳定 Microsoft 用户 ID。主邮箱与导入邮箱不一致时拒绝保存，要求改用设备码确认别名。已有账号默认跳过，只有明确勾选“覆盖已有授权”才会替换；任务完成后清除暂存的 refresh token 密文。

账号列表可按邮箱、显示名称、分组、标签和备注搜索，使用服务端分页及最多 1000 个匹配账号的跨页全选，并支持批量启用、停用和删除本地账号数据。账号资料可编辑导入邮箱、分组、标签和备注；Microsoft 主邮箱、显示名称和稳定用户 ID 只读。“替换 OAuth 凭据”会先验证新 Client ID 与 refresh token，成功后原子替换，失败不会清空当前有效 token。

完整格式、验证步骤、覆盖规则和错误排查见 [OAuth 凭据导入与账号编辑](docs/oauth-credential-import.md)。

## 远程访问

生产环境必须使用 HTTPS。推荐让应用仅监听服务器本机，由宝塔 Nginx 反向代理：

```dotenv
APP_BASE_URL=https://mail.example.com
APP_LISTEN_ADDR=:8080
APP_DATA_DIR=/data
APP_TIMEZONE=Asia/Shanghai
APP_LOG_LEVEL=info
# 可选，仅用于首次启动默认值；也可在管理台设置
MS_CLIENT_ID=
```

Nginx 对外提供 HTTPS，并反向代理到 `http://127.0.0.1:8080`。`APP_BASE_URL` 为 HTTPS 时，会话 Cookie 自动启用 `Secure`；非回环地址使用 HTTP 会被程序拒绝。

## Docker Compose

复制 `.env.example` 为 `.env`，设置公网 HTTPS 地址；Client ID 可以稍后在管理台“设置”中保存。生产部署直接拉取公开 GHCR 固定版本镜像，不需要在宝塔服务器构建源码：

```bash
docker pull ghcr.io/wyk335858575/outlook-mail-manager:1.0.3
docker compose up -d --no-build app
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
docker compose logs --tail=100 app
```

Compose 默认只把端口绑定到宿主机 `127.0.0.1:8080`，用于 Nginx 反向代理。生产镜像不包含 Node.js，数据保存在 `outlook_data` 卷中。应用容器不挂载 Docker Socket；在线更新只在管理员主动执行 Release 脚本时临时运行。宝塔面板拉取镜像、Compose 编排、digest 校验、HTTPS、故障排查、备份和更新的完整步骤见 [宝塔 Docker 镜像部署与运维](docs/baota-deployment.md)。

## 分类、清理与通知

- 分类固定为重要、验证码、营销、垃圾、普通和待确认，规则只使用发件人、域名、主题和正文关键词。
- 分类器只生成候选。管理员批准后，邮件先进入应用创建的待清理文件夹，并保留 14 天恢复期。
- 宽限期结束后只移动到 Outlook“已删除邮件”，程序没有永久删除接口，也不会清空该文件夹。
- 统一收件箱、个性化收件箱和验证码支持管理员手动将单封邮件移入 Outlook“已删除邮件”；失败时不会提前隐藏本地邮件，恢复仍在 Outlook 中完成。
- 通知支持 Telegram、PushPlus 和带时间戳、投递 ID、SHA-256 HMAC 签名的 Webhook；通知不包含完整正文。
- 个性化收件箱使用独立规则筛选付款、返利或指定公司的重要邮件；是否发送通知仍由通知中心单独配置。

## 只读外部 API

在“API token”页面创建 token，并明确选择 scope、账号或分组范围，可选限制 IP/CIDR 和到期时间。完整 token 只显示一次，数据库只保存 SHA-256 哈希。

固定接口：

- `GET /api/v1/accounts`
- `GET /api/v1/messages`
- `GET /api/v1/messages/{public_id}`
- `GET /api/v1/otp/latest`
- `GET /api/v1/health`

所有请求使用 `Authorization: Bearer <token>`。邮件和验证码响应带 `Cache-Control: no-store`，API 不提供已读、星标、移动、清理或规则修改能力。

创建 token、scope/账号范围、Bearer 请求、验证码 cURL/PowerShell/Node.js 示例与错误排查见 [只读 API 使用教程](docs/api-usage.md)。

## 备份与恢复

在线创建 SQLite 一致性备份：

```powershell
outlook-mail-manager backup
```

备份保存在数据目录的 `backups` 子目录，并在健康页显示大小和 SHA-256。离线恢复前必须停止服务：

```powershell
outlook-mail-manager restore C:\path\to\backup.db
```

恢复命令会先验证 SQLite 完整性，并把当前数据库保留为带 `before-restore` 时间戳的文件。恢复后启动服务，使用管理员密码和 TOTP 登录，再检查账号授权与增量同步状态。

Docker Compose 环境可使用：

```powershell
docker compose run --rm app backup
docker compose stop app
docker compose run --rm app restore /data/backups/备份文件名.db
docker compose up -d app
```

健康页可删除指定备份。删除接口只接受备份目录中的普通 `outlook-manager-*.db` 文件，拒绝路径穿越、符号链接、目录和当前数据库；删除最后一个备份时会额外警告，操作写入审计日志。

## 在线更新

健康页读取配置仓库的 GitHub Releases 最新稳定版，并在有新版本时显示可复制的宝塔终端命令。无需安装 systemd 服务或常驻更新助手：

```bash
curl -fsSL https://github.com/wyk335858575/outlook-mail-manager/releases/latest/download/update.sh | bash
```

脚本只在本次升级期间运行，自动识别 amd64/arm64，下载临时 Cosign 和 updater，验证当前 Release 标签对应的 GitHub Actions OIDC 身份、签名 manifest、文件哈希和固定镜像 digest。它会停止旧应用，再由已验证的新镜像创建并逐表核对 SQLite 一致性备份；升级后轮询 `/healthz`，失败时先用可信镜像恢复数据库，再切回旧镜像。完整流程和异常恢复见 [单次升级与回滚](docs/online-update.md)。

## 版本规则

首个正式版本为 `1.0.0`。之后每次正式更新只递增补丁号，例如 `1.0.1`、`1.0.2`，`1.0.9` 之后为 `1.0.10`。根目录 `VERSION` 是唯一版本来源：

```powershell
node scripts/version.mjs check
node scripts/version.mjs bump
```

`bump` 会同步代码和部署文件中的机械版本字段；补充 CHANGELOG 后再次运行 `check`。发布时打开 GitHub 仓库的 Actions，选择 `prepare release` 并点击 `Run workflow`。工作流会重复测试、创建严格连续的 `v${VERSION}` 标签，再触发镜像签名和 GitHub Release；无需本地安装 `gh`。

首次发布 GHCR 容器后，需要在 GitHub Packages 设置中确认 `outlook-mail-manager` 包为 Public。Fork 用户必须把 `.env` 中的仓库和镜像替换为自己的地址，否则签名身份校验会拒绝更新。

## 从旧版升级

当前数据库版本为 14。服务发现旧 schema 时会在迁移前自动执行 `VACUUM INTO` 一致性备份，再事务化升级。版本 12 创建独立个性化规则，版本 13 将同步周期从分钟迁移为秒，版本 14 创建加密的 OAuth 导入任务与任务明细表。内部开发版本 `0.11.0` 可直接升级到正式版 `1.0.0`，无需重新创建管理员。

宝塔 HTTPS、Nginx 反向代理、升级和回滚步骤见 `docs/baota-deployment.md`。

## 验证

```powershell
go test ./...
go vet ./...
npm --prefix web test
npm --prefix web run typecheck
npm --prefix web run build
```

`GET /healthz` 只返回服务和数据库是否就绪，不返回账号、配置或敏感信息。

## 安全、贡献与许可

- 安全边界与漏洞报告：[SECURITY.md](SECURITY.md)
- 开发、测试和提交规范：[CONTRIBUTING.md](CONTRIBUTING.md)
- 版本记录：[CHANGELOG.md](CHANGELOG.md)
- 许可证：GNU Affero General Public License v3.0，见 [LICENSE](LICENSE)

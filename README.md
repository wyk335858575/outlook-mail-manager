# 📬 Outlook Mail Hub

> 一个面向个人 Microsoft 邮箱的自托管多账号收件中枢。

[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

把分散在不同 Outlook / Hotmail 账号里的邮件集中到一个管理台：统一收件、搜索、提取验证码、筛选重要邮件、发送通知，并用可恢复的方式清理邮件。

项目面向**单管理员**和约 **1000 个个人 Microsoft 邮箱**，可部署在 2 核 2 GiB 的 Linux VPS、Docker Compose 或宝塔面板中。代码仓库与镜像标识继续使用 `outlook-mail-manager`，已有部署和升级地址不受展示名称调整影响。

> 🔐 **安全边界**
>
> 不保存邮箱密码，不申请 `Mail.Send`，不提供发信、回复、转发或永久删除功能。邮件规则在本地运行，不把正文交给外部 AI。

## 🧭 快速导航

- [它能做什么](#-它能做什么)
- [开始部署](#-开始部署)
- [配置 Microsoft OAuth](#-配置-microsoft-oauth)
- [导入与管理账号](#-导入与管理账号)
- [分类清理与通知](#-分类清理与通知)
- [只读外部 API](#-只读外部-api)
- [备份恢复与在线更新](#-备份恢复与在线更新)
- [本地开发与验证](#-本地开发与验证)

## ✨ 它能做什么

| 模块 | 能力 |
| --- | --- |
| 📥 统一收件箱 | 聚合多个 Outlook / Hotmail 账号，支持增量同步、未读管理和单账号筛选 |
| 🔎 邮件检索 | 使用 SQLite FTS5 搜索邮件，数据保存在自己的服务器中 |
| 🔐 验证码 | 自动识别验证码邮件，并提供独立查看入口和只读 API |
| ⭐ 个性化收件箱 | 用本地规则筛选付款、返利或指定公司的重要邮件 |
| 🔔 消息通知 | 支持 Telegram、PushPlus 和 WXPush |
| 🧹 安全清理 | 先审核、再进入待清理区，保留 14 天恢复期，最终只移入 Outlook“已删除邮件” |
| 👥 千账号管理 | 搜索、分页、跨页全选，批量启用、停用或删除本地账号数据 |
| 🧩 只读 API | 查询账号、邮件、验证码和健康状态，可限制账号范围、IP 与到期时间 |
| 💾 运维工具 | SQLite 一致性备份、完整性校验、健康检查和签名在线更新 |

### 适合这些场景

- 你有多个个人 Outlook / Hotmail 邮箱，希望在一个页面里集中收件。
- 你经常查验证码、付款、返利或 PayPal 等重要邮件。
- 你愿意自托管，并希望 OAuth token、邮件索引和规则留在自己的服务器。

### 它不是什么

- 不是完整邮件客户端，不支持写信、回信、转发或草稿。
- 不支持 Gmail、IMAP、POP3、SMTP，也不面向工作或学校 Microsoft 账号。
- 不会永久删除邮件；恢复操作仍在 Outlook“已删除邮件”中完成。

## 🛡️ 管理员与数据安全

- 首次打开管理台时创建唯一管理员，需要账号、至少 12 个字符的密码和 TOTP 身份验证器。
- 登录必须同时提供管理员账号、密码和六位 TOTP 验证码。
- 程序随机生成数据密钥。管理员密码经 Argon2id 派生后用于保护数据密钥，TOTP secret 和 OAuth token 再由数据密钥加密。
- 服务重启后数据密钥保持锁定；管理员首次登录成功后，才在进程内存中解锁并恢复后台同步。
- 不使用初始化凭据、恢复码、`APP_MASTER_KEY` 或磁盘 `master.key` 文件。
- 邮件 HTML 会经过清理，远程图片默认不会自动加载。

> ⚠️ **请认真保管管理员密码和 TOTP**
>
> 忘记密码或丢失身份验证器后，原数据密钥无法恢复。只能重置管理员，并重新授权 Microsoft 邮箱。

首次设置页面没有额外初始化口令。公网安装时，请先用服务器防火墙或宝塔访问限制，仅允许管理员 IP；完成管理员创建后再开放站点。

## 🚀 开始部署

### Docker Compose（推荐）

生产环境建议直接拉取公开 GHCR 镜像，不需要在服务器上编译源码。

```bash
git clone https://github.com/wyk335858575/outlook-mail-manager.git
cd outlook-mail-manager
cp .env.example .env
```

编辑 `.env`，至少确认公网地址和监听配置：

```dotenv
APP_BASE_URL=https://mail.example.com
APP_LISTEN_ADDR=:8080
APP_DATA_DIR=/data
APP_TIMEZONE=Asia/Shanghai
APP_LOG_LEVEL=info
GOMEMLIMIT=512MiB
GOGC=75

# 可选：也可以在管理台“设置”中填写
MS_CLIENT_ID=
```

然后启动固定版本镜像：

```bash
docker pull ghcr.io/wyk335858575/outlook-mail-manager:1.0.9
docker compose up -d --no-build app
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
docker compose logs --tail=100 app
```

Compose 默认只把应用端口绑定到宿主机 `127.0.0.1:8080`，供 Nginx 反向代理使用。生产镜像不包含 Node.js，数据保存在 `outlook_data` 卷中，应用容器也不会挂载 Docker Socket。

### HTTPS 与宝塔面板

生产环境必须使用 HTTPS。让应用监听服务器本机，再由宝塔 Nginx 把公网域名反向代理到：

```text
http://127.0.0.1:8080
```

当 `APP_BASE_URL` 使用 HTTPS 时，会话 Cookie 会自动启用 `Secure`；程序会拒绝非回环地址上的 HTTP 部署。

宝塔面板拉取镜像、Compose 编排、HTTPS、备份、更新与故障排查，请直接查看 [宝塔 Docker 部署与运维教程](docs/baota-deployment.md)。

## 🔑 配置 Microsoft OAuth

1. 在 Microsoft Entra 管理中心创建应用注册，账号类型推荐选择“仅个人 Microsoft 账户”。
2. 在“身份验证”中允许公共客户端流。设备码授权不需要 client secret。
3. 添加 Microsoft Graph 委托权限：`User.Read` 和 `Mail.ReadWrite`。
4. 应用还会请求 `openid profile offline_access`；请勿添加 `Mail.Send`。
5. 进入管理台“设置” > “Microsoft OAuth”，填写 Application (client) ID 并保存，无需重启。

完整注册步骤、权限说明、Client ID 更换规则和错误排查见 [Microsoft Client ID 配置教程](docs/microsoft-client-id.md)。环境变量 `MS_CLIENT_ID` 只用于首次启动的可选默认值；管理台保存的设置优先。

## 👥 导入与管理账号

### 网页授权账号

导入纯邮箱地址，或使用：

```text
email,group,tags,notes
```

导入后逐个完成设备码授权。网页授权账号不得包含邮箱密码。

### O2 令牌账号

支持以下分隔方式：

```text
邮箱----密码----client_id----refresh_token
```

也可以使用 Tab、逗号或分号。密码只在浏览器本地用于识别字段位置，随后立即丢弃；它不会进入 HTTP 请求、日志、数据库、审计或导入结果。服务端只接收邮箱、Client ID 和 refresh token。

- 单次最多导入 1000 行，最多 4 个并发验证。
- 服务端会换取新 token、检查权限，并通过 `/me` 校验稳定 Microsoft 用户 ID。
- Microsoft 主邮箱与导入邮箱不一致时会拒绝保存，请改用设备码流程确认别名。
- 已存在的账号默认跳过；只有明确选择“覆盖已有授权”时才替换凭据。
- 导入任务结束后会清除暂存的 refresh token 密文。

完整格式、覆盖规则与错误排查见 [O2 令牌导入与账号编辑](docs/oauth-credential-import.md)。

### 日常管理

账号管理页会区分“网页授权账号”和“O2 令牌账号”。你可以按邮箱、显示名称、分组、标签或备注搜索，使用服务端分页和最多 1000 个匹配账号的跨页全选，并批量启用、停用或删除本地账号数据。

导入邮箱、分组、标签和备注可以编辑；账号类型、Microsoft 主邮箱、显示名称和稳定用户 ID 只读。替换 O2 令牌时，系统会先验证新的 Client ID 与 refresh token；验证失败不会清空现有有效 token。

## 📂 分类、清理与通知

### 分类和个性化收件箱

分类包括重要、验证码、营销、垃圾、普通和待确认。规则只使用发件人、域名、主题和正文关键词，不调用外部 AI。

个性化收件箱有独立规则，可以筛选付款、返利或指定公司的邮件。规则只决定邮件是否进入个性化收件箱；是否通知仍在通知中心单独设置。

### 安全清理

1. 分类器只生成候选，不会自动删除。
2. 管理员批准后，邮件进入应用创建的待清理文件夹。
3. 邮件保留 14 天恢复期。
4. 宽限期结束后，只移入 Outlook“已删除邮件”。

统一收件箱、个性化收件箱和验证码页也支持管理员手动移动单封邮件。Graph 操作失败时，本地邮件不会提前隐藏。

### 通知通道

支持 Telegram、PushPlus 和 WXPush。WXPush 由 Go 服务端直接发送，可附带最多 500 字符的纯文本正文预览，不包含完整正文。

#### WXPush

WXPush 已集成在应用中，不需要另行部署 WXPush 服务。新建通道时填写：

- `WX_APPID`、`WX_SECRET`：微信公众平台测试号或公众号的 AppID、AppSecret。
- `WX_USERID`：接收者 OpenID；多人接收时分别创建通道。
- `WX_TEMPLATE_ID`：微信模板消息 ID。

模板建议使用以下动态字段：

```text
通知类型：{{title.DATA}}
寄件人：{{sender.DATA}}
邮件标题：{{subject.DATA}}
正文：{{body.DATA}}
```

程序也会发送兼容旧模板的 `content` 字段。创建前请先点击“测试配置”，确认微信收到动态测试消息。AppSecret、OpenID 和 access token 不会出现在通道响应、审计或投递错误中。参考实现：<https://github.com/frankiejun/wxpush>。

## 🧩 只读外部 API

在“API token”页面创建 token，并选择 scope、账号、分组或动态“全部账号”范围。还可以限制 IP/CIDR 和到期时间。完整 token 只显示一次，数据库只保存 SHA-256 哈希；活跃 token 需要先撤销，之后才能永久删除。

```http
GET /api/v1/accounts
GET /api/v1/messages
GET /api/v1/messages/{public_id}
GET /api/v1/otp/latest
GET /api/v1/health
```

推荐通过 Bearer Token 调用：

```bash
curl -H "Authorization: Bearer <token>" \
  "https://mail.example.com/api/v1/otp/latest?account=<account>&wait_seconds=30"
```

所有接口都是只读 GET。程序不提供已读、星标、移动、清理或规则修改 API。查询参数 `access_token` 虽然可以生成浏览器直开链接，但可能进入浏览器历史和代理日志；生产环境请使用 HTTPS，并限制 token 的有效期和来源 IP。

参数说明及 cURL、PowerShell、Node.js、C# 示例见 [只读 API 使用教程](docs/api-usage.md)。

## 💾 备份、恢复与在线更新

### 创建备份

```powershell
outlook-mail-manager backup
```

备份保存在数据目录的 `backups` 子目录，健康页会显示文件大小和 SHA-256。健康页也可以删除指定备份；程序会拒绝路径穿越、符号链接、目录和当前数据库，删除最后一个备份时还会额外警告。

### 离线恢复

恢复前必须停止服务：

```powershell
outlook-mail-manager restore C:\path\to\backup.db
```

程序会先检查 SQLite 完整性，并把当前数据库保留为带 `before-restore` 时间戳的文件。

Docker Compose 环境：

```bash
docker compose run --rm app backup
docker compose stop app
docker compose run --rm app restore /data/backups/备份文件名.db
docker compose up -d app
```

### 单次在线更新

健康页会检查 GitHub Releases 最新稳定版。发现新版本后，在宝塔终端运行：

```bash
curl -fsSL https://github.com/wyk335858575/outlook-mail-manager/releases/latest/download/update.sh | bash
```

脚本只在本次升级期间运行，不需要安装 systemd 服务。它会自动识别 amd64/arm64，验证 GitHub Actions OIDC/Cosign 签名、Release manifest、文件哈希和固定镜像 digest；随后创建 SQLite 一致性备份、切换镜像并轮询 `/healthz`。健康检查失败时会恢复升级前数据库和旧镜像。

完整流程见 [单次升级与回滚教程](docs/online-update.md)。

## 🧱 版本与升级规则

根目录 [`VERSION`](VERSION) 是唯一版本来源。正式版本从 `1.0.0` 开始，每次只递增补丁号：`1.0.1`、`1.0.2`，`1.0.9` 之后是 `1.0.10`。

```powershell
node scripts/version.mjs check
node scripts/version.mjs bump
```

补充 CHANGELOG 后，在 GitHub Actions 中运行 `prepare release`。工作流会重新执行测试、创建严格连续的 `v${VERSION}` 标签，并触发多架构镜像构建、签名和 GitHub Release。

数据库当前 schema 版本为 20。程序发现旧 schema 时，会先通过 `VACUUM INTO` 创建一致性备份，再在事务中执行迁移。内部开发版本 `0.11.0` 可以直接升级到正式版 `1.0.0`，无需重新创建管理员。

Fork 用户必须把 `.env` 中的仓库和镜像地址改为自己的地址，否则签名身份校验会拒绝更新。首次发布 GHCR 镜像后，也需要在 GitHub Packages 中确认镜像包为 Public。

## 🧰 本地开发与验证

需要 Go 1.26、Node.js 24 和 npm。

```powershell
npm --prefix web install
npm --prefix web run build

$env:APP_DATA_DIR = ".\data\runtime"
$env:APP_BASE_URL = "http://localhost:8080"
$env:APP_LISTEN_ADDR = "127.0.0.1:8080"
$env:MS_CLIENT_ID = "你的 Microsoft Application (client) ID" # 可选

go run ./cmd/server
```

打开 <http://localhost:8080>，按页面提示创建管理员。直接运行仓库根目录中的 Windows EXE 时，未设置 `APP_DATA_DIR` 也会默认使用 `data\runtime`。

提交前运行：

```powershell
go test ./...
go vet ./...
npm --prefix web test
npm --prefix web run typecheck
npm --prefix web run build
```

`GET /healthz` 只返回服务和数据库是否就绪，不暴露账号、配置或敏感信息。

## 📚 文档与参与

- [Microsoft Client ID 配置](docs/microsoft-client-id.md)
- [O2 令牌导入与账号编辑](docs/oauth-credential-import.md)
- [只读 API 使用教程](docs/api-usage.md)
- [宝塔 Docker 部署与运维](docs/baota-deployment.md)
- [单次升级与回滚](docs/online-update.md)
- [安全策略与漏洞报告](SECURITY.md)
- [开发、测试和贡献规范](CONTRIBUTING.md)
- [版本记录](CHANGELOG.md)

## ⚖️ 许可证

本项目采用 [GNU Affero General Public License v3.0](LICENSE)。

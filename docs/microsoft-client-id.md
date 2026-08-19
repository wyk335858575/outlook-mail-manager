# 注册并配置 Microsoft Client ID

本项目只支持个人 Outlook、Hotmail 和其他 Microsoft 个人邮箱。每个开源部署实例都应注册并使用自己的 Microsoft Entra 应用，不要复制 Thunderbird、其他开源项目或公共工具的 Client ID。

Client ID 是公开的应用标识，不是密码。设备码授权不需要 client secret，本项目也不会收集邮箱密码。

## 1. 打开 Microsoft Entra 管理中心

1. 访问 [Microsoft Entra 管理中心](https://entra.microsoft.com/)，使用你的 Microsoft 账号登录。
2. 打开“标识”或“Identity”。
3. 进入“应用程序” > “应用注册”。
4. 选择“新注册”。

如果账号还没有可用的 Microsoft Entra 租户，管理中心会提示先创建租户。租户只用于保存应用注册；被管理的 Outlook/Hotmail 邮箱仍然通过 Microsoft 个人账号登录。

## 2. 创建应用注册

填写以下内容：

- **名称**：例如 `Outlook Mail Manager`。名称只用于你在管理中心识别应用。
- **支持的账户类型**：推荐选择“仅个人 Microsoft 账户”。
- **重定向 URI**：留空。设备码流程不依赖浏览器回调地址。

选择“注册”。注册完成后会进入应用的“概述”页。

## 3. 复制 Application (client) ID

在“概述”页找到“应用程序(客户端) ID”或 “Application (client) ID”，复制形如下面的 GUID：

```text
00000000-0000-0000-0000-000000000000
```

不要复制“对象 ID”“目录(租户) ID”，也不要创建或填写客户端密码。

## 4. 启用公共客户端设备码流程

1. 在应用左侧打开“身份验证”。
2. 找到“高级设置”。
3. 将“允许公共客户端流”设置为“是”。
4. 保存更改。

如果该选项未启用，设备码或令牌请求可能返回 `invalid_client`、`AADSTS7000218`，或提示必须提供 client secret。

## 5. 配置 Microsoft Graph 委托权限

1. 打开“API 权限”。
2. 选择“添加权限”。
3. 选择“Microsoft Graph”。
4. 选择“委托的权限”，不要选择“应用程序权限”。
5. 添加以下权限：

| 权限 | 用途 |
| --- | --- |
| `User.Read` | 读取当前登录账号的稳定用户 ID、显示名称和邮箱地址 |
| `Mail.ReadWrite` | 读取邮件、同步已读和星标状态，并执行可恢复的邮件移动 |

程序在授权时还会请求 `openid`、`profile` 和 `offline_access`，用于确认登录身份并取得可刷新的离线授权。如果管理中心在 OpenID 权限中显示这些项目，可以一并添加。

不要添加 `Mail.Send`。本项目不发信、不回复、不转发，也不会永久删除邮件。个人 Microsoft 账号通常可以在授权页自行同意这些委托权限，不需要租户管理员授予应用程序权限。

## 6. 在项目中保存 Client ID

1. 启动项目并完成唯一管理员的账号、密码和身份验证器设置。
2. 登录管理台，打开“设置”。
3. 找到“Microsoft OAuth”。
4. 粘贴 Application (client) ID。
5. 选择“保存 Client ID”。

保存后立即生效，不需要重启服务。`MS_CLIENT_ID` 环境变量仍可作为首次启动的可选默认值；一旦在设置页保存过 Client ID，数据库中的设置优先于环境变量。

## 7. 授权 Outlook 或 Hotmail 账号

1. 打开“账号管理”。
2. 导入纯邮箱地址，或导入 `email,group,tags,notes` CSV。
3. 选择账号的“开始授权”。
4. 打开界面显示的 Microsoft 设备登录页。
5. 输入设备码，选择要授权的 Microsoft 个人账号并同意权限。
6. 返回管理台，等待账号状态变为“正常”或按提示确认邮箱别名。

不要把邮箱密码写入 CSV、环境变量或项目配置。授权另一个账号时，在 Microsoft 页面选择“使用其他账号”；如果浏览器仍自动进入上一个账号，可使用无痕窗口完成该次设备码登录。

## 更换 Client ID

设置页更换 Client ID 后：

- 新开始的设备码授权使用新 Client ID。
- 已经授权的账号继续使用各自授权时的 Client ID 刷新令牌。
- 正在进行的设备码任务继续使用生成设备码时的 Client ID，不会被设置变更打断。
- 如果要把旧账号迁移到新 Client ID，需要对该账号重新授权。

不要删除仍有账号使用的旧 Entra 应用注册。删除后，对应账号的 refresh token 将无法继续刷新。

## 常见问题

### `invalid_microsoft_client_id`

设置页只接受标准 GUID。确认复制的是“应用程序(客户端) ID”，不是对象 ID 或租户 ID，并删除首尾多余字符。

### `invalid_client` 或要求 client secret

检查以下项目：

1. Client ID 是否来自当前仍存在的应用注册。
2. “允许公共客户端流”是否已经设置为“是”并保存。
3. 应用支持的账户类型是否包含个人 Microsoft 账户。
4. 是否误把对象 ID 或租户 ID 当成 Client ID。

### 个人 Outlook/Hotmail 账号无法登录

应用注册的账户类型可能只允许组织目录账号。重新创建或修改应用，使其支持个人 Microsoft 账户。本项目使用 `consumers` 授权端点，不支持仅工作或学校账号的应用注册。

### 授权成功但提示权限不足或同步返回 403

确认添加的是 Microsoft Graph **委托的权限** `User.Read` 和 `Mail.ReadWrite`，而不是应用程序权限。修改权限后，旧授权不会自动增加 scope，需要对账号重新授权并再次同意。

### 修改了 Client ID，但旧账号仍显示旧配置错误

这是预期的兼容行为：旧账号绑定原 Client ID。要迁移该账号，请在账号管理中重新授权。若原应用注册已被删除，只能使用新 Client ID 完整重新授权。

## Microsoft 官方资料

- [注册 Microsoft identity platform 应用](https://learn.microsoft.com/entra/identity-platform/quickstart-register-app)
- [OAuth 2.0 设备授权流程](https://learn.microsoft.com/entra/identity-platform/v2-oauth2-device-code)
- [桌面应用和公共客户端配置](https://learn.microsoft.com/entra/identity-platform/scenario-desktop-app-registration)
- [Microsoft Graph 权限参考](https://learn.microsoft.com/graph/permissions-reference)

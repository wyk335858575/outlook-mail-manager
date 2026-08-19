# OAuth 凭据导入与账号编辑

账号管理提供“基础账号”和“OAuth 凭据”两种导入方式。基础账号用于之后逐个设备码授权；OAuth 凭据用于迁移你自己合法持有的 Client ID 与 refresh token。

## 基础账号

每行可填写：

```text
email,group,tags,notes
user@example.com,Finance,payment|important,付款账号
```

标签使用 `|` 分隔。基础账号导入不接受邮箱密码。

## OAuth 凭据

支持以下四种分隔符，单次最多 1000 行：

```text
邮箱----密码----client_id----refresh_token
邮箱<Tab>密码<Tab>client_id<Tab>refresh_token
邮箱,密码,client_id,refresh_token
邮箱;密码;client_id;refresh_token
```

“密码”列只用于兼容既有文本格式。浏览器解析字段后立即丢弃该列，提交的 JSON 只有 `email`、`client_id` 和 `refresh_token`。密码不会进入 HTTP 请求、后端、日志、数据库、审计或错误结果。

不要为了导入而提供或收集邮箱密码。本项目不会用密码自动登录 Microsoft，也不会绕过 Microsoft 的交互授权。

## 验证流程

1. 后端把待处理 refresh token 临时加密写入导入任务表。
2. 最多 4 个 worker 使用对应 Client ID 向 Microsoft 刷新 token。
3. 验证返回 scope 包含项目所需权限。
4. 调用 Microsoft Graph `/me` 读取稳定用户 ID、主邮箱和显示名称。
5. 新账号的主邮箱必须与导入邮箱一致；不一致可能是别名，必须改用设备码流程确认。
6. 已有账号默认跳过；勾选“覆盖已有授权”后才允许验证并替换。
7. 每项完成后立即清除任务表中的 refresh token 密文；脱敏结果保留 24 小时。

refresh token 可能被 Microsoft 撤销、因长期未使用失效，或在安全策略变化后要求重新登录。程序会原子保存刷新响应中的新 refresh token，但不会承诺 token 永不失效。

## 编辑账号

账号列表的编辑按钮允许修改导入邮箱、分组、标签和备注。Microsoft 主邮箱、显示名称和稳定用户 ID 来自 `/me`，不可手工修改。

“替换 OAuth 凭据”要求重新输入 Client ID 与 refresh token。服务端先验证凭据属于同一稳定 Microsoft 用户 ID，再原子替换；验证或写入失败时保留当前有效 token。

## 常见结果

- `账号已存在，未勾选覆盖已有授权`：保持现有凭据，按需重新导入并明确勾选覆盖。
- `Microsoft 主邮箱与导入邮箱不一致`：可能导入了别名，改用设备码授权确认。
- `凭据属于另一个 Microsoft 账号`：Client ID/refresh token 与目标账号不匹配，拒绝替换。
- `授权缺少 User.Read 或 Mail.ReadWrite 权限`：使用正确权限重新取得 refresh token。
- `Client ID 或 refresh token 无效`：确认应用支持个人 Microsoft 账号、启用公共客户端流，并重新授权。

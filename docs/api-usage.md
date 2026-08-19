# 只读 API 使用教程

Outlook Mail Manager 的 `/api/v1/*` 接口只提供读取能力。它不能发信、修改已读状态、移动邮件或执行清理。生产环境必须使用 HTTPS。

## 1. 创建 API token

1. 登录管理台，打开“API token”。
2. 点击“创建 token”并填写名称。
3. 选择所需的只读 scope：
   - `accounts:read`：账号列表与同步状态。
   - `mail:read`：邮件列表和纯文本正文。
   - `otp:read`：最新验证码和针对单账号的优先同步。
   - `system:read`：脱敏系统健康状态。
4. 至少选择一个账号或分组范围。未在范围内的账号不可访问。
5. 可选设置到期时间和允许的 IP/CIDR。
6. 创建后立即保存完整 token。它只显示一次，服务端仅保存 SHA-256 哈希。

API token 只应保存在调用方服务端的 secret 管理器或环境变量中。不要写入浏览器前端、`localStorage`、日志或公开仓库。

```bash
export OMM_API_TOKEN='omm_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'
export OMM_BASE_URL='https://mail.example.com'
```

PowerShell：

```powershell
$env:OMM_API_TOKEN = 'omm_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'
$env:OMM_BASE_URL = 'https://mail.example.com'
```

## 2. Bearer 请求

所有请求都在 `Authorization` 请求头中携带 token：

```text
Authorization: Bearer <token>
```

例如读取授权范围内的账号：

```bash
curl "$OMM_BASE_URL/api/v1/accounts" \
  -H "Authorization: Bearer $OMM_API_TOKEN"
```

## 3. 调用最新验证码

`GET /api/v1/otp/latest` 参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `account` | 是 | 账号公开 ID、导入邮箱或 Microsoft 主邮箱。 |
| `after` | 是 | RFC 3339 UTC 时间，例如 `2026-08-18T08:00:00Z`。只查询该时间之后的验证码。 |
| `wait_seconds` | 否 | `0` 到 `30`。大于 0 时将账号加入高优先级同步并等待结果。 |
| `sender` | 否 | 精确匹配发件人邮箱地址。 |
| `subject` | 否 | 主题包含的文本。 |

### cURL

```bash
curl --get "$OMM_BASE_URL/api/v1/otp/latest" \
  -H "Authorization: Bearer $OMM_API_TOKEN" \
  --data-urlencode "account=account@example.com" \
  --data-urlencode "after=2026-08-18T08:00:00Z" \
  --data-urlencode "wait_seconds=30" \
  --data-urlencode "sender=no-reply@example.com" \
  --data-urlencode "subject=verification code"
```

### PowerShell

```powershell
$headers = @{ Authorization = "Bearer $env:OMM_API_TOKEN" }
$query = @{
  account = 'account@example.com'
  after = '2026-08-18T08:00:00Z'
  wait_seconds = '30'
  sender = 'no-reply@example.com'
  subject = 'verification code'
}
$encoded = ($query.GetEnumerator() | ForEach-Object {
  '{0}={1}' -f [uri]::EscapeDataString($_.Key), [uri]::EscapeDataString($_.Value)
}) -join '&'
Invoke-RestMethod -Method Get -Uri "$env:OMM_BASE_URL/api/v1/otp/latest?$encoded" -Headers $headers
```

### Node.js 18+

```javascript
const url = new URL(`${process.env.OMM_BASE_URL}/api/v1/otp/latest`)
url.searchParams.set('account', 'account@example.com')
url.searchParams.set('after', '2026-08-18T08:00:00Z')
url.searchParams.set('wait_seconds', '30')
url.searchParams.set('sender', 'no-reply@example.com')
url.searchParams.set('subject', 'verification code')

const response = await fetch(url, {
  headers: { Authorization: `Bearer ${process.env.OMM_API_TOKEN}` },
})
if (!response.ok) throw new Error(`HTTP ${response.status}`)
console.log(await response.json())
```

## 4. 理解响应

```json
{
  "code": "123456",
  "message_public_id": "msg_xxx",
  "received_at": "2026-08-18T08:00:05Z",
  "synced_at": "2026-08-18T08:00:08Z",
  "fresh": true,
  "account_status": "active"
}
```

- `code`：提取到的验证码。无新验证码时为 `null`。
- `fresh`：本次等待期内账号同步成功，且找到 `after` 之后的邮件时为 `true`。
- `received_at`：邮件在 Microsoft 端的收件时间。
- `synced_at`：账号最后成功写入本地索引的时间。
- `account_status`：账号当前状态，如 `active`、`degraded` 或 `reauth_required`。
- `retry_after_seconds`：同步未成功时建议等待的秒数。

接口不会为了“有结果”而返回 `after` 之前的旧验证码。

## 5. 错误排查

- `401 Unauthorized`：Bearer token 缺失、错误、已撤销或已过期。
- `404 Not Found`：账号不存在，或 token 未获得该账号/分组的访问权。两种情况故意使用相同响应，避免账号枚举。
- `429 Too Many Requests`：降低调用频率，并按响应的 `Retry-After` 等待后重试。
- `code: null`：这是没有符合条件的新验证码，不是服务器错误。检查 `after`、`sender` 和 `subject`。
- `account_status: reauth_required`：在管理台“账号管理”重新授权。
- 出现 `retry_after_seconds`：账号同步未在等待期内成功，不要立即紧密循环请求。

## 6. 其他固定接口

- `GET /api/v1/accounts`
- `GET /api/v1/messages`
- `GET /api/v1/messages/{public_id}`
- `GET /api/v1/otp/latest`
- `GET /api/v1/health`

邮件与验证码响应均设置 `Cache-Control: no-store`。

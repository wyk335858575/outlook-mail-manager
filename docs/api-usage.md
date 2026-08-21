# 只读 API 使用教程

Outlook Mail Manager 的 `/api/v1/*` 接口只提供读取能力，不能发信、修改已读状态、移动邮件或执行清理。生产环境必须使用 HTTPS。

## 1. 创建 API token

1. 登录管理台，打开“API token”。
2. 点击“创建 token”并填写名称。
3. 选择需要的只读 scope：
   - `accounts:read`：账号列表与同步状态。
   - `mail:read`：邮件列表和纯文本正文。
   - `otp:read`：最新验证码和针对单账号的优先同步。
   - `system:read`：脱敏系统健康状态。
4. 选择账号、分组范围，或选择“全部账号”。“全部账号”会自动包含以后新添加的账号。
5. 可选设置到期时间和允许的 IP/CIDR。浏览器直开链接建议同时设置这两项。
6. 创建后立即保存完整 token。它只显示一次，服务端仅保存 SHA-256 哈希。

已经泄露的 token 应立即撤销并重新创建。已撤销或已过期的 token 可以在管理台永久删除；活跃 token 必须先撤销。

## 2. 两种认证方式

### Bearer 请求头（程序调用推荐）

```http
Authorization: Bearer <token>
```

```bash
export OMM_API_TOKEN='omm_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'
export OMM_BASE_URL='https://mail.example.com'

curl "$OMM_BASE_URL/api/v1/accounts" \
  -H "Authorization: Bearer $OMM_API_TOKEN"
```

PowerShell：

```powershell
$env:OMM_API_TOKEN = 'omm_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'
$env:OMM_BASE_URL = 'https://mail.example.com'
$headers = @{ Authorization = "Bearer $env:OMM_API_TOKEN" }
Invoke-RestMethod -Method Get -Uri "$env:OMM_BASE_URL/api/v1/accounts" -Headers $headers
```

### 浏览器直开链接

只读 GET 接口也接受固定参数 `access_token`：

```text
https://mail.example.com/api/v1/accounts?access_token=omm_xxx
```

可以在管理台点击“生成浏览器链接”，选择接口、填写参数后复制或在新标签页打开。生成器中的 token 只保留在当前弹窗内，不写入浏览器存储或服务端。

浏览器链接适合人工查看 JSON，不适合程序长期调用。完整网址可能进入浏览器历史、剪贴板、Nginx、宝塔或 CDN 访问日志。应用自身只记录请求路径，不记录查询字符串。生产环境必须使用 HTTPS，建议限制 token 的 IP 和到期时间。

同时提供请求头和查询参数时，请求头优先；请求头错误时不会回退使用查询参数。

## 3. 固定接口

### 账号列表

```http
GET /api/v1/accounts
Scope: accounts:read
```

返回授权范围内账号的公开 ID、显示名称、邮箱、状态、分组、标签、最后同步时间和脱敏的重新授权原因。无额外业务参数。

```text
https://mail.example.com/api/v1/accounts?access_token=omm_xxx
```

### 邮件列表

```http
GET /api/v1/messages
Scope: mail:read
```

接口返回请求时本地索引中的当前邮件快照，不会因为打开链接自动同步账号。

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `q` | 否 | 本地全文搜索关键词。 |
| `account` | 否 | 账号公开 ID、导入邮箱或 Microsoft 主邮箱。 |
| `group` | 否 | 分组名称。 |
| `tag` | 否 | 标签名称。 |
| `category` | 否 | `important`、`verification`、`marketing`、`spam`、`normal` 或 `uncertain`。 |
| `folder` | 否 | `inbox` 或 `junkemail`。 |
| `sender` | 否 | 发件人邮箱地址。 |
| `unread` | 否 | `true` 或 `false`。 |
| `limit` | 否 | 每页 1–100，默认 50。 |
| `cursor` | 否 | 上一页响应中的 `next_cursor`。 |

```text
https://mail.example.com/api/v1/messages?account=user%40outlook.com&unread=true&limit=20&access_token=omm_xxx
```

响应包含 `messages` 和 `next_cursor`。`next_cursor` 为空表示没有下一页；继续查询时原样传回 `cursor`，并保持其他筛选条件不变。

### 单封邮件正文

```http
GET /api/v1/messages/{public_id}
Scope: mail:read
```

`public_id` 来自邮件列表的 `public_id` 字段。响应在邮件摘要之外增加安全纯文本 `body_text` 和正文缓存时间，不返回未经清洗的远程 HTML。

```text
https://mail.example.com/api/v1/messages/msg_xxx?access_token=omm_xxx
```

邮件不存在与 token 无权访问该邮件时都返回相同的 `404`，避免枚举其他账号的数据。

### 最新验证码

```http
GET /api/v1/otp/latest
Scope: otp:read
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `account` | 是 | 账号公开 ID、导入邮箱或 Microsoft 主邮箱。 |
| `wait_seconds` | 否 | 0–30；大于 0 时加入高优先级同步并等待结果。 |
| `sender` | 否 | 精确匹配发件人邮箱地址。 |
| `subject` | 否 | 邮件主题包含的文本。 |

```text
https://mail.example.com/api/v1/otp/latest?account=user%40outlook.com&wait_seconds=30&access_token=omm_xxx
```

响应示例：

```json
{
  "code": "123456",
  "message_public_id": "msg_xxx",
  "received_at": "2026-08-21T00:00:05Z",
  "synced_at": "2026-08-21T00:00:08Z",
  "fresh": true,
  "account_status": "active"
}
```

`code: null` 表示请求开始前 15 分钟内没有符合条件的验证码，不是服务器错误。多封验证码邮件只返回最新一封。同步未成功时可能返回 `retry_after_seconds`。

旧链接中的 `after` 参数会被忽略，不再作为验证码起始时间。

### 系统健康

```http
GET /api/v1/health
Scope: system:read
```

返回脱敏的数据库状态、schema 版本、备份、失败通知、磁盘、同步队列和账号状态统计，不返回账号地址、凭据或正文。

```text
https://mail.example.com/api/v1/health?access_token=omm_xxx
```

## 4. C# 调用

程序调用应把 token 放在环境变量中，并使用 Bearer 请求头：

```csharp
using System.Net.Http.Headers;

using var client = new HttpClient
{
    BaseAddress = new Uri("https://mail.example.com")
};

var token = Environment.GetEnvironmentVariable("OMM_API_TOKEN")
    ?? throw new InvalidOperationException("OMM_API_TOKEN is not set");
client.DefaultRequestHeaders.Authorization =
    new AuthenticationHeaderValue("Bearer", token);

var account = Uri.EscapeDataString("user@outlook.com");
var path = $"/api/v1/otp/latest?account={account}&wait_seconds=30";

using var response = await client.GetAsync(path);
response.EnsureSuccessStatusCode();
Console.WriteLine(await response.Content.ReadAsStringAsync());
```

## 5. JavaScript 调用

```javascript
const url = new URL(`${process.env.OMM_BASE_URL}/api/v1/otp/latest`)
url.searchParams.set('account', 'user@outlook.com')
url.searchParams.set('wait_seconds', '30')

const response = await fetch(url, {
  headers: { Authorization: `Bearer ${process.env.OMM_API_TOKEN}` },
})
if (!response.ok) throw new Error(`HTTP ${response.status}`)
console.log(await response.json())
```

## 6. 错误排查

- `400 Bad Request`：参数格式或取值错误，例如无效的 `limit`、`unread` 或 `wait_seconds`。
- `401 Unauthorized`：Bearer 或 `access_token` 缺失、错误、已撤销、已过期、scope 不足或 IP 不符。
- `404 Not Found`：账号或邮件不存在，或者 token 没有对应账号、分组或全部账号访问权。
- `429 Too Many Requests`：降低调用频率，并按响应的 `Retry-After` 等待后重试。
- `code: null`：最近 15 分钟没有符合发件人、主题条件的验证码。
- `account_status: reauth_required`：在管理台“账号管理”重新授权。

所有外部 API JSON 响应均设置 `Cache-Control: no-store`。

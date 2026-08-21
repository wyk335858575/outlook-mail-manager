import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { BookOpen, Check, Clipboard, ExternalLink, KeyRound, Link2, LoaderCircle, LogOut, Plus, ShieldCheck, ShieldOff, Trash2, X } from 'lucide-react'

import { fetchAccounts } from '../api/accounts'
import { createAPIToken, deleteAPIToken, fetchAPITokens, revokeAPIToken, type APIScope, type CreatedAPIToken } from '../api/apiTokens'
import { APIError, type AuthStatus } from '../api/auth'
import { AppFrame } from '../components/AppFrame'
import { buildBrowserAPILink, copyBrowserAPILink, maskBrowserAPILink, openBrowserAPILink, type BrowserAPIEndpoint } from './apiBrowserLinks'
import { buildOTPExamples } from './apiExamples'

const scopes: Array<{ id: APIScope; label: string }> = [
  { id: 'accounts:read', label: '账号状态' }, { id: 'mail:read', label: '邮件查询与正文' },
  { id: 'otp:read', label: '最新验证码' }, { id: 'system:read', label: '系统健康' },
]

export function APIDashboard({ status, onLogout, loggingOut, logoutError }: {
  status: AuthStatus; onLogout: () => void; loggingOut: boolean; logoutError: string
}) {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [created, setCreated] = useState<CreatedAPIToken | null>(null)
  const [browserLinkToken, setBrowserLinkToken] = useState<string | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const tokensQuery = useQuery({ queryKey: ['api-tokens'], queryFn: ({ signal }) => fetchAPITokens(signal) })
  const accountsQuery = useQuery({ queryKey: ['accounts'], queryFn: ({ signal }) => fetchAccounts(signal) })
  const tokens = tokensQuery.data?.tokens ?? []
  const active = tokens.filter((token) => !token.revoked_at && (!token.expires_at || new Date(token.expires_at) > new Date())).length

  async function revoke(publicID: string) {
    if (!status.csrf_token || !window.confirm('撤销这个 API token？所有后续请求会立即被拒绝。')) return
    setBusy(publicID); setError('')
    try { await revokeAPIToken(publicID, status.csrf_token); await queryClient.invalidateQueries({ queryKey: ['api-tokens'] }) }
    catch (reason) { setError(messageFor(reason, '无法撤销 API token')) } finally { setBusy('') }
  }

  async function remove(publicID: string) {
    if (!status.csrf_token || !window.confirm('永久删除这个已失效的 API token？删除后无法恢复。')) return
    setBusy(publicID); setError('')
    try { await deleteAPIToken(publicID, status.csrf_token); await queryClient.invalidateQueries({ queryKey: ['api-tokens'] }) }
    catch (reason) { setError(messageFor(reason, '无法删除 API token')) } finally { setBusy('') }
  }

  return <AppFrame active="api">
    <main className="main-workspace operations-workspace">
      <header className="workspace-header operations-header"><div><p className="eyebrow">Read-only integration</p><h1>API token</h1></div><div className="header-actions"><button className="secondary-button api-link-button" type="button" onClick={() => setBrowserLinkToken('')}><Link2 size={17} /> 生成浏览器链接</button><button className="primary-button" type="button" onClick={() => setOpen(true)}><Plus size={17} /> 创建 token</button><button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}><LogOut size={17} />{loggingOut ? '正在退出' : '退出登录'}</button></div></header>
      {(logoutError || error) && <p className="operation-notice is-error" role="alert">{logoutError || error}</p>}
      <section className="api-summary-band"><div><span className="overview-icon"><KeyRound size={22} /></span><strong>{active}</strong><small>有效 token</small></div><div><strong>{tokens.length - active}</strong><small>已撤销或过期</small></div><p>外部 API 仅提供读取能力；每个 token 必须同时具备明确 scope 和账号、分组或全部账号范围。</p></section>
      <section className="operations-section"><div className="section-commandbar"><div><strong>访问凭据</strong><span>完整 token 只在创建时显示一次，服务端仅保存 SHA-256 哈希。</span></div></div><div className="operations-table api-token-table">
        <div className="operations-table-head"><span>名称</span><span>权限</span><span>范围</span><span>最近使用</span><span>操作</span></div>
        {tokensQuery.isPending ? <Empty text="正在读取 API token" loading /> : tokens.length === 0 ? <Empty text="尚未创建 API token" /> : tokens.map((token) => {
          const inactive = Boolean(token.revoked_at || token.expires_at && new Date(token.expires_at) <= new Date())
          const scopeSummary = token.all_accounts ? '全部账号' : `${token.account_public_ids.length} 个账号${token.group_names.length ? ` · ${token.group_names.join('、')}` : ''}`
          return <div className="operations-row" key={token.public_id}><div className="operation-identity"><strong>{token.name}</strong><span>{token.prefix}…</span></div><div className="scope-list">{token.scopes.map((scope) => <span key={scope}>{scope}</span>)}</div><small className="condition-summary">{scopeSummary}{token.ip_cidrs.length ? ` · ${token.ip_cidrs.join('、')}` : ''}</small><div><strong>{token.last_used_at ? formatDate(token.last_used_at) : '尚未使用'}</strong><small className="cell-note">{inactive ? token.revoked_at ? '已撤销' : '已过期' : token.expires_at ? `到期 ${formatDate(token.expires_at)}` : '长期有效'}</small></div><div className="row-actions">{!inactive ? <button className="icon-command danger-command" type="button" title="撤销 token" aria-label="撤销 token" disabled={busy !== ''} onClick={() => void revoke(token.public_id)}>{busy === token.public_id ? <LoaderCircle className="is-spinning" size={16} /> : <ShieldOff size={16} />}</button> : <button className="icon-command danger-command" type="button" title="永久删除 token" aria-label="永久删除 token" disabled={busy !== ''} onClick={() => void remove(token.public_id)}>{busy === token.public_id ? <LoaderCircle className="is-spinning" size={16} /> : <Trash2 size={16} />}</button>}</div></div>
        })}
      </div></section>
      <APITutorial accounts={accountsQuery.data?.accounts ?? []} />
    </main>
    {open && <TokenDialog accounts={accountsQuery.data?.accounts ?? []} csrfToken={status.csrf_token ?? ''} onClose={() => setOpen(false)} onCreated={async (token) => { setOpen(false); setCreated(token); await queryClient.invalidateQueries({ queryKey: ['api-tokens'] }) }} />}
    {created && <SecretDialog token={created} onClose={() => setCreated(null)} onGenerate={() => { setBrowserLinkToken(created.secret); setCreated(null) }} />}
    {browserLinkToken !== null && <BrowserLinkDialog accounts={accountsQuery.data?.accounts ?? []} initialToken={browserLinkToken} onClose={() => setBrowserLinkToken(null)} />}
  </AppFrame>
}

function APITutorial({ accounts }: { accounts: Awaited<ReturnType<typeof fetchAccounts>>['accounts'] }) {
  const [account, setAccount] = useState('')
  const [sender, setSender] = useState('no-reply@example.com')
  const [subject, setSubject] = useState('verification code')
  const [copied, setCopied] = useState('')

  useEffect(() => {
    if (!account && accounts.length > 0) setAccount(accounts[0].primary_email || accounts[0].imported_email || accounts[0].public_id)
  }, [account, accounts])

  const examples = useMemo(() => buildOTPExamples({
    baseURL: window.location.origin,
    account: account || 'account@example.com',
    sender,
    subject,
  }), [account, sender, subject])

  async function copyExample(id: string, value: string) {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(id)
      window.setTimeout(() => setCopied((current) => current === id ? '' : current), 1500)
    } catch {
      setCopied('')
    }
  }

  return <section className="operations-section api-guide" aria-labelledby="api-guide-title">
    <div className="section-commandbar"><div><strong id="api-guide-title">API 使用教程</strong><span>浏览器直开、C# 与服务端程序调用的完整说明。</span></div><BookOpen size={19} /></div>

    <div className="api-guide-section api-guide-steps">
      <h2>接入步骤</h2>
      <ol>
        <li><strong>1</strong><span>点击页面右上角“创建 token”，填写仅供自己识别的名称。</span></li>
        <li><strong>2</strong><span>调用验证码接口至少选择 <code>otp:read</code>；查账号状态再选 <code>accounts:read</code>。</span></li>
        <li><strong>3</strong><span>绑定允许访问的账号、分组或全部账号。未在范围内的账号会与不存在的账号一样返回 404。</span></li>
        <li><strong>4</strong><span>程序调用使用 <code>Authorization: Bearer &lt;token&gt;</code>；人工查看可点击“生成浏览器链接”，使用 <code>access_token</code> 参数。</span></li>
      </ol>
      <div className="api-security-note"><ShieldCheck size={18} /><div><strong>程序接入仍应优先使用 Bearer 请求头</strong><span>浏览器链接会把 token 留在历史记录或代理日志中，只适合人工查看。生产环境必须使用 HTTPS，建议设置到期时间和允许 IP；泄露后立即撤销。</span></div></div>
    </div>

    <div className="api-guide-section">
      <h2>验证码请求生成器</h2>
      <div className="api-example-inputs">
        <label><span>账号</span><select value={account} onChange={(event) => setAccount(event.target.value)}>{accounts.length === 0 ? <option value="">account@example.com</option> : accounts.map((item) => <option key={item.public_id} value={item.primary_email || item.imported_email || item.public_id}>{item.display_name ? `${item.display_name} · ${item.primary_email || item.imported_email}` : item.primary_email || item.imported_email}</option>)}</select></label>
        <label><span><code>sender</code> (可选)</span><input value={sender} onChange={(event) => setSender(event.target.value)} spellCheck={false} /></label>
        <label><span><code>subject</code> (可选)</span><input value={subject} onChange={(event) => setSubject(event.target.value)} /></label>
      </div>
      <p className="api-parameter-note"><code>wait_seconds=30</code> 会将该账号加入高优先级同步并最多等待 30 秒；接口固定查询请求开始时间前 15 分钟内的验证码，只返回最新一封。</p>
      <div className="api-code-grid">
        <CodeExample title="cURL" value={examples.curl} copied={copied === 'curl'} onCopy={() => void copyExample('curl', examples.curl)} />
        <CodeExample title="PowerShell" value={examples.powershell} copied={copied === 'powershell'} onCopy={() => void copyExample('powershell', examples.powershell)} />
        <CodeExample title="Node.js" value={examples.node} copied={copied === 'node'} onCopy={() => void copyExample('node', examples.node)} />
        <CodeExample title="C#" value={examples.csharp} copied={copied === 'csharp'} onCopy={() => void copyExample('csharp', examples.csharp)} />
      </div>
    </div>

    <div className="api-guide-section">
      <h2>接口与参数</h2>
      <div className="api-endpoint-reference">
        <section><header><code>GET /api/v1/accounts</code><span>accounts:read</span></header><p>返回 token 授权范围内的账号、邮箱、分组、标签和同步状态，无额外参数。</p></section>
        <section><header><code>GET /api/v1/messages</code><span>mail:read</span></header><p>返回当前本地索引快照；<code>q</code> 搜索关键词；<code>account</code> 接受账号公开 ID 或邮箱；<code>group</code>、<code>tag</code>、<code>sender</code> 用于筛选；<code>category</code> 接受 important、verification、marketing、spam、normal、uncertain；<code>folder</code> 接受 inbox、junkemail；<code>unread</code> 接受 true、false；<code>limit</code> 为 1–100；下一页传回响应中的 <code>next_cursor</code>。</p></section>
        <section><header><code>GET /api/v1/messages/{'{public_id}'}</code><span>mail:read</span></header><p>读取一封邮件的安全纯文本正文。<code>public_id</code> 来自邮件列表响应；无权访问与不存在均返回 404。</p></section>
        <section><header><code>GET /api/v1/otp/latest</code><span>otp:read</span></header><p><code>account</code> 必填；接口固定查询请求开始时间前 15 分钟内的验证码并返回最新一封；<code>wait_seconds</code> 为 0–30；<code>sender</code> 精确匹配发件人邮箱；<code>subject</code> 按主题包含文本筛选。</p></section>
        <section><header><code>GET /api/v1/health</code><span>system:read</span></header><p>返回脱敏的数据库、备份、队列、磁盘和账号状态，无额外参数。</p></section>
      </div>
      <p className="api-parameter-note">浏览器链接在以上参数之外增加 <code>access_token=omm_...</code>。所有参数都应通过链接生成器或标准 URL 编码，不要手工拼接含空格、加号或中文的值。</p>
    </div>

    <div className="api-guide-section api-reference-grid">
      <div><h2>验证码响应字段</h2><dl className="api-field-list"><dt><code>code</code></dt><dd>提取到的验证码；最近 15 分钟没有验证码时为 <code>null</code>。</dd><dt><code>fresh</code></dt><dd>本次等待期内账号同步成功并找到时间窗口内邮件时为 <code>true</code>。</dd><dt><code>received_at</code></dt><dd>邮件在 Microsoft 端的收件时间。</dd><dt><code>synced_at</code></dt><dd>该账号最后一次成功写入本地索引的时间。</dd><dt><code>account_status</code></dt><dd>账号当前授权/同步状态。</dd><dt><code>retry_after_seconds</code></dt><dd>同步失败或受限流时建议等待的秒数。</dd></dl></div>
      <div><h2>常见问题</h2><dl className="api-field-list"><dt>401</dt><dd>Bearer 或 <code>access_token</code> 缺失、错误、已撤销或已过期。</dd><dt>404</dt><dd>账号或邮件不存在，或 token 没有对应账号、分组或全部账号访问权。</dd><dt>429</dt><dd>请求过快；降低调用频率并按 <code>Retry-After</code> 重试。</dd><dt>无新验证码</dt><dd><code>code</code> 为 <code>null</code> 是正常空结果。检查最近 15 分钟内是否有验证码，以及发件人和主题条件。</dd><dt>账号同步失败</dt><dd>查看 <code>account_status</code> 和 <code>retry_after_seconds</code>；如为 <code>reauth_required</code>，先在账号管理重新授权。</dd></dl></div>
    </div>
  </section>
}

function CodeExample({ title, value, copied, onCopy }: { title: string; value: string; copied: boolean; onCopy: () => void }) {
  return <section className="api-code-example"><header><strong>{title}</strong><button className="icon-command" type="button" title={`复制 ${title} 示例`} aria-label={`复制 ${title} 示例`} onClick={onCopy}>{copied ? <Check size={15} /> : <Clipboard size={15} />}</button></header><pre><code>{value}</code></pre></section>
}

const browserEndpointOptions: Array<{ value: BrowserAPIEndpoint; label: string; scope: APIScope }> = [
  { value: 'accounts', label: '账号列表', scope: 'accounts:read' },
  { value: 'messages', label: '邮件列表', scope: 'mail:read' },
  { value: 'message', label: '单封邮件正文', scope: 'mail:read' },
  { value: 'otp', label: '最新验证码', scope: 'otp:read' },
  { value: 'health', label: '系统健康', scope: 'system:read' },
]

function BrowserLinkDialog({ accounts, initialToken, onClose }: {
  accounts: Awaited<ReturnType<typeof fetchAccounts>>['accounts']; initialToken: string; onClose: () => void
}) {
  const [endpoint, setEndpoint] = useState<BrowserAPIEndpoint>('accounts')
  const [token, setToken] = useState(initialToken)
  const [params, setParams] = useState<Record<string, string>>({
    wait_seconds: '30', limit: '20',
  })
  const [copied, setCopied] = useState(false)
  const link = useMemo(() => buildBrowserAPILink({ baseURL: window.location.origin, endpoint, token, params }), [endpoint, token, params])
  const selectedEndpoint = browserEndpointOptions.find((item) => item.value === endpoint) ?? browserEndpointOptions[0]
  const setParam = (name: string, value: string) => setParams((current) => ({ ...current, [name]: value }))

  async function copyLink() {
    const success = await copyBrowserAPILink(link)
    setCopied(success)
    if (success) window.setTimeout(() => setCopied(false), 1500)
  }

  function openLink() {
    openBrowserAPILink(link)
  }

  return <div className="dialog-backdrop"><section className="dialog-panel operation-dialog browser-link-dialog" role="dialog" aria-modal="true" aria-labelledby="browser-link-title">
    <div className="dialog-heading"><div><p className="eyebrow">Direct browser access</p><h2 id="browser-link-title">生成浏览器 API 链接</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button></div>
    <div className="api-link-security-note"><ShieldCheck size={18} /><div><strong>token 仅保留在当前弹窗</strong><span>浏览器历史和 Nginx 等代理日志可能记录完整网址；生产环境请使用 HTTPS，并限制 token 的到期时间和允许 IP。</span></div></div>
    <div className="form-grid two-columns browser-link-fields">
      <label><span>接口</span><select value={endpoint} onChange={(event) => { setEndpoint(event.target.value as BrowserAPIEndpoint); setCopied(false) }}>{browserEndpointOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select></label>
      <label><span>所需权限</span><input value={selectedEndpoint.scope} readOnly /></label>
      <label className="wide-field"><span>API token</span><input type="password" autoComplete="off" spellCheck={false} value={token} onChange={(event) => { setToken(event.target.value); setCopied(false) }} placeholder="omm_..." /></label>
      {endpoint === 'messages' && <>
        <label><span>关键词 <code>q</code></span><input value={params.q ?? ''} onChange={(event) => setParam('q', event.target.value)} /></label>
        <label><span>账号 <code>account</code></span><input list="browser-api-accounts" value={params.account ?? ''} onChange={(event) => setParam('account', event.target.value)} /></label>
        <label><span>分组 <code>group</code></span><input value={params.group ?? ''} onChange={(event) => setParam('group', event.target.value)} /></label>
        <label><span>标签 <code>tag</code></span><input value={params.tag ?? ''} onChange={(event) => setParam('tag', event.target.value)} /></label>
        <label><span>分类 <code>category</code></span><select value={params.category ?? ''} onChange={(event) => setParam('category', event.target.value)}><option value="">全部</option><option value="important">重要</option><option value="verification">验证码</option><option value="marketing">营销</option><option value="spam">垃圾</option><option value="normal">普通</option><option value="uncertain">待确认</option></select></label>
        <label><span>目录 <code>folder</code></span><select value={params.folder ?? ''} onChange={(event) => setParam('folder', event.target.value)}><option value="">全部</option><option value="inbox">收件箱</option><option value="junkemail">垃圾邮件</option></select></label>
        <label><span>发件人 <code>sender</code></span><input value={params.sender ?? ''} onChange={(event) => setParam('sender', event.target.value)} /></label>
        <label><span>未读 <code>unread</code></span><select value={params.unread ?? ''} onChange={(event) => setParam('unread', event.target.value)}><option value="">全部</option><option value="true">仅未读</option><option value="false">仅已读</option></select></label>
        <label><span>数量 <code>limit</code></span><input type="number" min="1" max="100" value={params.limit ?? ''} onChange={(event) => setParam('limit', event.target.value)} /></label>
        <label><span>分页游标 <code>cursor</code></span><input value={params.cursor ?? ''} onChange={(event) => setParam('cursor', event.target.value)} spellCheck={false} /></label>
      </>}
      {endpoint === 'message' && <label className="wide-field"><span>邮件公开 ID <code>public_id</code></span><input value={params.public_id ?? ''} onChange={(event) => setParam('public_id', event.target.value)} placeholder="msg_..." spellCheck={false} /></label>}
      {endpoint === 'otp' && <>
        <label><span>账号 <code>account</code></span><input list="browser-api-accounts" value={params.account ?? ''} onChange={(event) => setParam('account', event.target.value)} /></label>
        <label><span>等待秒数 <code>wait_seconds</code></span><input type="number" min="0" max="30" value={params.wait_seconds ?? ''} onChange={(event) => setParam('wait_seconds', event.target.value)} /></label>
        <label><span>发件人 <code>sender</code></span><input value={params.sender ?? ''} onChange={(event) => setParam('sender', event.target.value)} /></label>
        <label className="wide-field"><span>主题包含 <code>subject</code></span><input value={params.subject ?? ''} onChange={(event) => setParam('subject', event.target.value)} /></label>
      </>}
    </div>
    <datalist id="browser-api-accounts">{accounts.map((account) => <option key={account.public_id} value={account.primary_email || account.imported_email || account.public_id} />)}</datalist>
    <div className="browser-link-preview"><span>链接预览</span><code>{link ? maskBrowserAPILink(link) : '请填写 token 和当前接口的必填参数'}</code></div>
    <div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={() => void copyLink()} disabled={!link}>{copied ? <Check size={16} /> : <Clipboard size={16} />}{copied ? '已复制' : '复制链接'}</button><button className="primary-button" type="button" onClick={openLink} disabled={!link}><ExternalLink size={16} /> 新标签页打开</button></div>
  </section></div>
}

function TokenDialog({ accounts, csrfToken, onClose, onCreated }: { accounts: Awaited<ReturnType<typeof fetchAccounts>>['accounts']; csrfToken: string; onClose: () => void; onCreated: (token: CreatedAPIToken) => Promise<void> }) {
  const [name, setName] = useState(''); const [selectedScopes, setSelectedScopes] = useState<APIScope[]>(['mail:read']); const [accountScope, setAccountScope] = useState<'all' | 'selected'>('selected'); const [selectedAccounts, setSelectedAccounts] = useState<string[]>(accounts.slice(0, 1).map((a) => a.public_id)); const [groups, setGroups] = useState(''); const [cidrs, setCIDRs] = useState(''); const [expires, setExpires] = useState(''); const [saving, setSaving] = useState(false); const [error, setError] = useState('')
  const availableGroups = useMemo(() => [...new Set(accounts.flatMap((account) => account.groups))].sort(), [accounts])
  const selectedGroups = groups.split(',').map((item) => item.trim()).filter((item) => availableGroups.includes(item))
  const hasAccountScope = accountScope === 'all' || selectedAccounts.length + selectedGroups.length > 0
  async function save() { setSaving(true); setError(''); try { const token = await createAPIToken({ name, scopes: selectedScopes, all_accounts: accountScope === 'all', account_public_ids: accountScope === 'all' ? [] : selectedAccounts, group_names: accountScope === 'all' ? [] : selectedGroups, ip_cidrs: cidrs.split(/[\n,]/).map((item) => item.trim()).filter(Boolean), expires_at: expires ? new Date(expires).toISOString() : undefined }, csrfToken); await onCreated(token) } catch (reason) { setError(messageFor(reason, '无法创建 API token')); setSaving(false) } }
  return <div className="dialog-backdrop"><section className="dialog-panel operation-dialog token-dialog" role="dialog" aria-modal="true" aria-labelledby="token-title"><div className="dialog-heading"><div><p className="eyebrow">Scoped access</p><h2 id="token-title">创建 API token</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button></div>
    <div className="form-grid two-columns"><label className="wide-field"><span>名称</span><input value={name} onChange={(event) => setName(event.target.value)} /></label><fieldset className="wide-field checkbox-group"><legend>只读权限</legend>{scopes.map((scope) => <label key={scope.id}><input type="checkbox" checked={selectedScopes.includes(scope.id)} onChange={(event) => setSelectedScopes((current) => event.target.checked ? [...current, scope.id] : current.filter((item) => item !== scope.id))} /><span>{scope.label}</span><code>{scope.id}</code></label>)}</fieldset><fieldset className="wide-field checkbox-group account-scope-group"><legend>账号范围</legend><label><input type="radio" name="api-account-scope" checked={accountScope === 'all'} onChange={() => { setAccountScope('all'); setSelectedAccounts([]); setGroups('') }} /><span>全部账号</span><small className="cell-note">包含以后新增的账号</small></label><label><input type="radio" name="api-account-scope" checked={accountScope === 'selected'} onChange={() => setAccountScope('selected')} /><span>指定账号或分组</span></label>{accountScope === 'selected' && <>{accounts.length === 0 ? <span className="muted-value">尚无账号</span> : accounts.map((account) => <label key={account.public_id}><input type="checkbox" checked={selectedAccounts.includes(account.public_id)} onChange={(event) => setSelectedAccounts((current) => event.target.checked ? [...current, account.public_id] : current.filter((item) => item !== account.public_id))} /><span>{account.display_name || account.primary_email || account.imported_email}</span></label>)}<label className="api-group-field"><span>分组范围（逗号分隔）</span><input list="api-groups" value={groups} onChange={(event) => setGroups(event.target.value)} /><datalist id="api-groups">{availableGroups.map((group) => <option key={group} value={group} />)}</datalist></label></>}</fieldset><label><span>到期时间（可选）</span><input type="datetime-local" value={expires} onChange={(event) => setExpires(event.target.value)} /></label><label className="wide-field"><span>允许的 IP/CIDR（每行一个，可选）</span><textarea rows={3} value={cidrs} onChange={(event) => setCIDRs(event.target.value)} placeholder="203.0.113.10/32" /></label></div>
    {error && <p className="form-error">{error}</p>}<div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={saving || !name.trim() || selectedScopes.length === 0 || !hasAccountScope} onClick={() => void save()}>{saving ? <LoaderCircle className="is-spinning" size={16} /> : <Check size={16} />} 创建 token</button></div>
  </section></div>
}

function SecretDialog({ token, onClose, onGenerate }: { token: CreatedAPIToken; onClose: () => void; onGenerate: () => void }) {
  const [copied, setCopied] = useState(false)
  async function copy() { try { await navigator.clipboard.writeText(token.secret); setCopied(true) } catch { setCopied(false) } }
  return <div className="dialog-backdrop"><section className="dialog-panel secret-dialog" role="dialog" aria-modal="true" aria-labelledby="secret-title"><div className="dialog-heading"><div><p className="eyebrow">Shown once</p><h2 id="secret-title">保存 API token</h2></div></div><p className="dialog-copy">关闭后无法再次查看完整 token。丢失时请撤销并重新创建。</p><code className="secret-value">{token.secret}</code><div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={() => void copy()}>{copied ? <Check size={16} /> : <Clipboard size={16} />}{copied ? '已复制' : '复制 token'}</button><button className="secondary-button" type="button" onClick={onGenerate}><Link2 size={16} /> 用于生成链接</button><button className="primary-button" type="button" onClick={onClose}>我已保存</button></div></section></div>
}

function Empty({ text, loading = false }: { text: string; loading?: boolean }) { return <div className="operations-empty">{loading && <LoaderCircle className="is-spinning" size={18} />}{text}</div> }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(new Date(value)) }
function messageFor(error: unknown, fallback: string) { return error instanceof APIError ? error.message : fallback }

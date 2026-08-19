import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { BellRing, Check, LoaderCircle, LogOut, Plus, RefreshCw, Send, Trash2, X } from 'lucide-react'

import { APIError, type AuthStatus } from '../api/auth'
import {
  createNotificationChannel,
  createNotificationRule,
  deleteNotificationChannel,
  deleteNotificationRule,
  fetchNotificationChannels,
  fetchNotificationDeliveries,
  fetchNotificationRules,
  retryNotificationDelivery,
  testNotificationChannel,
  updateNotificationRule,
  type NotificationChannel,
  type NotificationChannelInput,
  type NotificationKind,
  type NotificationRule,
} from '../api/notifications'
import { AppFrame } from '../components/AppFrame'

type View = 'channels' | 'rules' | 'deliveries'

export function notificationRuleStart(channelCount: number) {
  return channelCount === 0
    ? { target: 'channel' as const, label: '先新建通知通道' }
    : { target: 'rule' as const, label: '新建规则' }
}

export function NotificationsDashboard({ status, onLogout, loggingOut, logoutError }: {
  status: AuthStatus
  onLogout: () => void
  loggingOut: boolean
  logoutError: string
}) {
  const queryClient = useQueryClient()
  const [view, setView] = useState<View>(() => window.location.hash === '#notifications-rules' ? 'rules' : 'channels')
  const [channelOpen, setChannelOpen] = useState(false)
  const [ruleOpen, setRuleOpen] = useState(false)
  const [continueWithRule, setContinueWithRule] = useState(false)
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const channelsQuery = useQuery({ queryKey: ['notification-channels'], queryFn: ({ signal }) => fetchNotificationChannels(signal) })
  const rulesQuery = useQuery({ queryKey: ['notification-rules'], queryFn: ({ signal }) => fetchNotificationRules(signal) })
  const deliveriesQuery = useQuery({ queryKey: ['notification-deliveries'], queryFn: ({ signal }) => fetchNotificationDeliveries(signal), refetchInterval: 30_000 })
  const channels = channelsQuery.data?.channels ?? []
  const rules = rulesQuery.data?.rules ?? []
  const deliveries = deliveriesQuery.data?.deliveries ?? []
  const failedCount = deliveries.filter((item) => item.status === 'failed').length
  const ruleStart = notificationRuleStart(channels.length)

  function beginRuleCreation() {
    if (ruleStart.target === 'channel') {
      setContinueWithRule(true)
      setChannelOpen(true)
      return
    }
    setRuleOpen(true)
  }

  async function testChannel(publicID: string) {
    if (!status.csrf_token) return
    setBusy(publicID); setError(''); setNotice('')
    try {
      const result = await testNotificationChannel(publicID, status.csrf_token)
      setNotice(result.status === 'sent' ? '测试通知已送达' : '测试通知已进入投递队列')
      await queryClient.invalidateQueries({ queryKey: ['notification-deliveries'] })
    } catch (reason) { setError(messageFor(reason, '测试通知发送失败')) } finally { setBusy('') }
  }

  async function removeChannel(publicID: string) {
    if (!status.csrf_token || !window.confirm('删除此通知通道及其规则和投递记录？')) return
    setBusy(publicID); setError('')
    try {
      await deleteNotificationChannel(publicID, status.csrf_token)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['notification-channels'] }),
        queryClient.invalidateQueries({ queryKey: ['notification-rules'] }),
        queryClient.invalidateQueries({ queryKey: ['notification-deliveries'] }),
      ])
    } catch (reason) { setError(messageFor(reason, '无法删除通知通道')) } finally { setBusy('') }
  }

  async function updateRule(rule: NotificationRule, changes: Partial<NotificationRule>) {
    if (!status.csrf_token || !rule.public_id) return
    setBusy(rule.public_id); setError('')
    try {
      await updateNotificationRule(rule.public_id, { ...rule, ...changes }, status.csrf_token)
      await queryClient.invalidateQueries({ queryKey: ['notification-rules'] })
    } catch (reason) { setError(messageFor(reason, '无法更新通知规则')) } finally { setBusy('') }
  }

  async function removeRule(rule: NotificationRule) {
    if (!status.csrf_token || !rule.public_id) return
    setBusy(rule.public_id); setError('')
    try {
      await deleteNotificationRule(rule.public_id, status.csrf_token)
      await queryClient.invalidateQueries({ queryKey: ['notification-rules'] })
    } catch (reason) { setError(messageFor(reason, '无法删除通知规则')) } finally { setBusy('') }
  }

  async function retry(publicID: string) {
    if (!status.csrf_token) return
    setBusy(publicID); setError('')
    try {
      await retryNotificationDelivery(publicID, status.csrf_token)
      setNotice('失败记录已重新加入投递队列')
      await queryClient.invalidateQueries({ queryKey: ['notification-deliveries'] })
    } catch (reason) { setError(messageFor(reason, '无法重试投递')) } finally { setBusy('') }
  }

  return <AppFrame active="notifications">
    <main className="main-workspace operations-workspace">
      <header className="workspace-header operations-header"><div><p className="eyebrow">Delivery control</p><h1>通知中心</h1></div><div className="header-actions">
        {view === 'channels' && <button className="primary-button" type="button" onClick={() => setChannelOpen(true)}><Plus size={17} /> 新建通道</button>}
        {view === 'rules' && <button className="primary-button" type="button" onClick={beginRuleCreation} title={ruleStart.target === 'channel' ? '创建规则前需要先添加通知通道' : '新建通知规则'}><Plus size={17} /> {ruleStart.label}</button>}
        <button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}><LogOut size={17} />{loggingOut ? '正在退出' : '退出登录'}</button>
      </div></header>
      {(logoutError || error || notice) && <p className={`operation-notice ${logoutError || error ? 'is-error' : ''}`} role="status">{logoutError || error || notice}</p>}
      <div className="operations-tabs" role="tablist" aria-label="通知中心视图">
        <button type="button" className={view === 'channels' ? 'is-selected' : ''} onClick={() => setView('channels')}><BellRing size={16} /> 通道 <span>{channels.length}</span></button>
        <button type="button" className={view === 'rules' ? 'is-selected' : ''} onClick={() => setView('rules')}><Send size={16} /> 规则 <span>{rules.length}</span></button>
        <button type="button" className={view === 'deliveries' ? 'is-selected' : ''} onClick={() => setView('deliveries')}><RefreshCw size={16} /> 投递记录 <span>{failedCount ? `${failedCount} 失败` : deliveries.length}</span></button>
      </div>

      {view === 'channels' && <section className="operations-section"><div className="section-commandbar"><div><strong>通知通道</strong><span>凭据加密保存；列表仅显示脱敏目标。</span></div></div><div className="operations-table notification-table">
        <div className="operations-table-head"><span>通道</span><span>类型</span><span>目标</span><span>系统告警</span><span>操作</span></div>
        {channelsQuery.isPending ? <Empty text="正在读取通知通道" loading /> : channels.length === 0 ? <Empty text="尚未配置通知通道" /> : channels.map((channel) => <div className="operations-row" key={channel.public_id}>
          <div className="operation-identity"><strong>{channel.name}</strong><span>{channel.enabled ? '已启用' : '已停用'}</span></div><span className={`channel-kind kind-${channel.kind}`}>{kindLabel(channel.kind)}</span><strong>{channel.destination}</strong><span>{channel.system_enabled ? '接收' : '不接收'}</span>
          <div className="row-actions"><button className="icon-command" type="button" title="发送测试通知" aria-label="发送测试通知" disabled={busy !== '' || !channel.enabled} onClick={() => void testChannel(channel.public_id)}>{busy === channel.public_id ? <LoaderCircle className="is-spinning" size={16} /> : <Send size={16} />}</button><button className="icon-command danger-command" type="button" title="删除通道" aria-label="删除通道" disabled={busy !== ''} onClick={() => void removeChannel(channel.public_id)}><Trash2 size={16} /></button></div>
        </div>)}</div></section>}

      {view === 'rules' && <section className="operations-section"><div className="section-commandbar"><div><strong>匹配规则</strong><span>单条规则内条件为 AND，多条启用规则之间为 OR。</span></div></div><div className="operations-table notification-rules-table">
        <div className="operations-table-head"><span>规则</span><span>通道</span><span>条件</span><span>状态</span><span>操作</span></div>
        {rulesQuery.isPending ? <Empty text="正在读取通知规则" loading /> : rules.length === 0 ? <Empty text="尚未创建通知规则" /> : rules.map((rule) => <div className="operations-row" key={rule.public_id}>
          <div className="operation-identity"><strong>{rule.name}</strong><span>{rule.require_otp ? '必须包含验证码' : '邮件通知'}</span></div><strong>{rule.channel_name}</strong><small className="condition-summary">{ruleSummary(rule)}</small><label className="inline-switch"><input type="checkbox" checked={rule.enabled} disabled={busy !== ''} onChange={(event) => void updateRule(rule, { enabled: event.target.checked })} /><span>{rule.enabled ? '启用' : '停用'}</span></label><div className="row-actions"><button className="icon-command danger-command" type="button" title="删除规则" aria-label="删除规则" disabled={busy !== ''} onClick={() => void removeRule(rule)}><Trash2 size={16} /></button></div>
        </div>)}</div></section>}

      {view === 'deliveries' && <section className="operations-section"><div className="section-commandbar"><div><strong>最近投递</strong><span>失败记录保留错误代码和有限重试次数。</span></div><button className="icon-command" type="button" title="刷新投递记录" aria-label="刷新投递记录" onClick={() => void deliveriesQuery.refetch()}><RefreshCw size={16} /></button></div><div className="operations-table delivery-table">
        <div className="operations-table-head"><span>时间</span><span>通道</span><span>事件</span><span>状态</span><span>操作</span></div>
        {deliveriesQuery.isPending ? <Empty text="正在读取投递记录" loading /> : deliveries.length === 0 ? <Empty text="尚无投递记录" /> : deliveries.map((delivery) => <div className="operations-row" key={delivery.public_id}>
          <time>{formatDate(delivery.created_at)}</time><strong>{delivery.channel_name}</strong><span>{delivery.event_type}</span><div><span className={`state-pill state-${delivery.status}`}>{delivery.status}</span><small className="cell-note">{delivery.last_error || `尝试 ${delivery.attempt_count} 次`}</small></div><div className="row-actions">{delivery.status === 'failed' && <button className="icon-command" type="button" title="重试投递" aria-label="重试投递" disabled={busy !== ''} onClick={() => void retry(delivery.public_id)}>{busy === delivery.public_id ? <LoaderCircle className="is-spinning" size={16} /> : <RefreshCw size={16} />}</button>}</div>
        </div>)}</div></section>}
    </main>
    {channelOpen && <ChannelDialog csrfToken={status.csrf_token ?? ''} onClose={() => { setChannelOpen(false); setContinueWithRule(false) }} onCreated={async (channel) => {
      setChannelOpen(false)
      queryClient.setQueryData<{ channels: NotificationChannel[] }>(['notification-channels'], (current) => ({
        channels: [...(current?.channels ?? []), channel],
      }))
      if (continueWithRule) {
        setContinueWithRule(false)
        setRuleOpen(true)
      }
    }} />}
    {ruleOpen && <RuleDialog channels={channels} csrfToken={status.csrf_token ?? ''} onClose={() => setRuleOpen(false)} onCreated={async () => { setRuleOpen(false); await queryClient.invalidateQueries({ queryKey: ['notification-rules'] }) }} />}
  </AppFrame>
}

function ChannelDialog({ csrfToken, onClose, onCreated }: { csrfToken: string; onClose: () => void; onCreated: (channel: NotificationChannel) => Promise<void> }) {
  const [input, setInput] = useState<NotificationChannelInput>({ name: '', kind: 'telegram', enabled: true, system_enabled: true })
  const [saving, setSaving] = useState(false); const [error, setError] = useState('')
  function set<K extends keyof NotificationChannelInput>(key: K, value: NotificationChannelInput[K]) { setInput((current) => ({ ...current, [key]: value })) }
  async function save() { setSaving(true); setError(''); try { const channel = await createNotificationChannel(input, csrfToken); await onCreated(channel) } catch (reason) { setError(messageFor(reason, '无法创建通知通道')); setSaving(false) } }
  return <div className="dialog-backdrop"><section className="dialog-panel operation-dialog" role="dialog" aria-modal="true" aria-labelledby="channel-title"><div className="dialog-heading"><div><p className="eyebrow">Notification channel</p><h2 id="channel-title">新建通知通道</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button></div>
    <div className="form-grid two-columns"><label><span>名称</span><input value={input.name} onChange={(event) => set('name', event.target.value)} /></label><label><span>类型</span><select value={input.kind} onChange={(event) => set('kind', event.target.value as NotificationKind)}><option value="telegram">Telegram</option><option value="pushplus">PushPlus</option><option value="webhook">HMAC Webhook</option></select></label>
      {input.kind === 'telegram' && <><label className="wide-field"><span>Bot token</span><input type="password" autoComplete="off" value={input.telegram_bot_token ?? ''} onChange={(event) => set('telegram_bot_token', event.target.value)} /></label><label className="wide-field"><span>Chat ID</span><input value={input.telegram_chat_id ?? ''} onChange={(event) => set('telegram_chat_id', event.target.value)} /></label></>}
      {input.kind === 'pushplus' && <><label className="wide-field"><span>PushPlus token</span><input type="password" autoComplete="off" value={input.pushplus_token ?? ''} onChange={(event) => set('pushplus_token', event.target.value)} /></label><label className="wide-field"><span>群组编码（可选）</span><input value={input.pushplus_topic ?? ''} onChange={(event) => set('pushplus_topic', event.target.value)} /></label></>}
      {input.kind === 'webhook' && <><label className="wide-field"><span>Webhook URL</span><input inputMode="url" value={input.webhook_url ?? ''} onChange={(event) => set('webhook_url', event.target.value)} /></label><label className="wide-field"><span>HMAC secret</span><input type="password" autoComplete="off" value={input.webhook_secret ?? ''} onChange={(event) => set('webhook_secret', event.target.value)} /></label></>}
      <label className="switch-field"><input type="checkbox" checked={input.enabled} onChange={(event) => set('enabled', event.target.checked)} /><span>启用邮件通知</span></label><label className="switch-field"><input type="checkbox" checked={input.system_enabled} onChange={(event) => set('system_enabled', event.target.checked)} /><span>接收系统告警</span></label></div>
    {error && <p className="form-error">{error}</p>}<div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={saving || !input.name.trim()} onClick={() => void save()}>{saving ? <LoaderCircle className="is-spinning" size={16} /> : <Check size={16} />} 创建通道</button></div>
  </section></div>
}

function RuleDialog({ channels, csrfToken, onClose, onCreated }: { channels: Array<{ public_id: string; name: string }>; csrfToken: string; onClose: () => void; onCreated: () => Promise<void> }) {
  const [name, setName] = useState(''); const [channel, setChannel] = useState(channels[0]?.public_id ?? ''); const [categories, setCategories] = useState('important,verification'); const [sender, setSender] = useState(''); const [domain, setDomain] = useState(''); const [keywords, setKeywords] = useState(''); const [accounts, setAccounts] = useState(''); const [groups, setGroups] = useState(''); const [tags, setTags] = useState(''); const [requireOTP, setRequireOTP] = useState(false); const [includeOTP, setIncludeOTP] = useState(false); const [saving, setSaving] = useState(false); const [error, setError] = useState('')
  const split = (value: string) => value.split(',').map((item) => item.trim()).filter(Boolean)
  async function save() { setSaving(true); setError(''); try { await createNotificationRule({ channel_public_id: channel, name, enabled: true, account_public_ids: split(accounts), group_names: split(groups), tag_names: split(tags), categories: split(categories), sender_address: sender.trim(), sender_domain: domain.trim(), subject_keywords: split(keywords), start_minute: -1, end_minute: -1, require_otp: requireOTP, include_otp: includeOTP }, csrfToken); await onCreated() } catch (reason) { setError(messageFor(reason, '无法创建通知规则')); setSaving(false) } }
  return <div className="dialog-backdrop"><section className="dialog-panel operation-dialog" role="dialog" aria-modal="true" aria-labelledby="notification-rule-title"><div className="dialog-heading"><div><p className="eyebrow">Notification rule</p><h2 id="notification-rule-title">新建通知规则</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button></div>
    <div className="form-grid two-columns"><label><span>规则名称</span><input value={name} onChange={(event) => setName(event.target.value)} /></label><label><span>通知通道</span><select value={channel} onChange={(event) => setChannel(event.target.value)}>{channels.map((item) => <option key={item.public_id} value={item.public_id}>{item.name}</option>)}</select></label><label className="wide-field"><span>分类（逗号分隔）</span><input value={categories} onChange={(event) => setCategories(event.target.value)} /></label><label><span>发件人地址</span><input value={sender} onChange={(event) => setSender(event.target.value)} /></label><label><span>发件域名</span><input value={domain} onChange={(event) => setDomain(event.target.value)} /></label><label className="wide-field"><span>主题关键词（逗号分隔）</span><input value={keywords} onChange={(event) => setKeywords(event.target.value)} /></label><label><span>账号公开 ID</span><input value={accounts} onChange={(event) => setAccounts(event.target.value)} /></label><label><span>分组</span><input value={groups} onChange={(event) => setGroups(event.target.value)} /></label><label className="wide-field"><span>标签</span><input value={tags} onChange={(event) => setTags(event.target.value)} /></label><label className="switch-field"><input type="checkbox" checked={requireOTP} onChange={(event) => setRequireOTP(event.target.checked)} /><span>只匹配含验证码邮件</span></label><label className="switch-field"><input type="checkbox" checked={includeOTP} onChange={(event) => setIncludeOTP(event.target.checked)} /><span>通知中包含提取出的验证码</span></label></div>
    {error && <p className="form-error">{error}</p>}<div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={saving || !name.trim() || !channel} onClick={() => void save()}>{saving ? <LoaderCircle className="is-spinning" size={16} /> : <Check size={16} />} 创建规则</button></div>
  </section></div>
}

function ruleSummary(rule: NotificationRule) {
  const parts = [rule.categories.join('/'), rule.sender_address, rule.sender_domain, rule.subject_keywords.join('、'), ...rule.group_names.map((v) => `组:${v}`), ...rule.tag_names.map((v) => `标签:${v}`)].filter(Boolean)
  return parts.length ? parts.join(' · ') : '所有新邮件'
}
function Empty({ text, loading = false }: { text: string; loading?: boolean }) { return <div className="operations-empty">{loading && <LoaderCircle className="is-spinning" size={18} />}{text}</div> }
function kindLabel(kind: NotificationKind) { return kind === 'telegram' ? 'Telegram' : kind === 'pushplus' ? 'PushPlus' : 'Webhook' }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(new Date(value)) }
function messageFor(error: unknown, fallback: string) { return error instanceof APIError ? error.message : fallback }

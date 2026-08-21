import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { BellRing, BookOpen, Check, ExternalLink, LoaderCircle, LogOut, Plus, RefreshCw, Send, Trash2, X } from 'lucide-react'

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
  testNotificationConfig,
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

export const notificationCategoryOptions = [
  { value: 'important', label: '重要' },
  { value: 'verification', label: '验证码' },
  { value: 'marketing', label: '营销' },
  { value: 'spam', label: '垃圾邮件' },
  { value: 'normal', label: '普通' },
] as const

const notificationCategoryLabels: Record<string, string> = {
  ...Object.fromEntries(notificationCategoryOptions.map((item) => [item.value, item.label])),
  uncertain: '待确认',
}

export function notificationCategoryLabel(value: string) {
  return notificationCategoryLabels[value] ?? value
}

export function notificationRuleSummary(rule: Pick<NotificationRule, 'categories' | 'personal_only' | 'sender_address' | 'sender_domain' | 'subject_keywords' | 'group_names' | 'tag_names'>) {
  const categories = rule.categories.map(notificationCategoryLabel)
  const parts = [
    ...categories,
    rule.personal_only ? '个性化邮件' : '',
    rule.sender_address,
    rule.sender_domain,
    rule.subject_keywords.join('、'),
    ...rule.group_names.map((value) => `组:${value}`),
    ...rule.tag_names.map((value) => `标签:${value}`),
  ].filter(Boolean)
  return parts.length ? parts.join(' · ') : '所有新邮件'
}

const wxPushCredentialKeys = ['wxpush_app_id', 'wxpush_app_secret', 'wxpush_user_id', 'wxpush_template_id'] as const

export function wxPushConfigFingerprint(input: NotificationChannelInput) {
  const value = wxPushCredentialKeys.map((key) => input[key]?.trim() ?? '').join('\u0000')
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(16).padStart(8, '0')
}

export function canCreateNotificationChannel(input: NotificationChannelInput, testedFingerprint: string) {
  if (!input.name.trim()) return false
  if (input.kind !== 'wxpush') return true
  if (wxPushCredentialKeys.some((key) => !input[key]?.trim())) return false
  return testedFingerprint !== '' && testedFingerprint === wxPushConfigFingerprint(input)
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
  const usableChannels = channels.filter((channel) => !channel.needs_reconfiguration)
  const rules = rulesQuery.data?.rules ?? []
  const deliveries = deliveriesQuery.data?.deliveries ?? []
  const failedCount = deliveries.filter((item) => item.status === 'failed').length
  const ruleStart = notificationRuleStart(usableChannels.length)

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
          <div className="operation-identity"><strong>{channel.name}</strong><span>{channel.needs_reconfiguration ? '旧版配置' : channel.enabled ? '已启用' : '已停用'}</span></div><span className={`channel-kind kind-${channel.kind}`}>{kindLabel(channel.kind)}</span><strong>{channel.destination}</strong><span>{channel.system_enabled ? '接收' : '不接收'}</span>
          <div className="row-actions">{channel.kind !== 'wxpush' && <button className="icon-command" type="button" title="发送测试通知" aria-label="发送测试通知" disabled={busy !== '' || !channel.enabled} onClick={() => void testChannel(channel.public_id)}>{busy === channel.public_id ? <LoaderCircle className="is-spinning" size={16} /> : <Send size={16} />}</button>}<button className="icon-command danger-command" type="button" title="删除通道" aria-label="删除通道" disabled={busy !== ''} onClick={() => void removeChannel(channel.public_id)}><Trash2 size={16} /></button></div>
        </div>)}</div></section>}

      {view === 'rules' && <section className="operations-section"><div className="section-commandbar"><div><strong>匹配规则</strong><span>单条规则内条件为 AND，多条启用规则之间为 OR。</span></div></div><div className="operations-table notification-rules-table">
        <div className="operations-table-head"><span>规则</span><span>通道</span><span>条件</span><span>状态</span><span>操作</span></div>
        {rulesQuery.isPending ? <Empty text="正在读取通知规则" loading /> : rules.length === 0 ? <Empty text="尚未创建通知规则" /> : rules.map((rule) => <div className="operations-row" key={rule.public_id}>
          <div className="operation-identity"><strong>{rule.name}</strong><span>{rule.require_otp ? '必须包含验证码' : '邮件通知'}</span></div><strong>{rule.channel_name}</strong><small className="condition-summary">{notificationRuleSummary(rule)}</small><label className="inline-switch"><input type="checkbox" checked={rule.enabled} disabled={busy !== ''} onChange={(event) => void updateRule(rule, { enabled: event.target.checked })} /><span>{rule.enabled ? '启用' : '停用'}</span></label><div className="row-actions"><button className="icon-command danger-command" type="button" title="删除规则" aria-label="删除规则" disabled={busy !== ''} onClick={() => void removeRule(rule)}><Trash2 size={16} /></button></div>
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
    {ruleOpen && <RuleDialog channels={usableChannels} csrfToken={status.csrf_token ?? ''} onClose={() => setRuleOpen(false)} onCreated={async () => { setRuleOpen(false); await queryClient.invalidateQueries({ queryKey: ['notification-rules'] }) }} />}
  </AppFrame>
}

function ChannelDialog({ csrfToken, onClose, onCreated }: { csrfToken: string; onClose: () => void; onCreated: (channel: NotificationChannel) => Promise<void> }) {
  const [input, setInput] = useState<NotificationChannelInput>({ name: '', kind: 'telegram', enabled: true, system_enabled: true })
  const [testedFingerprint, setTestedFingerprint] = useState('')
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false); const [error, setError] = useState('')
  function set<K extends keyof NotificationChannelInput>(key: K, value: NotificationChannelInput[K]) {
    if (key === 'kind' || wxPushCredentialKeys.includes(key as typeof wxPushCredentialKeys[number])) setTestedFingerprint('')
    setError('')
    setInput((current) => ({ ...current, [key]: value }))
  }
  async function testWXPush() {
    const fingerprint = wxPushConfigFingerprint(input)
    setTesting(true); setTestedFingerprint(''); setError('')
    try {
      await testNotificationConfig(input, csrfToken)
      setTestedFingerprint(fingerprint)
    } catch (reason) { setError(messageFor(reason, 'WXPush 配置测试失败')) } finally { setTesting(false) }
  }
  async function save() { setSaving(true); setError(''); try { const channel = await createNotificationChannel(input, csrfToken); await onCreated(channel) } catch (reason) { setError(messageFor(reason, '无法创建通知通道')); setSaving(false) } }
  return <div className="dialog-backdrop"><section className="dialog-panel operation-dialog" role="dialog" aria-modal="true" aria-labelledby="channel-title"><div className="dialog-heading"><div><p className="eyebrow">Notification channel</p><h2 id="channel-title">新建通知通道</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button></div>
    <div className="form-grid two-columns"><label><span>名称</span><input value={input.name} onChange={(event) => set('name', event.target.value)} /></label><label><span>类型</span><select value={input.kind} disabled={testing || saving} onChange={(event) => set('kind', event.target.value as NotificationKind)}><option value="telegram">Telegram</option><option value="pushplus">PushPlus</option><option value="wxpush">WXPush</option></select></label>
      {input.kind === 'telegram' && <><label className="wide-field"><span>Bot token</span><input type="password" autoComplete="off" value={input.telegram_bot_token ?? ''} onChange={(event) => set('telegram_bot_token', event.target.value)} /></label><label className="wide-field"><span>Chat ID</span><input value={input.telegram_chat_id ?? ''} onChange={(event) => set('telegram_chat_id', event.target.value)} /></label></>}
      {input.kind === 'pushplus' && <><label className="wide-field"><span>PushPlus token</span><input type="password" autoComplete="off" value={input.pushplus_token ?? ''} onChange={(event) => set('pushplus_token', event.target.value)} /></label><label className="wide-field"><span>群组编码（可选）</span><input value={input.pushplus_topic ?? ''} onChange={(event) => set('pushplus_topic', event.target.value)} /></label></>}
      {input.kind === 'wxpush' && <><label><span>WX_APPID</span><input autoComplete="off" disabled={testing} value={input.wxpush_app_id ?? ''} onChange={(event) => set('wxpush_app_id', event.target.value)} /></label><label><span>WX_SECRET</span><input type="password" autoComplete="off" disabled={testing} value={input.wxpush_app_secret ?? ''} onChange={(event) => set('wxpush_app_secret', event.target.value)} /></label><label className="wxpush-credential-field"><span>WX_USERID（单个 OpenID）</span><input autoComplete="off" disabled={testing} value={input.wxpush_user_id ?? ''} onChange={(event) => set('wxpush_user_id', event.target.value)} /></label><label className="wxpush-credential-field"><span>WX_TEMPLATE_ID</span><input autoComplete="off" disabled={testing} value={input.wxpush_template_id ?? ''} onChange={(event) => set('wxpush_template_id', event.target.value)} /></label></>}
      <ChannelGuide kind={input.kind} />
      <label className="switch-field"><input type="checkbox" checked={input.enabled} onChange={(event) => set('enabled', event.target.checked)} /><span>启用邮件通知</span></label><label className="switch-field"><input type="checkbox" checked={input.system_enabled} onChange={(event) => set('system_enabled', event.target.checked)} /><span>接收系统告警</span></label></div>
    {error && <p className="form-error" role="alert">{error}</p>}{input.kind === 'wxpush' && testedFingerprint === wxPushConfigFingerprint(input) && <p className="form-success" role="status"><Check size={15} /> 测试成功，可以创建通道</p>}<div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button>{input.kind === 'wxpush' && <button className="secondary-button" type="button" disabled={testing || saving || wxPushCredentialKeys.some((key) => !input[key]?.trim())} onClick={() => void testWXPush()}>{testing ? <LoaderCircle className="is-spinning" size={16} /> : <Send size={16} />} 测试配置</button>}<button className="primary-button" type="button" disabled={saving || testing || !canCreateNotificationChannel(input, testedFingerprint)} onClick={() => void save()}>{saving ? <LoaderCircle className="is-spinning" size={16} /> : <Check size={16} />} 创建通道</button></div>
  </section></div>
}

export function notificationChannelGuide(kind: NotificationKind) {
  return kind === 'telegram' ? {
    title: 'Telegram 配置教程',
    intro: '本项目通过 Telegram Bot API 的 sendMessage 接口发送邮件通知。',
    steps: [
      '在 Telegram 搜索 @BotFather，发送 /newbot，保存返回的 Bot Token。',
      '打开机器人会话发送 /start 或任意消息；发送到群组时，先把机器人加入群组。',
      '用该机器人调用 getUpdates，找到消息中的 chat.id；它就是本页面的 Chat ID。',
      '填写 Bot token 和 Chat ID，保存通道后点击测试通知。',
    ],
    links: [
      { href: 'https://t.me/BotFather', label: '打开 BotFather' },
      { href: 'https://core.telegram.org/bots/api', label: '查看 Bot API' },
    ],
  } : kind === 'pushplus' ? {
    title: 'PushPlus 配置教程',
    intro: '本项目调用 PushPlus 的消息接口，并固定使用 txt 模板发送纯文本通知。',
    steps: [
      '登录 PushPlus 官网，在个人中心复制消息接口使用的 Token。',
      '只发给自己时，群组编码留空；需要多人接收时，先在 PushPlus 创建群组并让成员加入。',
      '将 Token 填入 PushPlus token；需要群组推送时，再填写群组编码。',
      '保存通道后点击测试通知，确认消息已到达微信。',
    ],
    links: [
      { href: 'https://www.pushplus.plus/', label: '打开 PushPlus' },
      { href: 'https://www.pushplus.plus/doc/guide/api.html', label: '查看消息接口文档' },
    ],
  } : {
    title: 'WXPush 接入教程',
    intro: '本项目已内置微信公众号模板消息发送，可推送寄件人、邮件标题和正文预览，无需部署独立 WXPush 服务。',
    steps: [
      '打开微信公众平台接口测试帐号页面，扫码登录后复制 AppID 和 AppSecret，分别填写 WX_APPID、WX_SECRET。',
      '用接收通知的微信关注测试号，在测试号页面的用户列表复制该用户 OpenID，填写 WX_USERID；一个通道只对应一个 OpenID。',
      '新增测试模板；模板内容不能只写固定文字（例如 123），必须使用下方动态占位符。保存后复制模板 ID，填写 WX_TEMPLATE_ID。',
      '如沿用旧版两字段模板，{{content.DATA}} 仍会收到寄件人、邮件标题和正文预览的组合内容。',
      '点击“测试配置”并确认微信收到测试消息；测试成功后才能创建通道。任一凭据修改后需要重新测试。',
    ],
    template: '通知类型：{{title.DATA}}\n寄件人：{{sender.DATA}}\n邮件标题：{{subject.DATA}}\n正文：{{body.DATA}}',
    links: [
      { href: 'https://mp.weixin.qq.com/debug/cgi-bin/sandbox?t=sandbox/login', label: '打开微信测试号' },
      { href: 'https://developers.weixin.qq.com/doc/offiaccount/Message_Management/Template_Message_Interface.html', label: '查看模板消息文档' },
    ],
  }
}

function ChannelGuide({ kind }: { kind: NotificationKind }) {
  const guide = notificationChannelGuide(kind)
  return <div className="channel-guide wide-field" role="note"><div className="channel-guide-heading"><BookOpen size={16} /><strong>{guide.title}</strong></div><p>{guide.intro}</p><ol>{guide.steps.map((step) => <li key={step}>{step}</li>)}</ol>{'template' in guide && <pre className="channel-guide-template">{guide.template}</pre>}<div className="channel-guide-links">{guide.links.map((link) => <a key={link.href} href={link.href} target="_blank" rel="noreferrer"><ExternalLink size={14} /> {link.label}</a>)}</div></div>
}

function RuleDialog({ channels, csrfToken, onClose, onCreated }: { channels: Array<{ public_id: string; name: string }>; csrfToken: string; onClose: () => void; onCreated: () => Promise<void> }) {
  const [name, setName] = useState(''); const [channel, setChannel] = useState(channels[0]?.public_id ?? ''); const [categories, setCategories] = useState<string[]>([]); const [personalOnly, setPersonalOnly] = useState(false); const [sender, setSender] = useState(''); const [domain, setDomain] = useState(''); const [keywords, setKeywords] = useState(''); const [accounts, setAccounts] = useState(''); const [groups, setGroups] = useState(''); const [tags, setTags] = useState(''); const [requireOTP, setRequireOTP] = useState(false); const [includeOTP, setIncludeOTP] = useState(false); const [saving, setSaving] = useState(false); const [error, setError] = useState('')
  const split = (value: string) => value.split(',').map((item) => item.trim()).filter(Boolean)
  function toggleCategory(category: string, checked: boolean) { setCategories((current) => checked ? current.includes(category) ? current : [...current, category] : current.filter((item) => item !== category)) }
  async function save() { setSaving(true); setError(''); try { await createNotificationRule({ channel_public_id: channel, name, enabled: true, personal_only: personalOnly, account_public_ids: split(accounts), group_names: split(groups), tag_names: split(tags), categories, sender_address: sender.trim(), sender_domain: domain.trim(), subject_keywords: split(keywords), start_minute: -1, end_minute: -1, require_otp: requireOTP, include_otp: includeOTP }, csrfToken); await onCreated() } catch (reason) { setError(messageFor(reason, '无法创建通知规则')); setSaving(false) } }
  return <div className="dialog-backdrop"><section className="dialog-panel operation-dialog" role="dialog" aria-modal="true" aria-labelledby="notification-rule-title"><div className="dialog-heading"><div><p className="eyebrow">Notification rule</p><h2 id="notification-rule-title">新建通知规则</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button></div>
    <div className="form-grid two-columns"><label><span>规则名称</span><input value={name} onChange={(event) => setName(event.target.value)} /></label><label><span>通知通道</span><select value={channel} onChange={(event) => setChannel(event.target.value)}>{channels.map((item) => <option key={item.public_id} value={item.public_id}>{item.name}</option>)}</select></label><fieldset className="wide-field checkbox-group notification-category-group"><legend>邮件分类</legend>{notificationCategoryOptions.map((category) => <label key={category.value}><input type="checkbox" checked={categories.includes(category.value)} onChange={(event) => toggleCategory(category.value, event.target.checked)} /><span>{category.label}</span></label>)}<small className="notification-category-hint">未选择分类表示全部分类，包括垃圾邮件。</small></fieldset><label className="wide-field switch-field notification-personal-field"><input type="checkbox" checked={personalOnly} onChange={(event) => setPersonalOnly(event.target.checked)} /><span>仅匹配个性化邮件</span><small className="cell-note">邮件必须命中至少一条启用的个性化收件箱规则。</small></label><label><span>发件人地址</span><input value={sender} onChange={(event) => setSender(event.target.value)} /></label><label><span>发件域名</span><input value={domain} onChange={(event) => setDomain(event.target.value)} /></label><label className="wide-field"><span>主题关键词（逗号分隔）</span><input value={keywords} onChange={(event) => setKeywords(event.target.value)} /></label><label><span>账号公开 ID</span><input value={accounts} onChange={(event) => setAccounts(event.target.value)} /></label><label><span>分组</span><input value={groups} onChange={(event) => setGroups(event.target.value)} /></label><label className="wide-field"><span>标签</span><input value={tags} onChange={(event) => setTags(event.target.value)} /></label><label className="switch-field"><input type="checkbox" checked={requireOTP} onChange={(event) => setRequireOTP(event.target.checked)} /><span>只匹配含验证码邮件</span></label><label className="switch-field"><input type="checkbox" checked={includeOTP} onChange={(event) => setIncludeOTP(event.target.checked)} /><span>通知中包含提取出的验证码</span></label></div>
    {error && <p className="form-error">{error}</p>}<div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={saving || !name.trim() || !channel} onClick={() => void save()}>{saving ? <LoaderCircle className="is-spinning" size={16} /> : <Check size={16} />} 创建规则</button></div>
  </section></div>
}

function Empty({ text, loading = false }: { text: string; loading?: boolean }) { return <div className="operations-empty">{loading && <LoaderCircle className="is-spinning" size={18} />}{text}</div> }
function kindLabel(kind: NotificationKind) { return kind === 'telegram' ? 'Telegram' : kind === 'pushplus' ? 'PushPlus' : 'WXPush' }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(new Date(value)) }
function messageFor(error: unknown, fallback: string) { return error instanceof APIError ? error.message : fallback }

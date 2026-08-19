import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Check,
  Clock3,
  Database,
  ExternalLink,
  Globe2,
  HardDrive,
  KeyRound,
  LoaderCircle,
  LogOut,
  MailOpen,
  RefreshCw,
  Save,
  ShieldCheck,
} from 'lucide-react'

import { APIError, type AuthStatus } from '../api/auth'
import { fetchAccountConfig, updateAccountConfig } from '../api/accounts'
import { fetchMailStatus } from '../api/mail'
import { fetchSettings, type AppSettings, updateSettings } from '../api/settings'
import { AppFrame } from '../components/AppFrame'

const timezones = [
  ['Asia/Shanghai', '中国标准时间'],
  ['UTC', '协调世界时'],
  ['Asia/Tokyo', '东京'],
  ['Europe/London', '伦敦'],
  ['America/New_York', '纽约'],
  ['America/Los_Angeles', '洛杉矶'],
] as const

export function SettingsDashboard({
  status,
  onLogout,
  loggingOut,
  logoutError,
}: {
  status: AuthStatus
  onLogout: () => void
  loggingOut: boolean
  logoutError: string
}) {
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({
    queryKey: ['app-settings'],
    queryFn: ({ signal }) => fetchSettings(signal),
  })
  const mailStatusQuery = useQuery({
    queryKey: ['mail-status'],
    queryFn: ({ signal }) => fetchMailStatus(signal),
    refetchInterval: 30_000,
  })
  const microsoftConfigQuery = useQuery({
    queryKey: ['account-config'],
    queryFn: ({ signal }) => fetchAccountConfig(signal),
  })
  const [draft, setDraft] = useState<AppSettings | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [saveMessage, setSaveMessage] = useState('')
  const [clientIDDraft, setClientIDDraft] = useState('')
  const [savingClientID, setSavingClientID] = useState(false)
  const [clientIDError, setClientIDError] = useState('')
  const [clientIDMessage, setClientIDMessage] = useState('')

  useEffect(() => {
    if (settingsQuery.data) setDraft(settingsQuery.data)
  }, [settingsQuery.data])

  useEffect(() => {
    if (microsoftConfigQuery.data) setClientIDDraft(microsoftConfigQuery.data.client_id)
  }, [microsoftConfigQuery.data])

  const hasChanges = draft !== null && settingsQuery.data !== undefined && !sameSettings(draft, settingsQuery.data)
  const queue = (mailStatusQuery.data?.high_priority_queue ?? 0) + (mailStatusQuery.data?.background_queue ?? 0)
  const clientIDChanged = microsoftConfigQuery.data !== undefined && clientIDDraft.trim().toLowerCase() !== microsoftConfigQuery.data.client_id

  async function saveSettings() {
    if (!draft || !status.csrf_token) return
    setSaving(true)
    setSaveError('')
    setSaveMessage('')
    try {
      const saved = await updateSettings(draft, status.csrf_token)
      queryClient.setQueryData(['app-settings'], saved)
      setDraft(saved)
      setSaveMessage('设置已保存并开始生效')
      await queryClient.invalidateQueries({ queryKey: ['mail-messages'] })
    } catch (error) {
      setSaveError(error instanceof APIError ? error.message : '无法保存设置')
    } finally {
      setSaving(false)
    }
  }

  async function saveClientID() {
    if (!status.csrf_token || !clientIDChanged) return
    setSavingClientID(true)
    setClientIDError('')
    setClientIDMessage('')
    try {
      const saved = await updateAccountConfig(clientIDDraft, status.csrf_token)
      queryClient.setQueryData(['account-config'], saved)
      setClientIDDraft(saved.client_id)
      setClientIDMessage('Microsoft Client ID 已保存，新授权立即使用此配置')
    } catch (error) {
      setClientIDError(error instanceof APIError ? error.message : '无法保存 Microsoft Client ID')
    } finally {
      setSavingClientID(false)
    }
  }

  return (
    <AppFrame active="settings">
      <main className="main-workspace settings-workspace" id="settings">
        <header className="workspace-header settings-header">
          <div>
            <p className="eyebrow">Application preferences</p>
            <h1>设置</h1>
          </div>
          <div className="header-actions">
            <button className="secondary-button" type="button" onClick={() => { void settingsQuery.refetch(); void microsoftConfigQuery.refetch() }} disabled={settingsQuery.isFetching || microsoftConfigQuery.isFetching}>
              <RefreshCw className={settingsQuery.isFetching || microsoftConfigQuery.isFetching ? 'is-spinning' : ''} size={17} />
              重新读取
            </button>
            <button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}>
              <LogOut size={17} /> {loggingOut ? '正在退出' : '退出登录'}
            </button>
          </div>
        </header>

        {(logoutError || saveError || clientIDError || saveMessage || clientIDMessage) && (
          <div className={`settings-notice ${logoutError || saveError || clientIDError ? 'is-error' : ''}`} role="status">
            {logoutError || saveError || clientIDError ? <ShieldCheck size={17} /> : <Check size={17} />}
            <span>{logoutError || saveError || clientIDError || saveMessage || clientIDMessage}</span>
          </div>
        )}

        <section className="settings-status-strip" aria-label="系统运行摘要">
          <StatusItem icon={<Clock3 size={18} />} label="同步周期" value={draft ? formatSyncInterval(draft.sync_interval_seconds) : '读取中'} />
          <StatusItem icon={<Database size={18} />} label="可同步账号" value={`${mailStatusQuery.data?.active_accounts ?? 0} 个`} />
          <StatusItem icon={<RefreshCw size={18} />} label="当前队列" value={`${queue} 项`} />
          <StatusItem icon={<HardDrive size={18} />} label="磁盘占用" value={`${mailStatusQuery.data?.disk.used_percent ?? 0}%`} />
        </section>

        <form className="settings-form oauth-settings-form" onSubmit={(event) => { event.preventDefault(); void saveClientID() }}>
          <SettingsSection icon={<KeyRound size={19} />} title="Microsoft OAuth" detail="配置本项目自己的公开应用标识，用于 Outlook 与 Hotmail 设备码授权。">
            <SettingsField label="Application (client) ID" detail="从 Microsoft Entra 应用注册的概述页复制；Client ID 不是客户端密码。">
              <input
                type="text"
                value={clientIDDraft}
                onChange={(event) => setClientIDDraft(event.target.value)}
                placeholder="00000000-0000-0000-0000-000000000000"
                autoComplete="off"
                spellCheck={false}
                disabled={microsoftConfigQuery.isPending || microsoftConfigQuery.isError}
                aria-invalid={clientIDError ? 'true' : undefined}
              />
            </SettingsField>
            <div className="oauth-config-actions">
              <div>
                <strong className={microsoftConfigQuery.data?.microsoft_configured ? 'is-configured' : ''}>
                  {microsoftConfigQuery.isError ? '配置状态读取失败' : microsoftConfigQuery.data?.microsoft_configured ? '已配置，可开始新授权' : '尚未配置'}
                </strong>
                <a href="https://learn.microsoft.com/entra/identity-platform/quickstart-register-app" target="_blank" rel="noreferrer">
                  <ExternalLink size={14} /> 查看 Microsoft 注册说明
                </a>
              </div>
              <button className="primary-button" type="submit" disabled={savingClientID || !clientIDChanged || microsoftConfigQuery.isError}>
                {savingClientID ? <LoaderCircle className="is-spinning" size={17} /> : <Save size={17} />}
                {savingClientID ? '正在保存' : clientIDChanged ? '保存 Client ID' : 'Client ID 已保存'}
              </button>
            </div>
            <div className="settings-invariant">
              <ShieldCheck size={18} />
              <div><strong>更换配置不会改写已有账号</strong><span>已授权账号会继续使用原 Client ID 刷新；需要迁移时，请对该账号重新授权。</span></div>
            </div>
          </SettingsSection>
        </form>

        {settingsQuery.isError ? (
          <div className="settings-loading error-copy"><ShieldCheck size={23} />无法读取设置</div>
        ) : settingsQuery.isPending || !draft ? (
          <div className="settings-loading"><LoaderCircle className="is-spinning" size={23} />正在读取设置</div>
        ) : (
          <form className="settings-form" onSubmit={(event) => { event.preventDefault(); void saveSettings() }}>
            <SettingsSection icon={<RefreshCw size={19} />} title="邮件同步" detail="控制后台同步节奏和新账号首次建立索引的时间范围。">
              <SettingsField label="后台同步周期" detail="保存后调度器立即重新计算，不需要重启服务。">
                <select value={draft.sync_interval_seconds} onChange={(event) => setDraft({ ...draft, sync_interval_seconds: Number(event.target.value) as AppSettings['sync_interval_seconds'] })}>
                  <option value={5}>每 5 秒</option>
                  <option value={60}>每 1 分钟</option>
                  <option value={300}>每 5 分钟</option>
                  <option value={600}>每 10 分钟</option>
                  <option value={900}>每 15 分钟</option>
                  <option value={1800}>每 30 分钟</option>
                  <option value={3600}>每 60 分钟</option>
                </select>
              </SettingsField>
              {draft.sync_interval_seconds === 5 && <div className="settings-invariant settings-rate-warning">
                <Clock3 size={18} />
                <div><strong>5 秒为尽力同步</strong><span>系统仍会遵守 Microsoft Graph 限流和账号重试时间；账号较多时，不保证每个账号精确每 5 秒完成一次同步。</span></div>
              </div>}
              <SettingsField label="首次同步范围" detail="用于新授权账号和失效游标重建；不会删除已缓存邮件。">
                <select value={draft.initial_sync_days} onChange={(event) => setDraft({ ...draft, initial_sync_days: Number(event.target.value) as AppSettings['initial_sync_days'] })}>
                  <option value={7}>最近 7 天</option>
                  <option value={14}>最近 14 天</option>
                  <option value={30}>最近 30 天</option>
                  <option value={60}>最近 60 天</option>
                  <option value={90}>最近 90 天</option>
                </select>
              </SettingsField>
            </SettingsSection>

            <SettingsSection icon={<Database size={19} />} title="本地缓存" detail="在搜索完整度和磁盘占用之间选择合适的单封邮件上限。">
              <SettingsField label="正文缓存上限" detail="超出部分会安全截断，HTML 和附件不会持久化到本地。">
                <select value={draft.body_cache_kib} onChange={(event) => setDraft({ ...draft, body_cache_kib: Number(event.target.value) as AppSettings['body_cache_kib'] })}>
                  <option value={64}>64 KiB</option>
                  <option value={128}>128 KiB</option>
                  <option value={256}>256 KiB</option>
                  <option value={512}>512 KiB</option>
                  <option value={1024}>1 MiB</option>
                </select>
              </SettingsField>
              <div className="settings-invariant">
                <ShieldCheck size={18} />
                <div><strong>远程图片继续逐封确认</strong><span>设置页不会开启全局自动加载，避免追踪像素泄露阅读状态。</span></div>
              </div>
            </SettingsSection>

            <SettingsSection icon={<MailOpen size={19} />} title="收件箱体验" detail="调整列表密度、默认阅读方式和已读行为。">
              <SettingsField label="列表邮件数量" detail="搜索和筛选每次最多返回的邮件数量。">
                <select value={draft.message_page_size} onChange={(event) => setDraft({ ...draft, message_page_size: Number(event.target.value) as AppSettings['message_page_size'] })}>
                  <option value={25}>25 封</option>
                  <option value={50}>50 封</option>
                  <option value={100}>100 封</option>
                  <option value={200}>200 封</option>
                </select>
              </SettingsField>
              <SettingsField label="默认正文模式" detail="HTML 模式仍会拦截远程图片和脚本。">
                <div className="settings-segmented" role="group" aria-label="默认正文模式">
                  <button type="button" className={draft.reader_mode === 'text' ? 'is-selected' : ''} onClick={() => setDraft({ ...draft, reader_mode: 'text' })}>纯文本</button>
                  <button type="button" className={draft.reader_mode === 'html' ? 'is-selected' : ''} onClick={() => setDraft({ ...draft, reader_mode: 'html' })}>安全 HTML</button>
                </div>
              </SettingsField>
              <SettingsField label="默认邮件目录" detail="进入统一收件箱、个性化收件箱或验证码时首先显示的目录。">
                <select value={draft.default_folder} onChange={(event) => setDraft({ ...draft, default_folder: event.target.value as AppSettings['default_folder'] })}>
                  <option value="all">全部目录</option>
                  <option value="inbox">收件箱</option>
                  <option value="junkemail">垃圾邮件</option>
                </select>
              </SettingsField>
              <label className="settings-toggle-row">
                <div><strong>打开邮件后标记为已读</strong><span>关闭后，点击邮件只显示正文，不修改 Outlook 已读状态。</span></div>
                <input type="checkbox" checked={draft.mark_read_on_open} onChange={(event) => setDraft({ ...draft, mark_read_on_open: event.target.checked })} />
              </label>
              <label className="settings-toggle-row">
                <div><strong>默认只看未读</strong><span>每次进入邮件板块时先隐藏已读邮件，仍可在筛选栏随时关闭。</span></div>
                <input type="checkbox" checked={draft.default_unread_only} onChange={(event) => setDraft({ ...draft, default_unread_only: event.target.checked })} />
              </label>
              <label className="settings-toggle-row">
                <div><strong>桌面端自动打开第一封</strong><span>关闭后先保持阅读区为空，直到手动选择邮件。</span></div>
                <input type="checkbox" checked={draft.auto_select_first_message} onChange={(event) => setDraft({ ...draft, auto_select_first_message: event.target.checked })} />
              </label>
              <label className="settings-toggle-row">
                <div><strong>列表显示正文摘要</strong><span>关闭后列表更紧凑，主题、发件人和分类仍然保留。</span></div>
                <input type="checkbox" checked={draft.show_body_preview} onChange={(event) => setDraft({ ...draft, show_body_preview: event.target.checked })} />
              </label>
            </SettingsSection>

            <SettingsSection icon={<Globe2 size={19} />} title="区域与时间" detail="选择收件箱时间戳使用的显示时区。">
              <SettingsField label="收件箱时区" detail="数据库仍统一使用 UTC 保存时间。">
                <select value={draft.timezone} onChange={(event) => setDraft({ ...draft, timezone: event.target.value })}>
                  {timezones.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
                </select>
              </SettingsField>
            </SettingsSection>

            <footer className="settings-actions">
              <span>{draft.updated_at ? `上次保存：${formatSavedAt(draft.updated_at, draft.timezone)}` : '尚未保存'}</span>
              <button className="primary-button" type="submit" disabled={saving || !hasChanges}>
                {saving ? <LoaderCircle className="is-spinning" size={17} /> : <Save size={17} />}
                {saving ? '正在保存' : hasChanges ? '保存设置' : '设置已保存'}
              </button>
            </footer>
          </form>
        )}
      </main>
    </AppFrame>
  )
}

function SettingsSection({ icon, title, detail, children }: { icon: React.ReactNode; title: string; detail: string; children: React.ReactNode }) {
  return (
    <section className="settings-section">
      <header><span>{icon}</span><div><h2>{title}</h2><p>{detail}</p></div></header>
      <div className="settings-section-body">{children}</div>
    </section>
  )
}

function SettingsField({ label, detail, children }: { label: string; detail: string; children: React.ReactNode }) {
  return (
    <label className="settings-field">
      <span><strong>{label}</strong><small>{detail}</small></span>
      {children}
    </label>
  )
}

function StatusItem({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return <div className="settings-status-item"><span>{icon}</span><div><small>{label}</small><strong>{value}</strong></div></div>
}

function sameSettings(left: AppSettings, right: AppSettings) {
  return left.sync_interval_seconds === right.sync_interval_seconds &&
    left.initial_sync_days === right.initial_sync_days &&
    left.body_cache_kib === right.body_cache_kib &&
    left.message_page_size === right.message_page_size &&
    left.timezone === right.timezone &&
    left.reader_mode === right.reader_mode &&
    left.mark_read_on_open === right.mark_read_on_open &&
    left.default_folder === right.default_folder &&
    left.default_unread_only === right.default_unread_only &&
    left.auto_select_first_message === right.auto_select_first_message &&
    left.show_body_preview === right.show_body_preview
}

function formatSyncInterval(seconds: number) {
  return seconds < 60 ? `${seconds} 秒` : `${seconds / 60} 分钟`
}

function formatSavedAt(value: string, timezone: string) {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hour12: false, timeZone: timezone,
  }).format(new Date(value))
}

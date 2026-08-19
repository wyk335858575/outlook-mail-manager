import { useDeferredValue, useEffect, useMemo, useRef, useState } from 'react'
import { type QueryClient, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  ChevronDown,
  CircleAlert,
  FileText,
  Flag,
  Funnel,
  Download,
  Image as ImageIcon,
  Inbox,
  LayoutTemplate,
  LoaderCircle,
  LogOut,
  MailCheck,
  MailOpen,
  Paperclip,
  RefreshCw,
  Search,
  ShieldAlert,
  Trash2,
  X,
} from 'lucide-react'

import { fetchAccounts, type Account } from '../api/accounts'
import { APIError, type AuthStatus } from '../api/auth'
import { correctMessageCategory, setMessageFlagged, type MailCategory } from '../api/classification'
import {
  attachmentDownloadURL,
  fetchAttachments,
  fetchMailStatus,
  fetchMessage,
  fetchMessages,
  markMessageRead,
  markMessagesRead,
  messageHTMLURL,
  moveMessageToDeletedItems,
  syncMail,
  type MailFolder,
  type MailMessage,
  type MailMessageDetail,
} from '../api/mail'
import { fetchSettings } from '../api/settings'
import { AppFrame } from '../components/AppFrame'
import { inboxRefreshInterval, filterInboxAccounts, removeItemByPublicID } from './inboxHelpers'
import { PersonalRulesDialog } from './PersonalRulesDialog'

type FolderFilter = 'all' | MailFolder
type ReaderMode = 'text' | 'html'
type MailView = 'inbox' | 'personal' | 'verification'

const categoryLabels: Record<MailCategory, string> = {
  important: '重要', verification: '验证码', marketing: '营销', spam: '垃圾', normal: '普通', uncertain: '待确认',
}

export function InboxDashboard({
  status,
  onLogout,
  loggingOut,
  logoutError,
  view = 'inbox',
}: {
  status: AuthStatus
  onLogout: () => void
  loggingOut: boolean
  logoutError: string
  view?: MailView
}) {
  const verificationOnly = view === 'verification'
  const personalOnly = view === 'personal'
  const queryClient = useQueryClient()
  const preferencesApplied = useRef(false)
  const [search, setSearch] = useState('')
  const deferredSearch = useDeferredValue(search.trim())
  const [folder, setFolder] = useState<FolderFilter>('all')
  const [unreadOnly, setUnreadOnly] = useState(false)
  const [category, setCategory] = useState<MailCategory | 'all'>('all')
  const [accountID, setAccountID] = useState('')
  const [accountFilterNeedsSelection, setAccountFilterNeedsSelection] = useState(false)
  const [selectedID, setSelectedID] = useState('')
  const [syncing, setSyncing] = useState(false)
  const [syncPolling, setSyncPolling] = useState(false)
  const [syncMessage, setSyncMessage] = useState('')
  const [syncError, setSyncError] = useState('')
  const [readError, setReadError] = useState('')
  const [selectedForRead, setSelectedForRead] = useState<Set<string>>(() => new Set())
  const [bulkReading, setBulkReading] = useState(false)
  const [messageAction, setMessageAction] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<MailMessage | null>(null)
  const [deletingID, setDeletingID] = useState('')
  const [readerMode, setReaderMode] = useState<ReaderMode>('text')
  const [remoteImagesEnabled, setRemoteImagesEnabled] = useState(false)
  const [htmlFrameLoading, setHTMLFrameLoading] = useState(false)
  const [personalRulesOpen, setPersonalRulesOpen] = useState(false)
  const accountsQuery = useQuery({
    queryKey: ['accounts'],
    queryFn: ({ signal }) => fetchAccounts(signal),
  })
  const settingsQuery = useQuery({
    queryKey: ['app-settings'],
    queryFn: ({ signal }) => fetchSettings(signal),
  })
  const pageSize = settingsQuery.data?.message_page_size ?? 100
  const timezone = settingsQuery.data?.timezone ?? 'Asia/Shanghai'
  const dateFormatter = useMemo(() => new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false, timeZone: timezone,
  }), [timezone])
  const fullDateFormatter = useMemo(() => new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false, timeZone: timezone,
  }), [timezone])

  const messagesQuery = useQuery({
    queryKey: ['mail-messages', view, deferredSearch, accountID, folder, category, unreadOnly, pageSize],
    queryFn: ({ signal }) => fetchMessages({
      q: deferredSearch || undefined,
      account: accountID || undefined,
      folder: folder === 'all' ? undefined : folder,
      category: verificationOnly ? 'verification' : category === 'all' ? undefined : category,
      unread: unreadOnly ? true : undefined,
      personal: personalOnly ? true : undefined,
      limit: pageSize,
    }, signal),
    refetchInterval: syncPolling ? 2_000 : inboxRefreshInterval(settingsQuery.data?.sync_interval_seconds),
  })
  const detailQuery = useQuery({
    queryKey: ['mail-message', selectedID],
    queryFn: ({ signal }) => fetchMessage(selectedID, signal),
    enabled: selectedID !== '',
  })
  const mailStatusQuery = useQuery({
    queryKey: ['mail-status'],
    queryFn: ({ signal }) => fetchMailStatus(signal),
    refetchInterval: 30_000,
  })
  const attachmentsQuery = useQuery({
    queryKey: ['mail-attachments', selectedID],
    queryFn: ({ signal }) => fetchAttachments(selectedID, signal),
    enabled: selectedID !== '' && detailQuery.isSuccess,
  })
  const messages = messagesQuery.data?.messages ?? []
  const selectedMessage = messages.find((message) => message.public_id === selectedID)
  const unreadIDs = useMemo(() => messages.filter((message) => message.unread).map((message) => message.public_id), [messages])
  const selectedUnreadIDs = unreadIDs.filter((publicID) => selectedForRead.has(publicID))
  const allUnreadSelected = unreadIDs.length > 0 && selectedUnreadIDs.length === unreadIDs.length

  function selectAccount(publicID: string) {
    setAccountID(publicID)
    setAccountFilterNeedsSelection(true)
    setSelectedID('')
    setSelectedForRead(new Set())
  }

  useEffect(() => {
    if (!settingsQuery.data || preferencesApplied.current) return
    preferencesApplied.current = true
    setFolder(settingsQuery.data.default_folder)
    setUnreadOnly(settingsQuery.data.default_unread_only)
  }, [settingsQuery.data])

  useEffect(() => {
    setSelectedForRead((current) => {
      const visible = new Set(unreadIDs)
      const next = new Set([...current].filter((publicID) => visible.has(publicID)))
      if (next.size === current.size && [...next].every((publicID) => current.has(publicID))) return current
      return next
    })
  }, [unreadIDs])

  useEffect(() => {
    if (selectedID && messagesQuery.isSuccess && !messages.some((message) => message.public_id === selectedID)) {
      setSelectedID('')
      return
    }
    if (accountFilterNeedsSelection || settingsQuery.isPending || settingsQuery.data?.auto_select_first_message === false) return
    if (selectedID || messages.length === 0 || !window.matchMedia('(min-width: 761px)').matches) return
    setSelectedID(messages[0].public_id)
  }, [accountFilterNeedsSelection, messages, messagesQuery.isSuccess, selectedID, settingsQuery.data?.auto_select_first_message, settingsQuery.isPending])

  useEffect(() => {
    if (!syncPolling) return
    const timeout = window.setTimeout(() => setSyncPolling(false), 30_000)
    return () => window.clearTimeout(timeout)
  }, [syncPolling])

  useEffect(() => {
    if (window.matchMedia('(max-width: 760px)').matches) window.scrollTo({ top: 0, behavior: 'auto' })
  }, [selectedID])

  useEffect(() => {
    setReaderMode(settingsQuery.data?.reader_mode ?? 'text')
    setRemoteImagesEnabled(false)
    setHTMLFrameLoading(false)
  }, [selectedID, settingsQuery.data?.reader_mode])

  async function startSync() {
    if (!status.csrf_token) return
    setSyncing(true)
    setSyncMessage('')
    setSyncError('')
    try {
      const result = await syncMail(status.csrf_token)
      setSyncMessage(`已将 ${result.queued ?? 0} 个账号加入同步队列`)
      setSyncPolling(true)
      await queryClient.invalidateQueries({ queryKey: ['mail-status'] })
    } catch (error) {
      setSyncError(messageFor(error, '无法启动邮件同步'))
    } finally {
      setSyncing(false)
    }
  }

  async function selectMessage(message: MailMessage) {
    setAccountFilterNeedsSelection(false)
    setSelectedID(message.public_id)
    if (!message.unread || !status.csrf_token || settingsQuery.isPending || settingsQuery.data?.mark_read_on_open === false) return
    setReadError('')
    await queryClient.cancelQueries({ queryKey: ['mail-messages'] })
    setCachedReadState(queryClient, message.public_id, false)
    try {
      await markMessageRead(message.public_id, status.csrf_token)
      setCachedReadState(queryClient, message.public_id, false)
      await queryClient.invalidateQueries({ queryKey: ['mail-messages'] })
    } catch (error) {
      setCachedReadState(queryClient, message.public_id, true)
      setReadError(messageFor(error, '无法将邮件标记为已读'))
    }
  }

  function toggleReadSelection(publicID: string, selected: boolean) {
    setSelectedForRead((current) => {
      const next = new Set(current)
      if (selected) next.add(publicID)
      else next.delete(publicID)
      return next
    })
  }

  function toggleAllUnread(selected: boolean) {
    setSelectedForRead(selected ? new Set(unreadIDs) : new Set())
  }

  async function markSelectedRead() {
    if (!status.csrf_token || selectedUnreadIDs.length === 0) return
	const publicIDs = selectedUnreadIDs.slice(0, 500)
    setBulkReading(true)
    setReadError('')
    setSyncMessage('')
    await queryClient.cancelQueries({ queryKey: ['mail-messages'] })
    setCachedReadStates(queryClient, publicIDs, false)
    try {
      const result = await markMessagesRead(publicIDs, status.csrf_token)
      const failed = result.results.filter((item) => !item.read).map((item) => item.public_id)
      if (failed.length > 0) setCachedReadStates(queryClient, failed, true)
      setSelectedForRead(new Set(failed))
      setSyncMessage(`已将 ${result.updated} 封邮件设为已读`)
      if (failed.length > 0) setReadError(`${failed.length} 封邮件设置失败，请重试`)
      await queryClient.invalidateQueries({ queryKey: ['mail-messages'] })
    } catch (error) {
      setCachedReadStates(queryClient, publicIDs, true)
      setReadError(messageFor(error, '无法批量设置已读'))
    } finally {
      setBulkReading(false)
    }
  }

  async function changeCategory(value: MailCategory) {
    if (!selectedID || !status.csrf_token) return
    setMessageAction('category'); setReadError('')
    try {
      await correctMessageCategory(selectedID, value, status.csrf_token)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['mail-message', selectedID] }),
        queryClient.invalidateQueries({ queryKey: ['mail-messages'] }),
        queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] }),
      ])
    } catch (error) { setReadError(messageFor(error, '无法更新邮件分类')) } finally { setMessageAction('') }
  }

  async function toggleFlag() {
    if (!detailQuery.data || !status.csrf_token) return
    const flagged = !detailQuery.data.flagged
    setMessageAction('flag'); setReadError('')
    setCachedFlaggedState(queryClient, detailQuery.data.public_id, flagged)
    try {
      await setMessageFlagged(detailQuery.data.public_id, flagged, status.csrf_token)
      await queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] })
    } catch (error) {
      setCachedFlaggedState(queryClient, detailQuery.data.public_id, !flagged)
      setReadError(messageFor(error, '无法更新星标状态'))
    } finally { setMessageAction('') }
  }

  async function deleteSelectedMessage() {
    if (!deleteTarget || !status.csrf_token) return
    const publicID = deleteTarget.public_id
    setDeletingID(publicID)
    setReadError('')
    setSyncMessage('')
    try {
      await moveMessageToDeletedItems(publicID, status.csrf_token)
      removeCachedMessage(queryClient, publicID)
      setSelectedForRead((current) => {
        const next = new Set(current)
        next.delete(publicID)
        return next
      })
      if (selectedID === publicID) setSelectedID('')
      setDeleteTarget(null)
      setSyncMessage('邮件已移入 Outlook“已删除邮件”，可在 Outlook 中恢复')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['mail-messages'] }),
        queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] }),
        queryClient.invalidateQueries({ queryKey: ['mail-status'] }),
      ])
    } catch (error) {
      setReadError(messageFor(error, '无法将邮件移入 Outlook 已删除邮件'))
    } finally {
      setDeletingID('')
    }
  }

  return (
    <AppFrame active={verificationOnly ? 'verification' : personalOnly ? 'personal' : 'inbox'}>
      <main className={`mail-workspace ${selectedID ? 'is-reading' : ''}`}>
        <header className="mail-toolbar">
          <div className="mail-title">
            <p className="eyebrow">{verificationOnly ? 'Verification codes' : personalOnly ? 'Priority stream' : 'Unified inbox'}</p>
            <h1>{verificationOnly ? '验证码' : personalOnly ? '个性化收件箱' : '统一收件箱'}</h1>
          </div>
          <label className="mail-search">
            <Search size={17} aria-hidden="true" />
            <span className="visually-hidden">搜索邮件</span>
            <input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索主题、发件人或正文" />
          </label>
          <div className="mail-toolbar-actions">
            <button className="icon-command" type="button" title="立即同步" aria-label="立即同步" onClick={() => void startSync()} disabled={syncing}>
              {syncing ? <LoaderCircle className="is-spinning" size={17} /> : <RefreshCw size={17} />}
            </button>
            <button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}>
              {loggingOut ? <LoaderCircle className="is-spinning" size={17} /> : <LogOut size={17} />} 退出登录
            </button>
          </div>
        </header>

        {(logoutError || syncError || readError || syncMessage) && (
          <div className={`mail-notice ${logoutError || syncError || readError ? 'is-error' : ''}`} role="status">
            {logoutError || syncError || readError ? <AlertTriangle size={16} /> : <RefreshCw size={16} />}
            <span>{logoutError || syncError || readError || syncMessage}</span>
          </div>
        )}

        <div className="mail-filterbar">
          <div className="folder-tabs" role="tablist" aria-label="邮件文件夹">
            {([
              ['all', '全部'],
              ['inbox', '收件箱'],
              ['junkemail', '垃圾邮件'],
            ] as const).map(([value, label]) => (
              <button key={value} type="button" role="tab" aria-selected={folder === value} className={folder === value ? 'is-selected' : ''} onClick={() => setFolder(value)}>
                {label}
              </button>
            ))}
          </div>
          {!personalOnly && !verificationOnly && <AccountFilter
            accounts={accountsQuery.data?.accounts ?? []}
            loading={accountsQuery.isPending}
            value={accountID}
            onChange={selectAccount}
          />}
          <label className="unread-toggle">
            <input type="checkbox" checked={unreadOnly} onChange={(event) => setUnreadOnly(event.target.checked)} />
            <span>只看未读</span>
          </label>
          {!verificationOnly && <div className="category-filter-group"><label className="category-filter">
            <span className="visually-hidden">分类筛选</span>
            <select value={category} onChange={(event) => setCategory(event.target.value as MailCategory | 'all')}>
              <option value="all">全部分类</option>
              {Object.entries(categoryLabels).map(([id, label]) => <option key={id} value={id}>{label}</option>)}
            </select>
          </label>{personalOnly && <button className="personal-rule-button" type="button" onClick={() => setPersonalRulesOpen(true)}><Funnel size={14} /> 个性化规则</button>}</div>}
          <MailHealth status={mailStatusQuery.data} />
        </div>

        <section className="mail-content" aria-label="邮件浏览器">
          <div className="message-list-pane">
            <div className="message-list-heading">
              <label className="bulk-read-select">
                <input type="checkbox" checked={allUnreadSelected} disabled={unreadIDs.length === 0 || bulkReading} onChange={(event) => toggleAllUnread(event.target.checked)} />
                <span>{selectedUnreadIDs.length > 0 ? `已选 ${selectedUnreadIDs.length}` : '选择未读'}</span>
              </label>
              <span className="message-list-count">{messagesQuery.isPending ? '正在读取' : `${messages.length} 封`}</span>
              <button className="bulk-read-button" type="button" disabled={selectedUnreadIDs.length === 0 || bulkReading} onClick={() => void markSelectedRead()}>
                {bulkReading ? <LoaderCircle className="is-spinning" size={14} /> : <MailCheck size={14} />} 设为已读
              </button>
            </div>
            <div className="message-list" aria-live="polite">
              {messagesQuery.isPending ? (
                <MailEmpty icon={<LoaderCircle className="is-spinning" size={22} />} title="正在同步本地索引" />
              ) : messagesQuery.isError ? (
                <MailEmpty icon={<CircleAlert size={22} />} title="无法读取邮件" action={() => void messagesQuery.refetch()} />
              ) : messages.length === 0 ? (
                <MailEmpty
                  icon={<Inbox size={24} />}
                  title={deferredSearch || unreadOnly || folder !== 'all' ? '没有匹配的邮件' : verificationOnly ? '暂无验证码邮件' : personalOnly ? '暂无个性化邮件' : '收件箱尚无本地邮件'}
                  action={personalOnly && !deferredSearch && !unreadOnly && folder === 'all' ? () => setPersonalRulesOpen(true) : undefined}
                  actionLabel="管理规则"
                />
              ) : messages.map((message) => (
                <MessageRow
                  key={message.public_id}
                  message={message}
                  selected={selectedID === message.public_id}
                  selectedForRead={selectedForRead.has(message.public_id)}
                  showPreview={settingsQuery.data?.show_body_preview !== false}
                  dateFormatter={dateFormatter}
                  onSelect={() => void selectMessage(message)}
                  onToggleReadSelection={(selected) => toggleReadSelection(message.public_id, selected)}
                  deleting={deletingID === message.public_id}
                  onDelete={() => setDeleteTarget(message)}
                />
              ))}
            </div>
          </div>

          <article className="message-reader" aria-live="polite">
            {!selectedID ? (
              <div className="reader-empty"><MailOpen size={28} /><strong>选择一封邮件阅读</strong><span>可切换纯文本或安全 HTML 排版。</span></div>
            ) : detailQuery.isPending ? (
              <div className="reader-empty"><LoaderCircle className="is-spinning" size={24} /><span>正在读取邮件</span></div>
            ) : detailQuery.isError || !detailQuery.data ? (
              <div className="reader-empty error-copy"><CircleAlert size={24} /><strong>无法读取这封邮件</strong><button className="secondary-button" type="button" onClick={() => void detailQuery.refetch()}>重新读取</button></div>
            ) : (
              <>
                <button className="reader-back" type="button" onClick={() => setSelectedID('')}><ArrowLeft size={18} /> 返回邮件列表</button>
                <header className="reader-header">
                  <div className="reader-subject-line">
                    <span className={`folder-mark folder-${detailQuery.data.folder}`}>{detailQuery.data.folder_name}</span>
                    <span className={`category-badge category-${detailQuery.data.category}`}>{categoryLabels[detailQuery.data.category]}</span>
                    <ReadStateMark unread={selectedMessage?.unread ?? detailQuery.data.unread} />
                    {detailQuery.data.cleanup_protected && <span className="protection-mark"><ShieldAlert size={13} />受保护</span>}
                  </div>
                  <h2>{detailQuery.data.subject || '（无主题）'}</h2>
                  <div className="reader-sender">
                    <span className="sender-avatar">{senderInitial(detailQuery.data)}</span>
                    <div><strong>{detailQuery.data.sender_name || detailQuery.data.sender_address || '未知发件人'}</strong><span>{detailQuery.data.sender_address}</span></div>
                    <time>{fullDateFormatter.format(new Date(detailQuery.data.received_at))}</time>
                  </div>
                  <div className="reader-account">发送至 <strong>{detailQuery.data.account_address}</strong></div>
                  <div className="reader-actions">
                    <label><span>分类</span><select value={detailQuery.data.category} disabled={messageAction !== ''} onChange={(event) => void changeCategory(event.target.value as MailCategory)}>{Object.entries(categoryLabels).map(([id, label]) => <option key={id} value={id}>{label}</option>)}</select></label>
                    <button className={`icon-command ${detailQuery.data.flagged ? 'is-active' : ''}`} type="button" title={detailQuery.data.flagged ? '取消星标保护' : '添加星标保护'} aria-label={detailQuery.data.flagged ? '取消星标保护' : '添加星标保护'} disabled={messageAction !== ''} onClick={() => void toggleFlag()}>{messageAction === 'flag' ? <LoaderCircle className="is-spinning" size={16} /> : <Flag size={16} />}</button>
                    <button className="icon-command danger-command" type="button" title="移入 Outlook 已删除邮件" aria-label="删除邮件" disabled={messageAction !== '' || deletingID !== ''} onClick={() => setDeleteTarget(detailQuery.data)}>{deletingID === detailQuery.data.public_id ? <LoaderCircle className="is-spinning" size={16} /> : <Trash2 size={16} />}</button>
                    <span>{detailQuery.data.classification_reason}</span>
                  </div>
                </header>
                {detailQuery.data.body_truncated && <div className="body-warning"><ShieldAlert size={16} />正文超过 256 KiB，本地仅显示已缓存部分。</div>}
                <div className="reader-viewbar">
                  <div className="reader-mode-tabs" role="tablist" aria-label="正文显示方式">
                    <button type="button" role="tab" aria-selected={readerMode === 'text'} className={readerMode === 'text' ? 'is-selected' : ''} onClick={() => setReaderMode('text')}><FileText size={15} />纯文本</button>
                    <button type="button" role="tab" aria-selected={readerMode === 'html'} className={readerMode === 'html' ? 'is-selected' : ''} onClick={() => { setReaderMode('html'); setHTMLFrameLoading(true) }}><LayoutTemplate size={15} />HTML 排版</button>
                  </div>
                  {readerMode === 'html' && (
                    <button className="remote-image-button" type="button" onClick={() => { setRemoteImagesEnabled((enabled) => !enabled); setHTMLFrameLoading(true) }}>
                      <ImageIcon size={15} />{remoteImagesEnabled ? '重新拦截图片' : '加载图片'}
                    </button>
                  )}
                </div>
                {readerMode === 'text' ? (
                  <div className="message-body">{detailQuery.data.body_text || '当前处于元数据模式，尚未缓存这封邮件的正文。'}</div>
                ) : (
                  <>
                    <div className={`remote-content-notice ${remoteImagesEnabled ? 'is-enabled' : ''}`} role="status">
                      <ShieldAlert size={16} />
                      <span>{remoteImagesEnabled ? '已允许这封邮件加载 HTTPS 远程图片。' : '远程图片已拦截，避免发件方通过追踪像素获知你已打开邮件。'}</span>
                    </div>
                    <div className="html-reader">
                      {htmlFrameLoading && <div className="html-reader-loading"><LoaderCircle className="is-spinning" size={18} />正在读取安全 HTML</div>}
                      <iframe
                        key={`${detailQuery.data.public_id}-${remoteImagesEnabled}`}
                        className="message-html-frame"
                        src={messageHTMLURL(detailQuery.data.public_id, remoteImagesEnabled)}
                        title={`${detailQuery.data.subject || '无主题'} HTML 正文`}
                        sandbox=""
                        referrerPolicy="no-referrer"
                        onLoad={() => setHTMLFrameLoading(false)}
                      />
                    </div>
                  </>
                )}
                <section className="attachment-section" aria-labelledby="attachment-heading">
                  <div className="attachment-heading"><div><Paperclip size={16} /><strong id="attachment-heading">附件</strong></div><span>{attachmentsQuery.data?.attachments.length ?? 0} 个</span></div>
                  {attachmentsQuery.isPending ? <div className="attachment-empty"><LoaderCircle className="is-spinning" size={16} />正在读取附件</div> : attachmentsQuery.isError ? <div className="attachment-empty">无法读取附件</div> : (attachmentsQuery.data?.attachments.length ?? 0) === 0 ? <div className="attachment-empty">这封邮件没有可下载附件</div> : <div className="attachment-list">{attachmentsQuery.data?.attachments.map((attachment) => <a key={attachment.id} href={attachmentDownloadURL(detailQuery.data.public_id, attachment.id)}><Paperclip size={15} /><span><strong>{attachment.name || '未命名附件'}</strong><small>{formatBytes(attachment.size)}{attachment.inline ? ' · 内嵌' : ''}</small></span><Download size={16} aria-label="下载" /></a>)}</div>}
                </section>
              </>
            )}
          </article>
        </section>
      </main>
      {personalOnly && personalRulesOpen && <PersonalRulesDialog accounts={accountsQuery.data?.accounts ?? []} csrfToken={status.csrf_token ?? ''} onClose={() => setPersonalRulesOpen(false)} />}
      {deleteTarget && <DeleteMessageDialog message={deleteTarget} busy={deletingID !== ''} onClose={() => setDeleteTarget(null)} onConfirm={() => void deleteSelectedMessage()} />}
    </AppFrame>
  )
}

function AccountFilter({ accounts, loading, value, onChange }: {
  accounts: Account[]
  loading: boolean
  value: string
  onChange: (publicID: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const selected = accounts.find((account) => account.public_id === value)
  const matches = filterInboxAccounts(accounts, query)

  function choose(publicID: string) {
    onChange(publicID)
    setQuery('')
    setOpen(false)
  }

  return <div className="account-filter">
    <button className="account-filter-trigger" type="button" aria-haspopup="listbox" aria-expanded={open} onClick={() => setOpen((current) => !current)}>
      <span>{selected ? accountFilterLabel(selected) : '全部邮箱'}</span><ChevronDown size={14} />
    </button>
    {open && <div className="account-filter-popover">
      <label className="account-filter-search"><Search size={14} /><span className="visually-hidden">搜索邮箱</span><input autoFocus value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索邮箱或显示名称" /></label>
      <div className="account-filter-options" role="listbox" aria-label="邮箱筛选">
        <button type="button" role="option" aria-selected={value === ''} onClick={() => choose('')}><span><strong>全部邮箱</strong><small>不限定账号</small></span>{value === '' && <Check size={14} />}</button>
        {loading ? <div className="account-filter-empty"><LoaderCircle className="is-spinning" size={15} />正在读取账号</div>
          : matches.length === 0 ? <div className="account-filter-empty">没有匹配的邮箱</div>
            : matches.map((account) => <button key={account.public_id} type="button" role="option" aria-selected={value === account.public_id} onClick={() => choose(account.public_id)}><span><strong>{accountFilterLabel(account)}</strong><small>{accountFilterDetail(account)}</small></span>{value === account.public_id && <Check size={14} />}</button>)}
      </div>
    </div>}
  </div>
}

function MessageRow({
  message,
  selected,
  selectedForRead,
  showPreview,
  dateFormatter,
  onSelect,
  onToggleReadSelection,
  deleting,
  onDelete,
}: {
  message: MailMessage
  selected: boolean
  selectedForRead: boolean
  showPreview: boolean
  dateFormatter: Intl.DateTimeFormat
  onSelect: () => void
  onToggleReadSelection: (selected: boolean) => void
  deleting: boolean
  onDelete: () => void
}) {
  return (
    <div className={`message-row-shell ${message.unread ? 'is-unread' : ''} ${selected ? 'is-selected' : ''} ${showPreview ? '' : 'without-preview'}`}>
      {message.unread ? (
        <label className="message-read-checkbox" title="选择这封未读邮件">
          <input type="checkbox" checked={selectedForRead} onChange={(event) => onToggleReadSelection(event.target.checked)} />
          <span className="visually-hidden">选择邮件：{message.subject || '无主题'}</span>
        </label>
      ) : <span className="message-read-checkbox-spacer" aria-hidden="true" />}
      <button className="message-row" type="button" onClick={onSelect} aria-label={`${message.unread ? '未读' : '已读'}邮件：${message.subject || '无主题'}`}>
        <span className="account-stripe" aria-hidden="true" />
        <span className="message-row-top"><strong>{message.sender_name || message.sender_address || '未知发件人'}</strong><span className="message-row-meta"><ReadStateMark unread={message.unread} /><time>{dateFormatter.format(new Date(message.received_at))}</time></span></span>
        <span className="message-subject">{message.subject || '（无主题）'}</span>
        {showPreview && <span className="message-preview">{message.body_preview || '正文尚未缓存'}</span>}
        <span className="message-row-foot"><span>{message.account_name}</span><span className="message-row-foot-meta"><span>{message.folder_name}{message.flagged ? ' · 已标记' : ''}</span><span className={`category-badge category-${message.category}`}>{categoryLabels[message.category]}</span></span></span>
      </button>
      <button className="message-delete-button" type="button" title="移入 Outlook 已删除邮件" aria-label={`删除邮件：${message.subject || '无主题'}`} onClick={onDelete} disabled={deleting}>
        {deleting ? <LoaderCircle className="is-spinning" size={15} /> : <Trash2 size={15} />}
      </button>
    </div>
  )
}

function DeleteMessageDialog({
  message,
  busy,
  onClose,
  onConfirm,
}: {
  message: MailMessage
  busy: boolean
  onClose: () => void
  onConfirm: () => void
}) {
  return <div className="dialog-backdrop" role="presentation">
    <section className="dialog-panel delete-message-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-message-heading">
      <div className="dialog-heading"><div><p className="eyebrow">Move message</p><h2 id="delete-message-heading">移入已删除邮件</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭删除确认" onClick={onClose} disabled={busy}><X size={18} /></button></div>
      <div className="delete-message-summary"><span>{message.account_address}</span><strong>{message.subject || '（无主题）'}</strong><small>{message.sender_name || message.sender_address || '未知发件人'} · {message.folder_name}</small></div>
      <p className="field-hint">邮件将从本应用的收件箱中移除并移动到 Outlook“已删除邮件”。本应用不会永久删除邮件，之后仍可在 Outlook 中恢复。</p>
      <div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose} disabled={busy}>取消</button><button className="danger-button" type="button" onClick={onConfirm} disabled={busy}>{busy ? <LoaderCircle className="is-spinning" size={17} /> : <Trash2 size={17} />}移入已删除邮件</button></div>
    </section>
  </div>
}

function ReadStateMark({ unread }: { unread: boolean }) {
  return <span className={`read-state-mark ${unread ? 'is-unread' : 'is-read'}`}>{unread ? '未读' : '已读'}</span>
}

function MailHealth({ status }: { status: Awaited<ReturnType<typeof fetchMailStatus>> | undefined }) {
  if (!status) return <span className="mail-health">正在读取同步状态</span>
  const queue = status.high_priority_queue + status.background_queue
  return (
    <span className={`mail-health level-${status.disk.level}`}>
	  {status.active_accounts} 个可同步账号 · 队列 {queue} · 磁盘 {status.disk.used_percent}%
    </span>
  )
}

function MailEmpty({ icon, title, action, actionLabel = '重新读取' }: { icon: React.ReactNode; title: string; action?: () => void; actionLabel?: string }) {
  return (
    <div className="mail-empty">
      {icon}<strong>{title}</strong>
      {action && <button className="secondary-button" type="button" onClick={action}>{actionLabel}</button>}
    </div>
  )
}

function senderInitial(message: MailMessage) {
  return (message.sender_name || message.sender_address || '?').slice(0, 1).toUpperCase()
}

function accountFilterLabel(account: Account) {
  return account.display_name || account.primary_email || account.imported_email
}

function accountFilterDetail(account: Account) {
  const addresses = [account.primary_email, account.imported_email].filter((value, index, items) => value && items.indexOf(value) === index)
  return addresses.join(' · ')
}

function messageFor(error: unknown, fallback: string) {
  return error instanceof APIError ? error.message : fallback
}

function setCachedReadState(
  queryClient: QueryClient,
  publicID: string,
  unread: boolean,
) {
  queryClient.setQueriesData<{ messages: MailMessage[] }>({ queryKey: ['mail-messages'] }, (current) => {
    if (!current) return current
    return {
      messages: current.messages.map((message) => (
        message.public_id === publicID ? { ...message, unread } : message
      )),
    }
  })
  queryClient.setQueryData<MailMessageDetail>(['mail-message', publicID], (current) => (
    current ? { ...current, unread } : current
  ))
}

function setCachedReadStates(queryClient: QueryClient, publicIDs: string[], unread: boolean) {
  for (const publicID of publicIDs) setCachedReadState(queryClient, publicID, unread)
}

function setCachedFlaggedState(queryClient: QueryClient, publicID: string, flagged: boolean) {
  queryClient.setQueriesData<{ messages: MailMessage[] }>({ queryKey: ['mail-messages'] }, (current) => current ? { messages: current.messages.map((message) => message.public_id === publicID ? { ...message, flagged } : message) } : current)
  queryClient.setQueryData<MailMessageDetail>(['mail-message', publicID], (current) => current ? { ...current, flagged } : current)
}

function removeCachedMessage(queryClient: QueryClient, publicID: string) {
  queryClient.setQueriesData<{ messages: MailMessage[] }>({ queryKey: ['mail-messages'] }, (current) => (
    current ? { messages: removeItemByPublicID(current.messages, publicID) } : current
  ))
  queryClient.removeQueries({ queryKey: ['mail-message', publicID], exact: true })
  queryClient.removeQueries({ queryKey: ['mail-attachments', publicID], exact: true })
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / 1024 ** 2).toFixed(1)} MiB`
}

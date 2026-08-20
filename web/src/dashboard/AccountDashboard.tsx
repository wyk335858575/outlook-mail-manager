import { useEffect, useMemo, useState } from 'react'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  Ban,
  Check,
  ChevronLeft,
  ChevronRight,
  Clipboard,
  ExternalLink,
  FileUp,
  Globe2,
  KeyRound,
  LockKeyhole,
  LoaderCircle,
  LogOut,
  PauseCircle,
  PlayCircle,
  Pencil,
  RefreshCw,
  RotateCcw,
  Search,
  SearchCheck,
  ShieldCheck,
  Trash2,
  UsersRound,
  X,
} from 'lucide-react'

import {
  authorizationVerificationURL,
  checkAccount,
  confirmAuthorization,
  deleteAccount,
  deleteAccounts,
  fetchAccountConfig,
  fetchAccountPage,
  fetchAccountSelection,
  fetchAuthorization,
  fetchOAuthImport,
  importAccounts,
  microsoftAccountSignOutURL,
  replaceOAuthCredentials,
  restartAuthorization,
  setAccountDisabled,
  setAccountsDisabled,
  setAccountCleanupProtected,
  startAuthorization,
  startOAuthImport,
  updateAccount,
  type Account,
  type AccountAuthMethod,
  type AccountStatus,
  type OAuthImportJob,
  type BatchAccountResult,
} from '../api/accounts'
import { APIError, type AuthStatus } from '../api/auth'
import { AppFrame } from '../components/AppFrame'
import { parseOAuthCredentialImport } from './oauthImportParser'

type StatusFilter = 'all' | AccountStatus
type ImportMode = AccountAuthMethod
type PageSize = 25 | 50 | 100
type BulkAction = 'enable' | 'disable' | 'delete'

type MicrosoftAuthorizationPopup = {
  closed: boolean
  focus: () => void
  location: { href: string }
}

export function openMicrosoftAuthorizationPopup({
  verificationURL,
  openWindow,
  schedule,
  onNavigated,
}: {
  verificationURL: string
  openWindow: (url: string, target: string) => MicrosoftAuthorizationPopup | null
  schedule: (callback: () => void, delay: number) => void
  onNavigated?: () => void
}) {
  const popup = openWindow(microsoftAccountSignOutURL, 'outlook-mail-manager-microsoft-authorization')
  if (!popup) return false

  schedule(() => {
    if (!popup.closed) {
      popup.location.href = verificationURL
      popup.focus()
    }
    onNavigated?.()
  }, 4000)
  return true
}

export async function copyAuthorizationEmail(
  email: string,
  writeText: (value: string) => Promise<void> = (value) => navigator.clipboard.writeText(value),
) {
  const value = email.trim()
  if (!value) return false
  await writeText(value)
  return true
}

export function nextAuthorizationAction(
  accounts: Account[],
  microsoftConfigured: boolean,
  configPending: boolean,
  actionInProgress: boolean,
) {
  const account = accounts.find((item) =>
    item.auth_method === 'web' && (item.status === 'reauth_required' || item.status === 'pending'),
  )
  if (configPending) return { account, disabled: true, label: '正在读取配置' }
  if (!microsoftConfigured) return { account, disabled: true, label: '先配置 Client ID' }
  if (actionInProgress) return { account, disabled: true, label: '正在启动授权' }
  if (!account) return { account, disabled: true, label: '暂无待授权账号' }
  return { account, disabled: false, label: '授权下一个' }
}

export function failedBatchAccountIDs(result: BatchAccountResult) {
  return result.results.filter((item) => item.state === 'failed').map((item) => item.public_id)
}

export function accountDeleteConfirmationMatches(count: number, value: string) {
  return value === `删除 ${count} 个账号`
}

export function accountIdentityDisplay(account: Pick<Account, 'imported_email' | 'primary_email' | 'display_name'>) {
  const primaryEmail = (account.primary_email || account.imported_email).trim()
  const displayName = (account.display_name || '').trim()
  return {
    primaryEmail,
    displayName: displayName && displayName.localeCompare(primaryEmail, undefined, { sensitivity: 'accent' }) !== 0
      ? displayName
      : '',
  }
}

const statusOrder: StatusFilter[] = ['all', 'reauth_required', 'pending', 'degraded', 'active', 'disabled']

const statusLabels: Record<StatusFilter, string> = {
  all: '全部',
  pending: '待授权',
  active: '正常',
  degraded: '同步异常',
  reauth_required: '需重授权',
  disabled: '已停用',
}

const authMethodLabels: Record<AccountAuthMethod, string> = {
  web: '网页授权账号',
  oauth: 'O2 令牌账号',
}

const authMethodDescriptions: Record<AccountAuthMethod, string> = {
  web: '通过 Microsoft 网页设备码完成授权',
  oauth: '使用 Client ID 和 refresh token 进行授权',
}

const dateFormatter = new Intl.DateTimeFormat('zh-CN', {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
})

export function AccountDashboard({
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
  const [filter, setFilter] = useState<StatusFilter>('all')
  const [searchInput, setSearchInput] = useState('')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<PageSize>(50)
  const [selectedAccounts, setSelectedAccounts] = useState<Set<string>>(() => new Set())
  const [selectingAll, setSelectingAll] = useState(false)
  const [bulkAction, setBulkAction] = useState<BulkAction | null>(null)
  const [bulkBusy, setBulkBusy] = useState(false)
  const [bulkMessage, setBulkMessage] = useState('')
  const [importOpen, setImportOpen] = useState(false)
  const [importMode, setImportMode] = useState<ImportMode>('web')
  const [importData, setImportData] = useState('')
  const [overwriteExisting, setOverwriteExisting] = useState(false)
  const [importError, setImportError] = useState('')
  const [importResult, setImportResult] = useState('')
  const [importing, setImporting] = useState(false)
  const [actionID, setActionID] = useState('')
  const [actionError, setActionError] = useState('')
  const [jobID, setJobID] = useState('')
  const [confirming, setConfirming] = useState(false)
  const [copied, setCopied] = useState(false)
  const [oauthImportJobID, setOAuthImportJobID] = useState('')
  const [editingAccount, setEditingAccount] = useState<Account | null>(null)
  const [credentialAccount, setCredentialAccount] = useState<Account | null>(null)

  const accountsQuery = useQuery({
    queryKey: ['accounts', 'management', search, filter, page, pageSize],
    queryFn: ({ signal }) => fetchAccountPage({
      q: search || undefined,
      status: filter === 'all' ? undefined : filter,
      page,
      pageSize,
    }, signal),
    placeholderData: keepPreviousData,
    refetchInterval: (query) => query.state.data?.accounts.some((account) => account.authorization_in_progress)
      ? 3000
      : false,
  })
  const summaryQuery = useQuery({
    queryKey: ['accounts', 'summary'],
    queryFn: ({ signal }) => fetchAccountPage({ page: 1, pageSize: 25 }, signal),
    refetchInterval: (query) => query.state.data?.accounts.some((account) => account.authorization_in_progress)
      ? 3000
      : false,
  })
  const configQuery = useQuery({
    queryKey: ['accounts-config'],
    queryFn: ({ signal }) => fetchAccountConfig(signal),
  })
  const authorizationQuery = useQuery({
    queryKey: ['account-authorization', jobID],
    queryFn: ({ signal }) => fetchAuthorization(jobID, signal),
    enabled: jobID !== '',
    refetchInterval: (query) => {
      const state = query.state.data?.state
      return state === 'waiting' || state === 'finalizing' ? 1500 : false
    },
  })
  const oauthImportQuery = useQuery({
    queryKey: ['oauth-import', oauthImportJobID],
    queryFn: ({ signal }) => fetchOAuthImport(oauthImportJobID, signal),
    enabled: oauthImportJobID !== '',
    refetchInterval: (query) => query.state.data?.state === 'completed' ? false : 1000,
  })

  const accounts = accountsQuery.data?.accounts ?? []
  const accountsByMethod = useMemo<Record<AccountAuthMethod, Account[]>>(() => ({
    web: accounts.filter((account) => account.auth_method === 'web'),
    oauth: accounts.filter((account) => account.auth_method === 'oauth'),
  }), [accounts])
  const authMethodCounts = accountsQuery.data?.auth_method_counts ?? { web: 0, oauth: 0 }
  const searchCounts = accountsQuery.data?.status_counts
  const summaryCounts = summaryQuery.data?.status_counts
  const counts = useMemo<Record<StatusFilter, number>>(() => ({
    all: searchCounts ? Object.values(searchCounts).reduce((sum, value) => sum + value, 0) : 0,
    pending: searchCounts?.pending ?? 0,
    active: searchCounts?.active ?? 0,
    degraded: searchCounts?.degraded ?? 0,
    reauth_required: searchCounts?.reauth_required ?? 0,
    disabled: searchCounts?.disabled ?? 0,
  }), [searchCounts])
  const overviewCounts = useMemo<Record<StatusFilter, number>>(() => ({
    all: summaryCounts ? Object.values(summaryCounts).reduce((sum, value) => sum + value, 0) : 0,
    pending: summaryCounts?.pending ?? 0,
    active: summaryCounts?.active ?? 0,
    degraded: summaryCounts?.degraded ?? 0,
    reauth_required: summaryCounts?.reauth_required ?? 0,
    disabled: summaryCounts?.disabled ?? 0,
  }), [summaryCounts])
  const totalPages = Math.max(1, Math.ceil((accountsQuery.data?.total ?? 0) / pageSize))

  useEffect(() => {
    const timeout = window.setTimeout(() => {
      setSearch(searchInput.trim())
      setPage(1)
      setSelectedAccounts(new Set())
    }, 300)
    return () => window.clearTimeout(timeout)
  }, [searchInput])

  useEffect(() => {
    if (page > totalPages) setPage(totalPages)
  }, [page, totalPages])

  useEffect(() => {
    if (authorizationQuery.data?.state === 'completed') {
      void queryClient.invalidateQueries({ queryKey: ['accounts'] })
    }
  }, [authorizationQuery.data?.state, queryClient])

  useEffect(() => {
    if (oauthImportQuery.data?.state === 'completed') {
      void queryClient.invalidateQueries({ queryKey: ['accounts'] })
    }
  }, [oauthImportQuery.data?.state, queryClient])

  async function submitImport() {
    if (!status.csrf_token) return
    setImporting(true)
    setImportError('')
    setImportResult('')
    try {
      if (importMode === 'oauth') {
        const parsed = parseOAuthCredentialImport(importData)
        setImportData('')
        const job = await startOAuthImport(parsed, overwriteExisting, status.csrf_token)
        setOAuthImportJobID(job.id)
        queryClient.setQueryData(['oauth-import', job.id], job)
        setImportResult(`已提交 ${job.total} 个 O2 令牌，正在验证`)
      } else {
        const result = await importAccounts(importData, status.csrf_token)
        setImportResult(`已新增 ${result.created} 个账号，跳过 ${result.existing} 个已有账号`)
        setImportData('')
      }
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    } catch (error) {
      setImportError(messageFor(error, '导入失败，请检查邮箱、分组、标签和备注列'))
    } finally {
      setImporting(false)
    }
  }

  async function saveAccount(account: Account, input: Pick<Account, 'imported_email' | 'notes' | 'groups' | 'tags'>) {
    if (!status.csrf_token) return
    await updateAccount(account.public_id, input, status.csrf_token)
    await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    setEditingAccount(null)
  }

  async function saveCredentials(account: Account, clientID: string, refreshToken: string) {
    if (!status.csrf_token) return
    await replaceOAuthCredentials(account.public_id, clientID, refreshToken, status.csrf_token)
    await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    setCredentialAccount(null)
  }

  async function beginAuthorization(account: Account) {
    if (!status.csrf_token) return
    setActionID(account.public_id)
    setActionError('')
    setCopied(false)
    try {
      const job = await startAuthorization(account.public_id, status.csrf_token)
      setJobID(job.id)
      queryClient.setQueryData(['account-authorization', job.id], job)
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    } catch (error) {
      setActionError(messageFor(error, '无法启动 Microsoft 授权'))
    } finally {
      setActionID('')
    }
  }

  async function confirmAlias() {
    if (!status.csrf_token || !jobID) return
    setConfirming(true)
    try {
      const job = await confirmAuthorization(jobID, status.csrf_token)
      queryClient.setQueryData(['account-authorization', jobID], job)
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    } catch (error) {
      setActionError(messageFor(error, '无法确认 Microsoft 账号别名'))
    } finally {
      setConfirming(false)
    }
  }

  async function switchAuthorizationAccount() {
    if (!status.csrf_token || !jobID) return
    setConfirming(true)
    setActionError('')
    try {
      const job = await restartAuthorization(jobID, status.csrf_token)
      setJobID(job.id)
      setCopied(false)
      queryClient.setQueryData(['account-authorization', job.id], job)
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    } catch (error) {
      setJobID('')
      setActionError(messageFor(error, '无法切换 Microsoft 账号，请重新开始授权'))
    } finally {
      setConfirming(false)
    }
  }

  async function changeDisabled(account: Account, disabled: boolean) {
    if (!status.csrf_token) return
    setActionID(account.public_id)
    setActionError('')
    try {
      await setAccountDisabled(account.public_id, disabled, status.csrf_token)
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    } catch (error) {
      setActionError(messageFor(error, '无法更新账号状态'))
    } finally {
      setActionID('')
    }
  }

  async function verifyAccount(account: Account) {
    if (!status.csrf_token) return
    setActionID(account.public_id)
    setActionError('')
    try {
      await checkAccount(account.public_id, status.csrf_token)
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    } catch (error) {
      setActionError(messageFor(error, '授权检查未完成'))
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
    } finally {
      setActionID('')
    }
  }

  async function changeCleanupProtection(account: Account) {
    if (!status.csrf_token) return
    setActionID(account.public_id)
    setActionError('')
    try {
      await setAccountCleanupProtected(account.public_id, !account.cleanup_protected, status.csrf_token)
      await queryClient.invalidateQueries({ queryKey: ['accounts'] })
      await queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] })
    } catch (error) {
      setActionError(messageFor(error, '无法更新账号清理保护'))
    } finally {
      setActionID('')
    }
  }

  async function removeAccount(account: Account) {
    if (!status.csrf_token) return
    const confirmed = window.confirm(
      `确定从本管理器删除 ${account.imported_email} 吗？\n\n这会删除本地 Token、邮件缓存和同步记录，但不会删除 Microsoft 邮箱或云端邮件。`,
    )
    if (!confirmed) return
    setActionID(account.public_id)
    setActionError('')
    try {
      await deleteAccount(account.public_id, status.csrf_token)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['accounts'] }),
        queryClient.invalidateQueries({ queryKey: ['mail-messages'] }),
        queryClient.invalidateQueries({ queryKey: ['mail-status'] }),
        queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] }),
      ])
    } catch (error) {
      setActionError(messageFor(error, '无法删除账号'))
    } finally {
      setActionID('')
    }
  }

  function changeFilter(next: StatusFilter) {
    setFilter(next)
    setPage(1)
    setSelectedAccounts(new Set())
    setBulkMessage('')
  }

  function toggleAccount(publicID: string, selected: boolean) {
    setSelectedAccounts((current) => {
      const next = new Set(current)
      if (selected) next.add(publicID)
      else next.delete(publicID)
      return next
    })
    setBulkMessage('')
  }

  async function toggleAllMatching(selected: boolean) {
    if (!selected) {
      setSelectedAccounts(new Set())
      return
    }
    setSelectingAll(true)
    setActionError('')
    try {
      const result = await fetchAccountSelection({
        q: search || undefined,
        status: filter === 'all' ? undefined : filter,
      })
      setSelectedAccounts(new Set(result.public_ids))
    } catch (error) {
      setActionError(messageFor(error, '无法选择当前筛选结果'))
    } finally {
      setSelectingAll(false)
    }
  }

  async function executeBulkAction(action: BulkAction) {
    if (!status.csrf_token || selectedAccounts.size === 0) return
    setBulkBusy(true)
    setActionError('')
    setBulkMessage('')
    try {
      const ids = [...selectedAccounts]
      const result = action === 'delete'
        ? await deleteAccounts(ids, status.csrf_token)
        : await setAccountsDisabled(ids, action === 'disable', status.csrf_token)
      const failed = failedBatchAccountIDs(result)
      setSelectedAccounts(new Set(failed))
      setBulkMessage(
        `${action === 'delete' ? '删除' : action === 'disable' ? '停用' : '启用'}完成：成功 ${result.succeeded}，跳过 ${result.skipped}，失败 ${result.failed}`,
      )
      setBulkAction(null)
      if (action === 'delete') setPage(1)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['accounts'] }),
        ...(action === 'delete' ? [
          queryClient.invalidateQueries({ queryKey: ['mail-messages'] }),
          queryClient.invalidateQueries({ queryKey: ['mail-status'] }),
          queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] }),
        ] : []),
      ])
    } catch (error) {
      setActionError(messageFor(error, '批量账号操作未完成'))
    } finally {
      setBulkBusy(false)
    }
  }

  async function copyCode() {
    const code = authorizationQuery.data?.user_code
    if (!code) return
    try {
      await navigator.clipboard.writeText(code)
      setCopied(true)
    } catch {
      setActionError('无法写入剪贴板，请手动记录设备码')
    }
  }

  const microsoftConfigured = configQuery.data?.microsoft_configured === true
  const nextAuthorization = nextAuthorizationAction(
    summaryQuery.data?.accounts ?? [],
    microsoftConfigured,
    configQuery.isPending,
    actionID !== '',
  )
  const allMatchingSelected = selectedAccounts.size > 0
    && selectedAccounts.size === (accountsQuery.data?.total ?? 0)

  return (
    <AppFrame active="accounts">
      <main className="main-workspace accounts-workspace" id="accounts">
        <header className="workspace-header accounts-header">
          <div>
            <p className="eyebrow">Microsoft accounts</p>
            <h1>账号管理</h1>
          </div>
          <div className="header-actions">
            <button className="primary-button" type="button" onClick={() => setImportOpen(true)}>
              <FileUp size={17} aria-hidden="true" /> 导入账号
            </button>
            <button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}>
              <LogOut size={17} aria-hidden="true" />
              {loggingOut ? '正在退出' : '退出登录'}
            </button>
          </div>
        </header>

        {configQuery.isError ? (
          <section className="security-warning" role="alert">
            <AlertTriangle size={19} aria-hidden="true" />
            <div>
              <strong>无法读取 Microsoft 配置状态</strong>
              <span>检查服务状态后刷新页面，再开始设备码授权。</span>
            </div>
          </section>
        ) : !microsoftConfigured && !configQuery.isPending && (
          <section className="security-warning" role="status">
            <AlertTriangle size={19} aria-hidden="true" />
            <div>
              <strong>Microsoft 应用尚未配置</strong>
              <span>前往 <a href="#settings">设置</a> 填写自己的 Client ID，保存后即可开始设备码授权。</span>
            </div>
          </section>
        )}
        {(logoutError || actionError) && (
          <p className="form-error dashboard-error" role="alert">{logoutError || actionError}</p>
        )}

        <section className="account-overview" aria-label="账号队列概览">
          <div className="overview-lead">
            <span className="overview-icon" aria-hidden="true"><UsersRound size={23} /></span>
            <div><strong>{overviewCounts.all}</strong><span>已导入账号</span></div>
          </div>
          <dl className="queue-counts">
            <div><dt>待授权</dt><dd>{overviewCounts.pending}</dd></div>
            <div><dt>正常</dt><dd>{overviewCounts.active}</dd></div>
            <div><dt>同步异常</dt><dd>{overviewCounts.degraded}</dd></div>
            <div><dt>需重授权</dt><dd>{overviewCounts.reauth_required}</dd></div>
          </dl>
          <button
            className="secondary-button next-auth-button"
            type="button"
            onClick={() => nextAuthorization.account && void beginAuthorization(nextAuthorization.account)}
            disabled={nextAuthorization.disabled}
            title={nextAuthorization.disabled
              ? nextAuthorization.label
              : `授权 ${nextAuthorization.account?.imported_email}`}
          >
            <KeyRound size={16} aria-hidden="true" /> {nextAuthorization.label}
          </button>
        </section>

        <section className="accounts-section" aria-labelledby="account-list-heading">
          <div className="section-heading account-list-heading">
            <div>
              <p className="eyebrow">Authorization queue</p>
              <h2 id="account-list-heading">账号与授权队列</h2>
            </div>
            <button
              className="quiet-button refresh-list-button"
              type="button"
              title="刷新账号列表"
              aria-label="刷新账号列表"
              onClick={() => accountsQuery.refetch()}
              disabled={accountsQuery.isFetching}
            >
              <RefreshCw className={accountsQuery.isFetching ? 'is-spinning' : ''} size={16} />
            </button>
          </div>

          <div className="account-management-controls">
            <label className="account-search-field">
              <Search size={16} aria-hidden="true" />
              <span className="visually-hidden">搜索账号</span>
              <input value={searchInput} onChange={(event) => setSearchInput(event.target.value)} placeholder="搜索邮箱、名称、分组、标签或备注" />
              {searchInput && <button type="button" title="清除搜索" aria-label="清除搜索" onClick={() => setSearchInput('')}><X size={15} /></button>}
            </label>
            <label className="account-page-size">每页
              <select value={pageSize} onChange={(event) => {
                setPageSize(Number(event.target.value) as PageSize)
                setPage(1)
                setSelectedAccounts(new Set())
              }}>
                <option value={25}>25</option><option value={50}>50</option><option value={100}>100</option>
              </select>
            </label>
          </div>

          <div className="status-tabs" role="tablist" aria-label="账号状态筛选">
            {statusOrder.map((item) => (
              <button
                key={item}
                className={filter === item ? 'is-selected' : ''}
                type="button"
                role="tab"
                aria-selected={filter === item}
                onClick={() => changeFilter(item)}
              >
                {statusLabels[item]} <span>{counts[item]}</span>
              </button>
            ))}
          </div>

          <div className={`account-bulk-toolbar ${selectedAccounts.size > 0 ? 'has-selection' : ''}`}>
            <label className="account-select-all">
              <input
                type="checkbox"
                checked={allMatchingSelected}
                disabled={(accountsQuery.data?.total ?? 0) === 0 || selectingAll}
                onChange={(event) => void toggleAllMatching(event.target.checked)}
              />
              <span>{selectingAll ? '正在选择' : `全选当前筛选结果（${accountsQuery.data?.total ?? 0}）`}</span>
            </label>
            {selectedAccounts.size > 0 && <>
              <strong>已选 {selectedAccounts.size} 个</strong>
              <span className="account-bulk-actions">
                <button className="secondary-button" type="button" onClick={() => setBulkAction('enable')}><PlayCircle size={16} />启用</button>
                <button className="secondary-button" type="button" onClick={() => setBulkAction('disable')}><PauseCircle size={16} />停用</button>
                <button className="danger-button" type="button" onClick={() => setBulkAction('delete')}><Trash2 size={16} />删除</button>
              </span>
            </>}
          </div>
          {bulkMessage && <p className="form-success account-bulk-result" role="status"><Check size={15} />{bulkMessage}</p>}

          {accountsQuery.isPending ? (
            <div className="account-empty" aria-live="polite"><LoaderCircle className="is-spinning" size={22} /> 正在读取账号</div>
          ) : accountsQuery.isError ? (
            <div className="account-empty error-copy"><AlertTriangle size={22} /> 无法读取账号列表</div>
          ) : accounts.length === 0 ? (
            <div className="account-empty">
              <UsersRound size={24} />
              <strong>{overviewCounts.all === 0 ? '尚未导入账号' : '当前筛选没有账号'}</strong>
            </div>
          ) : (
            <div className="account-type-sections">
              {(['web', 'oauth'] as AccountAuthMethod[]).map((method) => {
                const items = accountsByMethod[method]
                if (items.length === 0) return null
                const icon = method === 'web' ? <Globe2 size={17} aria-hidden="true" /> : <KeyRound size={17} aria-hidden="true" />
                return (
                  <section className={`account-type-section account-type-${method}`} key={method} aria-labelledby={`account-type-${method}`}>
                    <div className="account-type-heading">
                      <div className="account-type-title">
                        <span className="account-type-icon">{icon}</span>
                        <div>
                          <h3 id={`account-type-${method}`}>{authMethodLabels[method]}</h3>
                          <p>{authMethodDescriptions[method]}</p>
                        </div>
                      </div>
                      <strong>{authMethodCounts[method] ?? items.length} 个</strong>
                    </div>
                    <div className="account-table">
                      <div className="account-table-head" aria-hidden="true">
                        <span></span><span>账号</span><span>组织</span><span>状态</span><span>最近同步</span><span>操作</span>
                      </div>
                      {items.map((account) => (
                        <AccountRow
                          key={account.public_id}
                          account={account}
                          selected={selectedAccounts.has(account.public_id)}
                          busy={actionID === account.public_id}
                          microsoftConfigured={microsoftConfigured}
                          onAuthorize={() => void beginAuthorization(account)}
                          onCheck={() => void verifyAccount(account)}
                          onCleanupProtection={() => void changeCleanupProtection(account)}
                          onDisabled={(disabled) => void changeDisabled(account, disabled)}
                          onEdit={() => setEditingAccount(account)}
                          onCredentials={() => setCredentialAccount(account)}
                          onDelete={() => void removeAccount(account)}
                          onSelection={(selected) => toggleAccount(account.public_id, selected)}
                        />
                      ))}
                    </div>
                  </section>
                )
              })}
            </div>
          )}
          {(accountsQuery.data?.total ?? 0) > 0 && <nav className="account-pagination" aria-label="账号分页">
            <span>第 {page} / {totalPages} 页 · 共 {accountsQuery.data?.total ?? 0} 个</span>
            <div>
              <button className="icon-command" type="button" title="上一页" aria-label="上一页" disabled={page <= 1 || accountsQuery.isFetching} onClick={() => setPage((current) => Math.max(1, current - 1))}><ChevronLeft size={17} /></button>
              <button className="icon-command" type="button" title="下一页" aria-label="下一页" disabled={page >= totalPages || accountsQuery.isFetching} onClick={() => setPage((current) => Math.min(totalPages, current + 1))}><ChevronRight size={17} /></button>
            </div>
          </nav>}
        </section>
      </main>

      {importOpen && (
        <div className="dialog-backdrop" role="presentation">
          <section className="dialog-panel import-dialog" role="dialog" aria-modal="true" aria-labelledby="import-heading">
            <div className="dialog-heading">
              <div><p className="eyebrow">Account import</p><h2 id="import-heading">导入账号</h2></div>
              <button className="field-icon-button" type="button" aria-label="关闭导入" title="关闭" onClick={() => setImportOpen(false)}>
                <X size={18} />
              </button>
            </div>
            <div className="settings-segmented import-mode-tabs" role="tablist" aria-label="导入方式">
              <button type="button" role="tab" aria-selected={importMode === 'web'} className={importMode === 'web' ? 'is-selected' : ''} onClick={() => { setImportMode('web'); setImportError(''); setImportResult(''); setOAuthImportJobID('') }}>网页授权账号</button>
              <button type="button" role="tab" aria-selected={importMode === 'oauth'} className={importMode === 'oauth' ? 'is-selected' : ''} onClick={() => { setImportMode('oauth'); setImportError(''); setImportResult('') }}>O2 令牌账号</button>
            </div>
            <label className="field-label" htmlFor="account-import">{importMode === 'web' ? '邮箱、分组、标签、备注' : '邮箱、密码、Client ID、refresh token'}</label>
            <textarea
              id="account-import"
              value={importData}
              onChange={(event) => setImportData(event.target.value)}
              placeholder={importMode === 'web'
                ? 'email,group,tags,notes\nuser@outlook.com,Personal,verification|important,Primary'
                : 'user@outlook.com----password----client_id----refresh_token'}
              spellCheck={false}
              autoFocus
            />
            {importMode === 'web' ? (
              <p className="field-hint">每行一个账号；标签用 | 分隔，不接受邮箱密码。</p>
            ) : (
              <>
                <p className="field-hint">支持 ----、Tab、逗号和分号。密码仅在本浏览器中识别后丢弃；同邮箱的网页授权账号会自动补充 O2 令牌。</p>
                <label className="inline-check"><input type="checkbox" checked={overwriteExisting} onChange={(event) => setOverwriteExisting(event.target.checked)} />覆盖已经授权账号的 O2 令牌</label>
              </>
            )}
            {importError && <p className="form-error" role="alert">{importError}</p>}
            {importResult && <p className="form-success" role="status"><Check size={15} /> {importResult}</p>}
            {importMode === 'oauth' && oauthImportQuery.data && (
              <OAuthImportProgress job={oauthImportQuery.data} />
            )}
            <div className="form-actions dialog-actions">
              <button className="secondary-button" type="button" onClick={() => setImportOpen(false)}>关闭</button>
              <button className="primary-button" type="button" onClick={() => void submitImport()} disabled={importing || !importData.trim()}>
                {importing ? <LoaderCircle className="is-spinning" size={17} /> : <FileUp size={17} />}
                导入账号
              </button>
            </div>
          </section>
        </div>
      )}

      {editingAccount && (
        <AccountEditDialog account={editingAccount} onClose={() => setEditingAccount(null)} onSave={saveAccount} />
      )}

      {credentialAccount && (
        <CredentialDialog account={credentialAccount} onClose={() => setCredentialAccount(null)} onSave={saveCredentials} />
      )}

      {bulkAction && (
        <BatchAccountDialog
          action={bulkAction}
          count={selectedAccounts.size}
          busy={bulkBusy}
          onClose={() => setBulkAction(null)}
          onConfirm={() => void executeBulkAction(bulkAction)}
        />
      )}

      {jobID && (
        <AuthorizationDialog
          authorization={authorizationQuery.data}
          loading={authorizationQuery.isPending}
          failed={authorizationQuery.isError}
          copied={copied}
          confirming={confirming}
          onCopy={() => void copyCode()}
          onConfirm={() => void confirmAlias()}
          onSwitch={() => void switchAuthorizationAccount()}
          onClose={() => {
            setJobID('')
            setCopied(false)
          }}
        />
      )}
    </AppFrame>
  )
}

function AccountRow({
  account,
  selected,
  busy,
  microsoftConfigured,
  onAuthorize,
  onCheck,
  onCleanupProtection,
  onDisabled,
  onEdit,
  onCredentials,
  onDelete,
  onSelection,
}: {
  account: Account
  selected: boolean
  busy: boolean
  microsoftConfigured: boolean
  onAuthorize: () => void
  onCheck: () => void
  onCleanupProtection: () => void
  onDisabled: (disabled: boolean) => void
  onEdit: () => void
  onCredentials: () => void
  onDelete: () => void
  onSelection: (selected: boolean) => void
}) {
  const needsAuthorization = account.status === 'pending' || account.status === 'reauth_required'
  const identity = accountIdentityDisplay(account)
  const labels = [...account.groups.map((value) => ({ value, kind: 'group' })), ...account.tags.map((value) => ({ value, kind: 'tag' }))]

  return (
    <div className={`account-row status-${account.status}`}>
      <label className="account-row-select" title={`选择 ${account.imported_email}`}>
        <input type="checkbox" checked={selected} onChange={(event) => onSelection(event.target.checked)} />
        <span className="visually-hidden">选择账号 {account.imported_email}</span>
      </label>
      <div className="account-identity">
        <div><strong>{identity.primaryEmail}</strong>{identity.displayName && <span>{identity.displayName}</span>}{account.notes && <small>{account.notes}</small>}</div>
      </div>
      <div className="account-labels">
        {labels.length === 0 ? <span className="muted-value">未分组</span> : labels.map((label) => (
          <span key={`${label.kind}-${label.value}`} className={`label-chip ${label.kind}`}>{label.value}</span>
        ))}
        {account.cleanup_protected && <span className="label-chip protection"><ShieldCheck size={12} />清理保护</span>}
      </div>
      <div className="account-status-cell">
        <span className={`account-status status-${account.status}`}>{statusLabels[account.status]}</span>
        {(account.reauth_reason || account.last_oauth_error || account.last_sync_error) && <small>{account.reauth_reason || account.last_oauth_error || account.last_sync_error}</small>}
      </div>
      <div className="account-refresh">
        <strong>{formatDate(account.last_sync_success_at || account.last_refresh_success_at)}</strong>
        <span>{account.authorization_in_progress
          ? '授权进行中'
          : account.last_sync_error
            ? `同步失败 ${account.sync_failures} 次`
            : account.last_sync_success_at
              ? account.sync_backlog > 0 ? `积压 ${account.sync_backlog}` : '邮件同步成功'
              : account.last_graph_success_at ? 'Graph 已验证' : '尚未验证'}</span>
      </div>
      <div className="account-actions">
        <button className="icon-command" type="button" title="编辑账号资料" aria-label="编辑账号资料" onClick={onEdit} disabled={busy}>
          <Pencil size={17} />
        </button>
        <button className="icon-command" type="button" title="替换 O2 令牌" aria-label="替换 O2 令牌" onClick={onCredentials} disabled={busy}>
          <LockKeyhole size={17} />
        </button>
        {account.status === 'disabled' ? (
          <button className="icon-command" type="button" title="启用账号" aria-label="启用账号" onClick={() => onDisabled(false)} disabled={busy}>
            {busy ? <LoaderCircle className="is-spinning" size={17} /> : <PlayCircle size={17} />}
          </button>
        ) : (
          <>
            {account.auth_method === 'web' && (
              <button
                className="icon-command primary-command"
                type="button"
                title={needsAuthorization ? '开始网页授权' : '重新网页授权'}
                aria-label={needsAuthorization ? '开始网页授权' : '重新网页授权'}
                onClick={onAuthorize}
                disabled={busy || !microsoftConfigured}
              >
                {busy ? <LoaderCircle className="is-spinning" size={17} /> : needsAuthorization ? <KeyRound size={17} /> : <RotateCcw size={17} />}
              </button>
            )}
            {(account.status === 'active' || account.status === 'degraded') && (
              <button className="icon-command" type="button" title="检查授权" aria-label="检查授权" onClick={onCheck} disabled={busy}>
                <SearchCheck size={17} />
              </button>
            )}
            <button className={`icon-command ${account.cleanup_protected ? 'is-active' : ''}`} type="button" title={account.cleanup_protected ? '关闭清理保护' : '开启清理保护'} aria-label={account.cleanup_protected ? '关闭清理保护' : '开启清理保护'} onClick={onCleanupProtection} disabled={busy}>
              <ShieldCheck size={17} />
            </button>
            <button className="icon-command" type="button" title="停用账号" aria-label="停用账号" onClick={() => onDisabled(true)} disabled={busy}>
              <PauseCircle size={17} />
            </button>
          </>
        )}
        <button className="icon-command danger-command" type="button" title="删除账号" aria-label="删除账号" onClick={onDelete} disabled={busy}>
          <Trash2 size={17} />
        </button>
      </div>
    </div>
  )
}

function BatchAccountDialog({
  action,
  count,
  busy,
  onClose,
  onConfirm,
}: {
  action: BulkAction
  count: number
  busy: boolean
  onClose: () => void
  onConfirm: () => void
}) {
  const [confirmation, setConfirmation] = useState('')
  const deleting = action === 'delete'
  const expected = `删除 ${count} 个账号`
  const title = deleting ? '批量删除账号' : action === 'disable' ? '批量停用账号' : '批量启用账号'
  const description = deleting
    ? '将删除这些账号在本管理器中的 Token、邮件缓存和同步记录，不会删除 Microsoft 邮箱或云端邮件。'
    : action === 'disable'
      ? '停用后将暂停这些账号的授权任务和邮件同步，现有本地数据仍会保留。'
      : '已有 O2 令牌的账号将恢复同步并在后台限并发校验；授权实际失效时才会要求重新授权。'
  const confirmed = !deleting || accountDeleteConfirmationMatches(count, confirmation)

  return <div className="dialog-backdrop" role="presentation">
    <section className="dialog-panel batch-account-dialog" role="dialog" aria-modal="true" aria-labelledby="batch-account-heading">
      <div className="dialog-heading"><div><p className="eyebrow">Bulk account action</p><h2 id="batch-account-heading">{title}</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭批量操作" onClick={onClose} disabled={busy}><X size={18} /></button></div>
      <div className={`batch-account-impact ${deleting ? 'is-danger' : ''}`}><strong>{count}</strong><span>个账号将被处理</span></div>
      <p className="field-hint">{description}</p>
      {deleting && <label className="field-label">输入“{expected}”确认
        <input autoFocus value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="off" />
      </label>}
      <div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose} disabled={busy}>取消</button><button className={deleting ? 'danger-button' : 'primary-button'} type="button" onClick={onConfirm} disabled={busy || !confirmed}>{busy ? <LoaderCircle className="is-spinning" size={17} /> : deleting ? <Trash2 size={17} /> : action === 'disable' ? <PauseCircle size={17} /> : <PlayCircle size={17} />}{title}</button></div>
    </section>
  </div>
}

function OAuthImportProgress({ job }: { job: OAuthImportJob }) {
  const processed = job.state === 'completed' ? job.total : job.items.filter((item) => item.state !== 'queued' && item.state !== 'running').length
  return (
    <div className="oauth-import-progress" role="status">
      <div><strong>{job.state === 'completed' ? '验证完成' : '正在验证 O2 令牌'}</strong><span>{processed} / {job.total}</span></div>
      <progress max={job.total} value={processed} />
      {job.state === 'completed' && <p>新增 {job.created} · 更新 {job.updated} · 跳过 {job.skipped} · 失败 {job.failed}</p>}
      {job.items.some((item) => item.state === 'failed') && (
        <div className="oauth-import-errors">
          {job.items.filter((item) => item.state === 'failed').slice(0, 8).map((item) => <span key={item.row}>第 {item.row} 行 {item.email}：{item.message}</span>)}
        </div>
      )}
    </div>
  )
}

function AccountEditDialog({
  account,
  onClose,
  onSave,
}: {
  account: Account
  onClose: () => void
  onSave: (account: Account, input: Pick<Account, 'imported_email' | 'notes' | 'groups' | 'tags'>) => Promise<void>
}) {
  const [email, setEmail] = useState(account.imported_email)
  const [notes, setNotes] = useState(account.notes)
  const [groups, setGroups] = useState(account.groups.join(', '))
  const [tags, setTags] = useState(account.tags.join(', '))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function submit() {
    setSaving(true)
    setError('')
    try {
      await onSave(account, {
        imported_email: email.trim(),
        notes: notes.trim(),
        groups: splitNames(groups),
        tags: splitNames(tags),
      })
    } catch (reason) {
      setError(messageFor(reason, '无法保存账号资料'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="dialog-backdrop" role="presentation">
      <section className="dialog-panel account-edit-dialog" role="dialog" aria-modal="true" aria-labelledby="account-edit-heading">
        <div className="dialog-heading"><div><p className="eyebrow">Account metadata</p><h2 id="account-edit-heading">编辑账号</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭编辑" onClick={onClose}><X size={18} /></button></div>
        <div className="readonly-identity"><span>Microsoft 主邮箱</span><strong>{account.primary_email || '尚未授权'}</strong><small>显示名称：{account.display_name || '尚未获取'} · 稳定账号标识不可编辑</small></div>
        <label className="field-label">导入邮箱<input value={email} onChange={(event) => setEmail(event.target.value)} /></label>
        <div className="readonly-identity"><span>账号类型</span><strong>{authMethodLabels[account.auth_method]}</strong><small>{authMethodDescriptions[account.auth_method]}</small></div>
        <label className="field-label">分组<input value={groups} onChange={(event) => setGroups(event.target.value)} placeholder="多个分组用逗号分隔" /></label>
        <label className="field-label">标签<input value={tags} onChange={(event) => setTags(event.target.value)} placeholder="多个标签用逗号分隔" /></label>
        <label className="field-label">备注<textarea value={notes} onChange={(event) => setNotes(event.target.value)} maxLength={500} /></label>
        {error && <p className="form-error" role="alert">{error}</p>}
        <div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button><button className="primary-button" type="button" onClick={() => void submit()} disabled={saving || !email.trim()}>{saving ? <LoaderCircle className="is-spinning" size={17} /> : <Check size={17} />}保存</button></div>
      </section>
    </div>
  )
}

function CredentialDialog({
  account,
  onClose,
  onSave,
}: {
  account: Account
  onClose: () => void
  onSave: (account: Account, clientID: string, refreshToken: string) => Promise<void>
}) {
  const [clientID, setClientID] = useState('')
  const [refreshToken, setRefreshToken] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function submit() {
    setSaving(true)
    setError('')
    try {
      await onSave(account, clientID.trim(), refreshToken.trim())
      setRefreshToken('')
    } catch (reason) {
      setError(messageFor(reason, '无法验证或替换 O2 令牌'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="dialog-backdrop" role="presentation">
      <section className="dialog-panel account-edit-dialog" role="dialog" aria-modal="true" aria-labelledby="credential-heading">
        <div className="dialog-heading"><div><p className="eyebrow">O2 token account</p><h2 id="credential-heading">替换 O2 令牌</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭 O2 令牌替换" onClick={onClose}><X size={18} /></button></div>
        <p className="field-hint">目标账号：{account.imported_email}。新凭据通过 Microsoft 验证成功后才会原子替换，失败不会清除当前有效 token。</p>
        <label className="field-label">Client ID<input value={clientID} onChange={(event) => setClientID(event.target.value)} autoComplete="off" /></label>
        <label className="field-label">refresh token<textarea value={refreshToken} onChange={(event) => setRefreshToken(event.target.value)} autoComplete="off" spellCheck={false} /></label>
        {error && <p className="form-error" role="alert">{error}</p>}
        <div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button><button className="primary-button" type="button" onClick={() => void submit()} disabled={saving || !clientID.trim() || !refreshToken.trim()}>{saving ? <LoaderCircle className="is-spinning" size={17} /> : <LockKeyhole size={17} />}验证并替换</button></div>
      </section>
    </div>
  )
}

function splitNames(value: string) {
  return value.split(/[，,]/).map((item) => item.trim()).filter(Boolean)
}

function AuthorizationDialog({
  authorization,
  loading,
  failed,
  copied,
  confirming,
  onCopy,
  onConfirm,
  onSwitch,
  onClose,
}: {
  authorization: Awaited<ReturnType<typeof fetchAuthorization>> | undefined
  loading: boolean
  failed: boolean
  copied: boolean
  confirming: boolean
  onCopy: () => void
  onConfirm: () => void
  onSwitch: () => void
  onClose: () => void
}) {
  const [openingMicrosoft, setOpeningMicrosoft] = useState(false)
  const [openError, setOpenError] = useState('')
  const [emailCopied, setEmailCopied] = useState(false)
  const active = authorization?.state === 'waiting' || authorization?.state === 'finalizing'
  const verificationURL = authorization ? authorizationVerificationURL(authorization) : ''

  function openMicrosoftLogin() {
    if (!verificationURL) return
    setOpenError('')
    onCopy()
    const opened = openMicrosoftAuthorizationPopup({
      verificationURL,
      openWindow: (url, target) => window.open(url, target),
      schedule: (callback, delay) => { window.setTimeout(callback, delay) },
      onNavigated: () => setOpeningMicrosoft(false),
    })
    if (!opened) {
      setOpenError('浏览器阻止了授权窗口，请允许此站点打开弹窗后重试')
      return
    }
    setOpeningMicrosoft(true)
  }

  async function copyEmail() {
    if (!authorization?.imported_email) return
    try {
      await copyAuthorizationEmail(authorization.imported_email)
      setEmailCopied(true)
    } catch {
      setOpenError('无法复制邮箱地址，请手动复制')
    }
  }

  return (
    <div className="dialog-backdrop" role="presentation">
      <section className="dialog-panel authorization-dialog" role="dialog" aria-modal="true" aria-labelledby="authorization-heading">
        <div className="dialog-heading">
          <div><p className="eyebrow">Device authorization</p><h2 id="authorization-heading">Microsoft 授权</h2></div>
          <button className="field-icon-button" type="button" aria-label="关闭授权窗口" title="关闭" onClick={onClose}>
            <X size={18} />
          </button>
        </div>
        {loading ? (
          <div className="authorization-state"><LoaderCircle className="is-spinning" size={24} /> 正在创建设备码</div>
        ) : failed || !authorization ? (
          <div className="authorization-state error-copy"><Ban size={24} /> 无法读取授权任务</div>
        ) : authorization.state === 'confirmation_required' ? (
          <>
            <div className="alias-comparison">
              <div><span>导入地址</span><strong>{authorization.imported_email}</strong></div>
              <div><span>Microsoft 返回</span><strong>{authorization.microsoft_email}</strong><small>{authorization.display_name}</small></div>
            </div>
            <p className="dialog-copy">两个地址不一致。确认它们属于同一 Microsoft 账号后再完成绑定。</p>
            <div className="form-actions dialog-actions">
              <button className="secondary-button" type="button" onClick={onSwitch} disabled={confirming}>
                {confirming && <LoaderCircle className="is-spinning" size={17} />} 切换账号并生成新码
              </button>
              <button className="primary-button" type="button" onClick={onConfirm} disabled={confirming}>
                {confirming && <LoaderCircle className="is-spinning" size={17} />} 确认并绑定
              </button>
            </div>
          </>
        ) : authorization.state === 'completed' ? (
          <div className="authorization-complete">
            <span><Check size={24} /></span>
            <h3>账号授权完成</h3>
            <p>Token 已加密保存，账号已进入正常状态。</p>
            <button className="primary-button" type="button" onClick={onClose}>完成</button>
          </div>
        ) : authorization.state === 'failed' || authorization.state === 'expired' ? (
          <div className="authorization-complete authorization-failed">
            <span><AlertTriangle size={24} /></span>
            <h3>{authorization.state === 'expired' ? '设备码已过期' : '授权未完成'}</h3>
            <p>{authorization.message || '请关闭后重新开始授权。'}</p>
            <button className="secondary-button" type="button" onClick={onClose}>关闭</button>
          </div>
        ) : (
          <>
            <div className="authorization-target">
              <span>本次授权账号</span>
              <div>
                <strong>{authorization.imported_email}</strong>
                <button className="field-icon-button" type="button" title="复制邮箱地址" aria-label="复制邮箱地址" onClick={() => void copyEmail()}>
                  {emailCopied ? <Check size={16} /> : <Clipboard size={16} />}
                </button>
              </div>
            </div>
            <div className="device-code-panel">
              <span>设备码</span>
              <code>{authorization.user_code}</code>
              <button className="quiet-button" type="button" onClick={onCopy}>
                {copied ? <Check size={16} /> : <Clipboard size={16} />} {copied ? '已复制' : '复制设备码'}
              </button>
            </div>
            <button className="primary-button verification-link" type="button" onClick={openMicrosoftLogin} disabled={openingMicrosoft || !active}>
              {openingMicrosoft ? <LoaderCircle className="is-spinning" size={17} /> : <ExternalLink size={17} />}
              {openingMicrosoft ? '正在重置 Microsoft 登录' : '复制设备码并打开登录页'}
            </button>
            <p className="authorization-account-warning"><strong>将先结束浏览器中的旧 Microsoft 会话。</strong>约 4 秒后自动打开登录页，直接登录上方目标邮箱。</p>
            {openError && <p className="form-error" role="alert">{openError}</p>}
            <div className="authorization-waiting" role="status">
              <LoaderCircle className={active ? 'is-spinning' : ''} size={17} />
              <span>{authorization.message || '等待完成授权'}</span>
            </div>
          </>
        )}
      </section>
    </div>
  )
}

function formatDate(value?: string) {
  if (!value) return '尚无记录'
  return dateFormatter.format(new Date(value))
}

function messageFor(error: unknown, fallback: string) {
  return error instanceof APIError ? error.message : fallback
}

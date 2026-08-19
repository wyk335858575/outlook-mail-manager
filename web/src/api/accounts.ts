import { apiRequest } from './auth'

export type AccountStatus = 'pending' | 'active' | 'degraded' | 'reauth_required' | 'disabled'

export type Account = {
  public_id: string
  imported_email: string
  primary_email?: string
  display_name?: string
  notes: string
  status: AccountStatus
  reauth_reason?: string
  last_oauth_error?: string
  consecutive_failures: number
  next_retry_at?: string
  last_refresh_success_at?: string
  last_graph_success_at?: string
  last_sync_success_at?: string
  last_sync_error?: string
  sync_failures: number
  sync_next_retry_at?: string
  sync_backlog: number
  groups: string[]
  tags: string[]
  cleanup_protected: boolean
  authorization_in_progress: boolean
}

export type AccountConfig = {
  microsoft_configured: boolean
  client_id: string
  updated_at?: string
}

export type AccountStatusCounts = Record<AccountStatus, number>

export type AccountListResponse = {
  accounts: Account[]
  total: number
  page: number
  page_size: number
  status_counts: AccountStatusCounts
}

export type BatchAccountItemResult = {
  public_id: string
  state: 'updated' | 'deleted' | 'skipped' | 'failed'
  status?: AccountStatus
  error?: string
}

export type BatchAccountResult = {
  requested: number
  succeeded: number
  skipped: number
  failed: number
  results: BatchAccountItemResult[]
}

export type Authorization = {
  id: string
  account_public_id: string
  imported_email: string
  state: 'waiting' | 'confirmation_required' | 'finalizing' | 'completed' | 'failed' | 'expired'
  user_code?: string
  verification_uri?: string
  verification_uri_complete?: string
  expires_at: string
  microsoft_email?: string
  display_name?: string
  error_code?: string
  message?: string
}

export type OAuthImportInput = {
  email: string
  client_id: string
  refresh_token: string
}

export type OAuthImportItem = {
  row: number
  email: string
  state: 'queued' | 'running' | 'created' | 'updated' | 'skipped' | 'failed'
  error_code?: string
  message?: string
}

export type OAuthImportJob = {
  id: string
  state: 'queued' | 'running' | 'completed'
  total: number
  processed: number
  created: number
  updated: number
  skipped: number
  failed: number
  created_at: string
  updated_at: string
  completed_at?: string
  items: OAuthImportItem[]
}

const microsoftDeviceLoginURL = 'https://www.microsoft.com/link'
const microsoftDeviceLoginHosts = new Set(['microsoft.com', 'www.microsoft.com'])
const microsoftDeviceLoginPaths = new Set(['/devicelogin', '/link'])
export const microsoftAccountSignOutURL = 'https://login.microsoftonline.com/consumers/oauth2/v2.0/logout'

export function fetchAccounts(signal?: AbortSignal) {
  return apiRequest<{ accounts: Account[] }>('/api/accounts', { signal })
}

export function fetchAccountPage(input: {
  q?: string
  status?: AccountStatus
  page: number
  pageSize: 25 | 50 | 100
}, signal?: AbortSignal) {
  const query = new URLSearchParams({ page: String(input.page), page_size: String(input.pageSize) })
  if (input.q) query.set('q', input.q)
  if (input.status) query.set('status', input.status)
  return apiRequest<AccountListResponse>(`/api/accounts?${query.toString()}`, { signal })
}

export function fetchAccountSelection(input: { q?: string; status?: AccountStatus }, signal?: AbortSignal) {
  const query = new URLSearchParams()
  if (input.q) query.set('q', input.q)
  if (input.status) query.set('status', input.status)
  const suffix = query.size > 0 ? `?${query.toString()}` : ''
  return apiRequest<{ public_ids: string[]; total: number }>(`/api/accounts/selection${suffix}`, { signal })
}

export function fetchAccountConfig(signal?: AbortSignal) {
  return apiRequest<AccountConfig>('/api/accounts/config', { signal })
}

export function updateAccountConfig(clientID: string, csrfToken: string) {
  return apiRequest<AccountConfig>('/api/accounts/config', {
    method: 'PUT',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ client_id: clientID }),
  })
}

export function importAccounts(data: string, csrfToken: string) {
  return apiRequest<{ created: number; existing: number }>('/api/accounts/import', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ data }),
  })
}

export function startOAuthImport(accounts: OAuthImportInput[], overwriteExisting: boolean, csrfToken: string) {
  return apiRequest<OAuthImportJob>('/api/accounts/oauth-imports', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ accounts, overwrite_existing: overwriteExisting }),
  })
}

export function fetchOAuthImport(jobID: string, signal?: AbortSignal) {
  return apiRequest<OAuthImportJob>(`/api/accounts/oauth-imports/${encodeURIComponent(jobID)}`, { signal })
}

export function updateAccount(publicID: string, input: Pick<Account, 'imported_email' | 'notes' | 'groups' | 'tags'>, csrfToken: string) {
  return apiRequest<Account>(`/api/accounts/${encodeURIComponent(publicID)}`, {
    method: 'PUT',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export function replaceOAuthCredentials(publicID: string, clientID: string, refreshToken: string, csrfToken: string) {
  return apiRequest<void>(`/api/accounts/${encodeURIComponent(publicID)}/oauth-credentials`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ client_id: clientID, refresh_token: refreshToken }),
  })
}

export function startAuthorization(publicID: string, csrfToken: string) {
  return apiRequest<Authorization>(`/api/accounts/${encodeURIComponent(publicID)}/oauth/start`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function fetchAuthorization(jobID: string, signal?: AbortSignal) {
  return apiRequest<Authorization>(`/api/accounts/oauth/${encodeURIComponent(jobID)}`, { signal })
}

export function authorizationVerificationURL(authorization: Authorization) {
  const candidate = authorization.verification_uri?.trim()
  if (!candidate) return microsoftDeviceLoginURL

  try {
    const url = new URL(candidate)
    const path = url.pathname.replace(/\/+$/, '').toLowerCase()
    const host = url.hostname.toLowerCase()
    if (url.protocol === 'https:' && microsoftDeviceLoginHosts.has(host) && microsoftDeviceLoginPaths.has(path)) {
      return `${url.origin}${path}`
    }
  } catch {
    // Fall through to the known Microsoft device login page.
  }
  return microsoftDeviceLoginURL
}

export function confirmAuthorization(jobID: string, csrfToken: string) {
  return apiRequest<Authorization>(`/api/accounts/oauth/${encodeURIComponent(jobID)}/confirm`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function restartAuthorization(jobID: string, csrfToken: string) {
  return apiRequest<Authorization>(`/api/accounts/oauth/${encodeURIComponent(jobID)}/restart`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function setAccountDisabled(publicID: string, disabled: boolean, csrfToken: string) {
  return apiRequest<void>(`/api/accounts/${encodeURIComponent(publicID)}/status`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ disabled }),
  })
}

export function setAccountsDisabled(publicIDs: string[], disabled: boolean, csrfToken: string) {
  return apiRequest<BatchAccountResult>('/api/accounts/batch/status', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ public_ids: publicIDs, disabled }),
  })
}

export function checkAccount(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/accounts/${encodeURIComponent(publicID)}/check`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function deleteAccount(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/accounts/${encodeURIComponent(publicID)}`, {
    method: 'DELETE',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function deleteAccounts(publicIDs: string[], csrfToken: string) {
  return apiRequest<BatchAccountResult>('/api/accounts/batch/delete', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ public_ids: publicIDs, confirm: 'DELETE_LOCAL_ACCOUNTS' }),
  })
}

export function setAccountCleanupProtected(publicID: string, protectedValue: boolean, csrfToken: string) {
  return apiRequest<void>(`/api/accounts/${encodeURIComponent(publicID)}/cleanup-protection`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ protected: protectedValue }),
  })
}

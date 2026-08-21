import { apiRequest } from './auth'

export type APIScope = 'accounts:read' | 'mail:read' | 'otp:read' | 'system:read'

export type APIToken = {
  public_id: string
  name: string
  prefix: string
  scopes: APIScope[]
  all_accounts: boolean
  account_public_ids: string[]
  group_names: string[]
  ip_cidrs: string[]
  expires_at?: string
  last_used_at?: string
  revoked_at?: string
  created_at: string
}

export type CreatedAPIToken = APIToken & { secret: string }

export function fetchAPITokens(signal?: AbortSignal) {
  return apiRequest<{ tokens: APIToken[] }>('/api/api-tokens', { signal })
}

export function createAPIToken(input: {
  name: string
  scopes: APIScope[]
  all_accounts: boolean
  account_public_ids: string[]
  group_names: string[]
  ip_cidrs: string[]
  expires_at?: string
}, csrfToken: string) {
  return apiRequest<CreatedAPIToken>('/api/api-tokens', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify(input),
  })
}

export function revokeAPIToken(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/api-tokens/${encodeURIComponent(publicID)}/revoke`, {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function deleteAPIToken(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/api-tokens/${encodeURIComponent(publicID)}`, {
    method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
  })
}

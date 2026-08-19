import { apiRequest } from './auth'

export type MailCategory = 'important' | 'verification' | 'marketing' | 'spam' | 'normal' | 'uncertain'

export type ClassificationRule = {
  public_id: string
  name: string
  match_field: 'sender' | 'domain' | 'subject' | 'body'
  match_operator: 'equals' | 'contains'
  match_value: string
  target_category?: MailCategory
  protects_cleanup: boolean
  priority: number
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export type CleanupState = 'candidate' | 'dismissed' | 'holding' | 'restored' | 'deleted' | 'failed'

export type CleanupItem = {
  public_id: string
  state: CleanupState
  candidate_reason: string
  message_public_id: string
  account_public_id: string
  account_name: string
  subject: string
  sender_name: string
  sender_address: string
  category: MailCategory
  received_at: string
  approved_at?: string
  execute_after?: string
  moved_at?: string
  restored_at?: string
  completed_at?: string
  last_error?: string
  attempt_count: number
}

export type AuditEvent = {
  public_id: string
  event_type: string
  actor: string
  entity_type: string
  entity_public_id: string
  detail: Record<string, unknown>
  created_at: string
}

export function fetchClassificationRules(signal?: AbortSignal) {
  return apiRequest<{ rules: ClassificationRule[] }>('/api/classification/rules', { signal })
}

export function createClassificationRule(rule: Omit<ClassificationRule, 'public_id' | 'created_at' | 'updated_at'>, csrfToken: string) {
  return apiRequest<ClassificationRule>('/api/classification/rules', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify(rule),
  })
}

export function deleteClassificationRule(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/classification/rules/${encodeURIComponent(publicID)}`, {
    method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function reclassifyMessages(csrfToken: string) {
  return apiRequest<{ reclassified: boolean }>('/api/classification/reclassify', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function correctMessageCategory(publicID: string, category: MailCategory, csrfToken: string) {
  return apiRequest<void>(`/api/mail/messages/${encodeURIComponent(publicID)}/category`, {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify({ category }),
  })
}

export function setMessageFlagged(publicID: string, flagged: boolean, csrfToken: string) {
  return apiRequest<void>(`/api/mail/messages/${encodeURIComponent(publicID)}/flag`, {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify({ flagged }),
  })
}

export function fetchCleanupActions(state: CleanupState | 'all' = 'all', signal?: AbortSignal) {
  return apiRequest<{ actions: CleanupItem[] }>(`/api/cleanup?state=${state}`, { signal })
}

export function approveCleanup(publicIDs: string[], csrfToken: string) {
  return apiRequest<{ results: Array<{ public_id: string; item?: CleanupItem; error?: string }> }>('/api/cleanup/approve', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ public_ids: publicIDs, confirm: 'MOVE_TO_HOLDING' }),
  })
}

export function restoreCleanup(publicID: string, csrfToken: string) {
  return apiRequest<CleanupItem>(`/api/cleanup/${encodeURIComponent(publicID)}/restore`, {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify({ confirm: 'RESTORE' }),
  })
}

export function retryCleanup(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/cleanup/${encodeURIComponent(publicID)}/retry`, {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function fetchAuditEvents(signal?: AbortSignal) {
  return apiRequest<{ events: AuditEvent[] }>('/api/audit', { signal })
}

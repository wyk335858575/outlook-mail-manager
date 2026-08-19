import { apiRequest } from './auth'
import type { MailCategory } from './classification'

export type MailFolder = 'inbox' | 'junkemail'

export type MailMessage = {
  public_id: string
  account_public_id: string
  account_name: string
  account_address: string
  folder: MailFolder
  folder_name: string
  subject: string
  sender_name: string
  sender_address: string
  received_at: string
  unread: boolean
  flagged: boolean
  body_preview: string
  body_truncated: boolean
  synced_at?: string
  category: MailCategory
  classification_reason: string
  cleanup_protected: boolean
}

export type MailMessageDetail = MailMessage & {
  body_text: string
  body_cached_at?: string
}

export type MailAttachment = {
  id: string
  name: string
  content_type: string
  size: number
  inline: boolean
}

export type MailStatus = {
  disk: {
    used_percent: number
    level: 'normal' | 'warning' | 'critical' | 'metadata_only'
    metadata_only: boolean
    checked_at: string
  }
  high_priority_queue: number
  background_queue: number
  active_accounts: number
}

export type MarkMessagesReadResult = {
  updated: number
  results: Array<{
    public_id: string
    read: boolean
    error?: string
  }>
}

export function fetchMessages(input: {
  q?: string
  account?: string
  folder?: MailFolder
  category?: MailCategory
  unread?: boolean
  personal?: boolean
  limit?: number
}, signal?: AbortSignal) {
  const query = new URLSearchParams()
  if (input.q) query.set('q', input.q)
  if (input.account) query.set('account', input.account)
  if (input.folder) query.set('folder', input.folder)
  if (input.category) query.set('category', input.category)
  if (input.unread !== undefined) query.set('unread', String(input.unread))
  if (input.personal !== undefined) query.set('personal', String(input.personal))
  if (input.limit) query.set('limit', String(input.limit))
  const suffix = query.size > 0 ? `?${query.toString()}` : ''
  return apiRequest<{ messages: MailMessage[] }>(`/api/mail/messages${suffix}`, { signal })
}

export function fetchMessage(publicID: string, signal?: AbortSignal) {
  return apiRequest<MailMessageDetail>(`/api/mail/messages/${encodeURIComponent(publicID)}`, { signal })
}

export function markMessageRead(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/mail/messages/${encodeURIComponent(publicID)}/read`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function markMessagesRead(publicIDs: string[], csrfToken: string) {
  return apiRequest<MarkMessagesReadResult>('/api/mail/messages/read', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ public_ids: publicIDs }),
  })
}

export function moveMessageToDeletedItems(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/mail/messages/${encodeURIComponent(publicID)}/delete`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ confirm: 'MOVE_TO_DELETED_ITEMS' }),
  })
}

export function messageHTMLURL(publicID: string, loadImages: boolean) {
  return `/api/mail/messages/${encodeURIComponent(publicID)}/html?images=${loadImages ? '1' : '0'}`
}

export function fetchAttachments(publicID: string, signal?: AbortSignal) {
  return apiRequest<{ attachments: MailAttachment[] }>(`/api/mail/messages/${encodeURIComponent(publicID)}/attachments`, { signal })
}

export function attachmentDownloadURL(publicID: string, attachmentID: string) {
  return `/api/mail/messages/${encodeURIComponent(publicID)}/attachments/${encodeURIComponent(attachmentID)}`
}

export function fetchMailStatus(signal?: AbortSignal) {
  return apiRequest<MailStatus>('/api/mail/status', { signal })
}

export function syncMail(csrfToken: string, account = '') {
  return apiRequest<{ queued?: number; synced?: number }>('/api/mail/sync', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ account }),
  })
}

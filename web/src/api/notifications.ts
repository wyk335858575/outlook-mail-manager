import { apiRequest } from './auth'

export type NotificationKind = 'telegram' | 'pushplus' | 'wxpush' | 'bark'

export type NotificationChannel = {
  public_id: string
  name: string
  kind: NotificationKind
  enabled: boolean
  system_enabled: boolean
  configured: boolean
  destination: string
  needs_reconfiguration: boolean
  created_at: string
  updated_at: string
}

export type NotificationChannelInput = {
  name: string
  kind: NotificationKind
  enabled: boolean
  system_enabled: boolean
  telegram_bot_token?: string
  telegram_chat_id?: string
  pushplus_token?: string
  pushplus_topic?: string
  wxpush_app_id?: string
  wxpush_app_secret?: string
  wxpush_user_id?: string
  wxpush_template_id?: string
  bark_server_url?: string
  bark_device_key?: string
  bark_group?: string
  bark_sound?: string
}

export type NotificationRule = {
  public_id?: string
  channel_public_id: string
  channel_name?: string
  name: string
  enabled: boolean
  personal_only: boolean
  account_public_ids: string[]
  group_names: string[]
  tag_names: string[]
  categories: string[]
  sender_address: string
  sender_domain: string
  subject_keywords: string[]
  start_minute: number
  end_minute: number
  require_otp: boolean
  include_otp: boolean
  created_at?: string
  updated_at?: string
}

export type NotificationDelivery = {
  public_id: string
  channel_name: string
  event_type: string
  status: string
  attempt_count: number
  last_error?: string
  created_at: string
  sent_at?: string
}

export function fetchNotificationChannels(signal?: AbortSignal) {
  return apiRequest<{ channels: NotificationChannel[] }>('/api/notifications/channels', { signal })
}

export function createNotificationChannel(input: NotificationChannelInput, csrfToken: string) {
  return apiRequest<NotificationChannel>('/api/notifications/channels', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify(input),
  })
}

export function deleteNotificationChannel(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/notifications/channels/${encodeURIComponent(publicID)}`, {
    method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function testNotificationChannel(publicID: string, csrfToken: string) {
  return apiRequest<NotificationDelivery>(`/api/notifications/channels/${encodeURIComponent(publicID)}/test`, {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function testNotificationConfig(input: NotificationChannelInput, csrfToken: string) {
  return apiRequest<{ status: 'sent' }>('/api/notifications/channels/test-config', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify(input),
  })
}

export function fetchNotificationRules(signal?: AbortSignal) {
  return apiRequest<{ rules: NotificationRule[] }>('/api/notifications/rules', { signal })
}

export function createNotificationRule(input: NotificationRule, csrfToken: string) {
  return apiRequest<NotificationRule>('/api/notifications/rules', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify(input),
  })
}

export function updateNotificationRule(publicID: string, input: NotificationRule, csrfToken: string) {
  return apiRequest<NotificationRule>(`/api/notifications/rules/${encodeURIComponent(publicID)}`, {
    method: 'PUT', headers: { 'X-CSRF-Token': csrfToken }, body: JSON.stringify(input),
  })
}

export function deleteNotificationRule(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/notifications/rules/${encodeURIComponent(publicID)}`, {
    method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function fetchNotificationDeliveries(signal?: AbortSignal) {
  return apiRequest<{ deliveries: NotificationDelivery[] }>('/api/notifications/deliveries', { signal })
}

export function retryNotificationDelivery(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/notifications/deliveries/${encodeURIComponent(publicID)}/retry`, {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
  })
}

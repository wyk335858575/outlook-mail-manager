import { apiRequest } from './auth'

export type ReaderMode = 'text' | 'html'
export type DefaultFolder = 'all' | 'inbox' | 'junkemail'

export type AppSettings = {
  sync_interval_seconds: 5 | 60 | 300 | 600 | 900 | 1800 | 3600
  initial_sync_days: 7 | 14 | 30 | 60 | 90
  body_cache_kib: 64 | 128 | 256 | 512 | 1024
  message_page_size: 25 | 50 | 100 | 200
  timezone: string
  reader_mode: ReaderMode
  mark_read_on_open: boolean
  default_folder: DefaultFolder
  default_unread_only: boolean
  auto_select_first_message: boolean
  show_body_preview: boolean
  updated_at: string
}

export function fetchSettings(signal?: AbortSignal) {
  return apiRequest<AppSettings>('/api/settings', { signal })
}

export function updateSettings(settings: AppSettings, csrfToken: string) {
  return apiRequest<AppSettings>('/api/settings', {
    method: 'PUT',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(settings),
  })
}

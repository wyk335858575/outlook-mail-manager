import { apiRequest } from './auth'
import type { MailStatus } from './mail'

export type Backup = {
  name: string
  size_bytes: number
  sha256: string
  created_at: string
}

export type MaintenanceStatus = {
  database_ok: boolean
  database_size_bytes: number
  schema_version: number
  backup_count: number
  latest_backup?: Backup
  failed_notifications: number
  cleanup_failures: number
  checked_at: string
}

export type UpdateStatus = {
  current_version: string
  latest_version?: string
  release_notes?: string
  release_url?: string
  checked_at: string
  configured: boolean
  update_available: boolean
  updater_available: boolean
  can_update: boolean
  reason?: string
}

export type UpdateJob = {
  id: string
  state: string
  version?: string
  message?: string
  error?: string
  created_at: string
  updated_at: string
  completed_at?: string
}

export function fetchDetailedHealth(signal?: AbortSignal) {
  return apiRequest<{ maintenance: MaintenanceStatus; mail: MailStatus }>('/api/health/detail', { signal })
}

export function fetchBackups(signal?: AbortSignal) {
  return apiRequest<{ backups: Backup[] }>('/api/backups', { signal })
}

export function createBackup(csrfToken: string) {
  return apiRequest<Backup>('/api/backups', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function deleteBackup(name: string, csrfToken: string) {
  return apiRequest<void>(`/api/backups/${encodeURIComponent(name)}`, {
    method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function fetchUpdateStatus(signal?: AbortSignal) {
  return apiRequest<UpdateStatus>('/api/update/status', { signal })
}

export function startUpdate(csrfToken: string) {
  return apiRequest<UpdateJob>('/api/update/jobs', {
    method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
  })
}

export function fetchUpdateJob(jobID: string, signal?: AbortSignal) {
  return apiRequest<UpdateJob>(`/api/update/jobs/${encodeURIComponent(jobID)}`, { signal })
}

import { apiRequest } from './auth'
import type { MailCategory } from './classification'

export type PersonalInboxRule = {
  public_id?: string
  name: string
  enabled: boolean
  account_public_ids: string[]
  group_names: string[]
  tag_names: string[]
  categories: MailCategory[]
  sender_address: string
  sender_domain: string
  subject_keywords: string[]
  require_otp: boolean
  created_at?: string
  updated_at?: string
}

export function fetchPersonalInboxRules(signal?: AbortSignal) {
  return apiRequest<{ rules: PersonalInboxRule[] }>('/api/personal-inbox/rules', { signal })
}

export function createPersonalInboxRule(input: PersonalInboxRule, csrfToken: string) {
  return apiRequest<PersonalInboxRule>('/api/personal-inbox/rules', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export function updatePersonalInboxRule(publicID: string, input: PersonalInboxRule, csrfToken: string) {
  return apiRequest<PersonalInboxRule>(`/api/personal-inbox/rules/${encodeURIComponent(publicID)}`, {
    method: 'PUT',
    headers: { 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export function deletePersonalInboxRule(publicID: string, csrfToken: string) {
  return apiRequest<void>(`/api/personal-inbox/rules/${encodeURIComponent(publicID)}`, {
    method: 'DELETE',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

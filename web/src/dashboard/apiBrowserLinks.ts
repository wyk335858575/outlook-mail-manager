export type BrowserAPIEndpoint = 'accounts' | 'messages' | 'message' | 'otp' | 'health'

export type BrowserAPILinkInput = {
  baseURL: string
  endpoint: BrowserAPIEndpoint
  token: string
  params?: Record<string, string>
}

const messageParameters = ['q', 'account', 'group', 'tag', 'category', 'folder', 'sender', 'unread', 'limit', 'cursor']
const otpParameters = ['account', 'wait_seconds', 'sender', 'subject']

export function buildBrowserAPILink(input: BrowserAPILinkInput) {
  const token = input.token.trim()
  const params = input.params ?? {}
  if (!token) return ''

  let path = '/api/v1/accounts'
  let allowedParameters: string[] = []
  if (input.endpoint === 'messages') {
    path = '/api/v1/messages'
    allowedParameters = messageParameters
  } else if (input.endpoint === 'message') {
    const publicID = params.public_id?.trim()
    if (!publicID) return ''
    path = `/api/v1/messages/${encodeURIComponent(publicID)}`
  } else if (input.endpoint === 'otp') {
    if (!params.account?.trim()) return ''
    path = '/api/v1/otp/latest'
    allowedParameters = otpParameters
  } else if (input.endpoint === 'health') {
    path = '/api/v1/health'
  }

  const url = new URL(`${input.baseURL.replace(/\/$/, '')}${path}`)
  for (const name of allowedParameters) {
    const value = params[name]?.trim()
    if (value) url.searchParams.set(name, value)
  }
  url.searchParams.set('access_token', token)
  return url.toString()
}

export function maskBrowserAPILink(value: string) {
  if (!value) return ''
  const url = new URL(value)
  if (url.searchParams.has('access_token')) url.searchParams.set('access_token', 'omm_...')
  return url.toString()
}

export async function copyBrowserAPILink(value: string, writeText: (value: string) => Promise<void> = (text) => navigator.clipboard.writeText(text)) {
  if (!value) return false
  try {
    await writeText(value)
    return true
  } catch {
    return false
  }
}

export function openBrowserAPILink(value: string, openWindow: (url: string, target: string, features: string) => unknown = (url, target, features) => window.open(url, target, features)) {
  if (!value) return false
  openWindow(value, '_blank', 'noopener,noreferrer')
  return true
}

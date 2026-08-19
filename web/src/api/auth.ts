export type AuthStatus = {
  initialized: boolean
  authenticated: boolean
  username?: string
  csrf_token?: string
  session_expires_at?: string
}

export type SetupStartResult = {
  challenge_id: string
  secret: string
  provisioning_uri: string
  expires_at: string
}

export type AuthenticationResult = {
  csrf_token: string
  session_expires_at: string
}

export class APIError extends Error {
  readonly code: string
  readonly status: number

  constructor(code: string, message: string, status: number) {
    super(message)
    this.name = 'APIError'
    this.code = code
    this.status = status
  }
}

export function fetchAuthStatus(signal?: AbortSignal) {
  return apiRequest<AuthStatus>('/api/auth/status', { signal })
}

export function startSetup(input: {
  username: string
  password: string
  passwordConfirmation: string
}) {
  return apiRequest<SetupStartResult>('/api/auth/setup/start', {
    method: 'POST',
    body: JSON.stringify({
      username: input.username,
      password: input.password,
      password_confirmation: input.passwordConfirmation,
    }),
  })
}

export function completeSetup(input: {
  challengeID: string
  passcode: string
}) {
  return apiRequest<AuthenticationResult>('/api/auth/setup/complete', {
    method: 'POST',
    body: JSON.stringify({
      challenge_id: input.challengeID,
      passcode: input.passcode,
    }),
  })
}

export function login(input: {
  username: string
  password: string
  passcode: string
}) {
  return apiRequest<AuthenticationResult>('/api/auth/login', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function logout(csrfToken: string) {
  return apiRequest<void>('/api/auth/logout', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export async function apiRequest<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...options,
    cache: 'no-store',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(options.body ? { 'Content-Type': 'application/json' } : {}),
      ...options.headers,
    },
  })

  if (response.status === 204) {
    return undefined as T
  }

  const body: unknown = await response.json().catch(() => null)
  if (!response.ok) {
    const error = readError(body)
    throw new APIError(error.code, error.message, response.status)
  }
  return body as T
}

function readError(value: unknown): { code: string; message: string } {
  if (
    typeof value === 'object' && value !== null && 'error' in value &&
    typeof value.error === 'object' && value.error !== null &&
    'code' in value.error && typeof value.error.code === 'string' &&
    'message' in value.error && typeof value.error.message === 'string'
  ) {
    return { code: value.error.code, message: value.error.message }
  }
  return { code: 'unexpected_response', message: '服务返回了无法识别的响应' }
}

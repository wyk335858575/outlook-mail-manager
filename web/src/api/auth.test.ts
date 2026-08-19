import { afterEach, describe, expect, it, vi } from 'vitest'

import { APIError, completeSetup, fetchAuthStatus, login, startSetup } from './auth'

describe('auth API', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('reads the unauthenticated status', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      initialized: true,
      authenticated: false,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))

    await expect(fetchAuthStatus()).resolves.toMatchObject({
      initialized: true,
      authenticated: false,
    })
  })

  it('uses account, password, and authenticator setup endpoints', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        challenge_id: 'challenge',
        secret: 'TOTPSECRET',
        provisioning_uri: 'otpauth://totp/example',
        expires_at: '2026-08-17T08:10:00Z',
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        csrf_token: 'csrf',
        session_expires_at: '2026-08-17T20:00:00Z',
      }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await startSetup({
      username: 'admin.root',
      password: 'correct horse battery staple',
      passwordConfirmation: 'correct horse battery staple',
    })
    await completeSetup({ challengeID: 'challenge', passcode: '123456' })

    expect(fetchMock.mock.calls[0]).toEqual([
      '/api/auth/setup/start',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          username: 'admin.root',
          password: 'correct horse battery staple',
          password_confirmation: 'correct horse battery staple',
        }),
      }),
    ])
    expect(fetchMock.mock.calls[1]).toEqual([
      '/api/auth/setup/complete',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ challenge_id: 'challenge', passcode: '123456' }),
      }),
    ])
  })

  it('preserves the server error code and message', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: 'invalid_credentials', message: '密码或验证信息无效' },
    }), { status: 401, headers: { 'Content-Type': 'application/json' } })))

    await expect(login({ username: 'admin', password: 'wrong', passcode: '000000' }))
      .rejects.toEqual(expect.objectContaining<Partial<APIError>>({
        code: 'invalid_credentials',
        status: 401,
      }))
  })
})

import { describe, expect, it, vi } from 'vitest'

import { buildBrowserAPILink, copyBrowserAPILink, maskBrowserAPILink, openBrowserAPILink } from './apiBrowserLinks'

describe('browser API links', () => {
  it('builds direct links for endpoints without parameters', () => {
    expect(buildBrowserAPILink({ baseURL: 'https://mail.example.com/', endpoint: 'accounts', token: 'omm_secret' }))
      .toBe('https://mail.example.com/api/v1/accounts?access_token=omm_secret')
    expect(buildBrowserAPILink({ baseURL: 'https://mail.example.com', endpoint: 'health', token: 'omm_secret' }))
      .toBe('https://mail.example.com/api/v1/health?access_token=omm_secret')
  })

  it('encodes supported message filters and ignores unsupported parameters', () => {
    const link = buildBrowserAPILink({
      baseURL: 'https://mail.example.com', endpoint: 'messages', token: 'omm_secret',
      params: { q: 'invoice + alpha', account: 'user+alias@example.com', unread: 'true', limit: '20', since: 'ignored', until: 'ignored', unsupported: 'ignored' },
    })
    const url = new URL(link)

    expect(url.pathname).toBe('/api/v1/messages')
    expect(url.searchParams.get('q')).toBe('invoice + alpha')
    expect(url.searchParams.get('account')).toBe('user+alias@example.com')
    expect(url.searchParams.get('unread')).toBe('true')
    expect(url.searchParams.get('limit')).toBe('20')
    expect(url.searchParams.has('since')).toBe(false)
    expect(url.searchParams.has('until')).toBe(false)
    expect(url.searchParams.has('unsupported')).toBe(false)
    expect(url.searchParams.get('access_token')).toBe('omm_secret')
  })

  it('requires and escapes the message public ID', () => {
    expect(buildBrowserAPILink({ baseURL: 'https://mail.example.com', endpoint: 'message', token: 'omm_secret' })).toBe('')
    expect(buildBrowserAPILink({
      baseURL: 'https://mail.example.com', endpoint: 'message', token: 'omm_secret', params: { public_id: 'msg/a b' },
    })).toBe('https://mail.example.com/api/v1/messages/msg%2Fa%20b?access_token=omm_secret')
  })

  it('requires the OTP account and ignores the legacy timestamp', () => {
    expect(buildBrowserAPILink({
      baseURL: 'https://mail.example.com', endpoint: 'otp', token: 'omm_secret', params: { account: 'user@example.com' },
    })).toBe('https://mail.example.com/api/v1/otp/latest?account=user%40example.com&access_token=omm_secret')

    const link = buildBrowserAPILink({
      baseURL: 'https://mail.example.com', endpoint: 'otp', token: 'omm_secret',
      params: { account: 'user+alias@example.com', after: '2026-08-21T00:00:00Z', wait_seconds: '30', subject: 'verification code' },
    })
    const url = new URL(link)
    expect(url.pathname).toBe('/api/v1/otp/latest')
    expect(url.searchParams.get('account')).toBe('user+alias@example.com')
    expect(url.searchParams.has('after')).toBe(false)
    expect(url.searchParams.get('wait_seconds')).toBe('30')
    expect(url.searchParams.get('subject')).toBe('verification code')
  })

  it('does not build a link without a token', () => {
    expect(buildBrowserAPILink({ baseURL: 'https://mail.example.com', endpoint: 'accounts', token: '  ' })).toBe('')
  })

  it('masks the token in the visible link preview', () => {
    const preview = maskBrowserAPILink('https://mail.example.com/api/v1/accounts?access_token=omm_very_secret')
    expect(preview).toContain('access_token=omm_...')
    expect(preview).not.toContain('very_secret')
  })

  it('copies and opens only complete links', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    const openWindow = vi.fn()
    const link = 'https://mail.example.com/api/v1/accounts?access_token=omm_secret'

    await expect(copyBrowserAPILink(link, writeText)).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith(link)
    expect(openBrowserAPILink(link, openWindow)).toBe(true)
    expect(openWindow).toHaveBeenCalledWith(link, '_blank', 'noopener,noreferrer')
    expect(openBrowserAPILink('', openWindow)).toBe(false)
  })
})


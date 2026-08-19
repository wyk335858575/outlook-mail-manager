import { afterEach, describe, expect, it, vi } from 'vitest'

import { fetchHealth } from './health'

describe('fetchHealth', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('accepts the minimal readiness response', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ status: 'ok' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))

    await expect(fetchHealth()).resolves.toEqual({ status: 'ok' })
  })

  it('rejects unavailable and malformed responses', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ status: 'unavailable' }), {
        status: 503,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))

    await expect(fetchHealth()).rejects.toThrow('status 503')
  })
})

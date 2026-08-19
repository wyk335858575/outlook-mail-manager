import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAPIToken } from './apiTokens'
import { approveCleanup } from './classification'
import { markMessagesRead } from './mail'
import { fetchDetailedHealth } from './maintenance'

describe('operations APIs', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('sends the explicit cleanup confirmation with CSRF protection', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ results: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await approveCleanup(['cleanup_1'], 'csrf-value')
    expect(fetchMock).toHaveBeenCalledWith('/api/cleanup/approve', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      body: JSON.stringify({ public_ids: ['cleanup_1'], confirm: 'MOVE_TO_HOLDING' }),
    }))
  })

  it('sends up to five hundred bulk operations in one API request', async () => {
    const publicIDs = Array.from({ length: 500 }, (_, index) => `item_${index}`)
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ updated: 500, results: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ results: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await markMessagesRead(publicIDs, 'csrf-value')
    await approveCleanup(publicIDs, 'csrf-value')

    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string).public_ids).toHaveLength(500)
    expect(JSON.parse(fetchMock.mock.calls[1][1].body as string).public_ids).toHaveLength(500)
  })

  it('creates a scoped API token without browser caching', async () => {
    const response = { public_id: 'token_1', name: 'reader', prefix: 'omm_example', scopes: ['mail:read'], account_public_ids: ['acc_1'], group_names: [], ip_cidrs: [], created_at: '2026-08-17T12:00:00Z', secret: 'omm_secret' }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)
    await createAPIToken({ name: 'reader', scopes: ['mail:read'], account_public_ids: ['acc_1'], group_names: [], ip_cidrs: [] }, 'csrf-value')
    expect(fetchMock).toHaveBeenCalledWith('/api/api-tokens', expect.objectContaining({ cache: 'no-store', credentials: 'same-origin' }))
  })

  it('reads detailed health without browser caching', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ maintenance: {}, mail: {} }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await fetchDetailedHealth()
    expect(fetchMock).toHaveBeenCalledWith('/api/health/detail', expect.objectContaining({ cache: 'no-store' }))
  })
})

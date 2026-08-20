import { afterEach, describe, expect, it, vi } from 'vitest'

import { fetchSettings, type AppSettings, updateSettings } from './settings'

const settings: AppSettings = {
  sync_interval_seconds: 5,
  initial_sync_days: 30,
  body_cache_kib: 256,
  message_page_size: 100,
  timezone: 'Asia/Shanghai',
  reader_mode: 'text',
  mark_read_on_open: true,
  default_folder: 'all',
  default_unread_only: false,
  auto_select_first_message: true,
  show_body_preview: true,
  updated_at: '2026-08-17T12:00:00Z',
}

describe('settings API', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('loads settings without browser caching', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(settings), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(fetchSettings()).resolves.toEqual(settings)
    expect(fetchMock).toHaveBeenCalledWith('/api/settings', expect.objectContaining({
      cache: 'no-store',
      credentials: 'same-origin',
    }))
  })

  it('updates settings with CSRF protection', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(settings), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await updateSettings(settings, 'csrf-value')

    expect(fetchMock).toHaveBeenCalledWith('/api/settings', expect.objectContaining({
      method: 'PUT',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      body: JSON.stringify(settings),
    }))
  })
})

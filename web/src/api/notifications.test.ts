import { afterEach, describe, expect, it, vi } from 'vitest'

import { testNotificationConfig, type NotificationChannelInput } from './notifications'

describe('notification API', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('tests unsaved WXPush credentials with CSRF protection', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'sent' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)
    const input: NotificationChannelInput = {
      name: 'WXPush', kind: 'wxpush', enabled: true, system_enabled: true,
      wxpush_app_id: 'app-id', wxpush_app_secret: 'app-secret', wxpush_user_id: 'open-id', wxpush_template_id: 'template-id',
    }

    await expect(testNotificationConfig(input, 'csrf-value')).resolves.toEqual({ status: 'sent' })
    expect(fetchMock).toHaveBeenCalledWith('/api/notifications/channels/test-config', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      body: JSON.stringify(input),
    }))
    expect(JSON.stringify(input)).not.toContain('wxpush_url')
    expect(JSON.stringify(input)).not.toContain('wxpush_token')
  })
})

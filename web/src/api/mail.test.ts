import { afterEach, describe, expect, it, vi } from 'vitest'

import { fetchMessage, fetchMessages, markMessageRead, markMessagesRead, messageHTMLURL, moveMessageToDeletedItems, syncMail } from './mail'

describe('mail API', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('encodes search filters and message ids', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ messages: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ public_id: 'msg/1', body_text: '' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }))
    vi.stubGlobal('fetch', fetchMock)

    await fetchMessages({ q: 'invoice alpha', folder: 'inbox', category: 'verification', unread: true, personal: true, limit: 25 })
    await fetchMessage('msg/1')

    expect(fetchMock.mock.calls[0][0]).toBe('/api/mail/messages?q=invoice+alpha&folder=inbox&category=verification&unread=true&personal=true&limit=25')
    expect(fetchMock.mock.calls[1][0]).toBe('/api/mail/messages/msg%2F1')
  })

  it('adds CSRF protection to manual synchronization', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ queued: 1 }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await syncMail('csrf-value')

    expect(fetchMock).toHaveBeenCalledWith('/api/mail/sync', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      body: JSON.stringify({ account: '' }),
    }))
  })

  it('adds CSRF protection when marking a message read', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await markMessageRead('msg/1', 'csrf-value')

    expect(fetchMock).toHaveBeenCalledWith('/api/mail/messages/msg%2F1/read', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
    }))
  })

  it('sends selected message ids when marking messages read in bulk', async () => {
    const result = { updated: 2, results: [{ public_id: 'msg/1', read: true }, { public_id: 'msg_2', read: true }] }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(result), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(markMessagesRead(['msg/1', 'msg_2'], 'csrf-value')).resolves.toEqual(result)
    expect(fetchMock).toHaveBeenCalledWith('/api/mail/messages/read', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      body: JSON.stringify({ public_ids: ['msg/1', 'msg_2'] }),
    }))
  })

  it('requires an explicit CSRF-protected move to Deleted Items', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await moveMessageToDeletedItems('msg/1', 'csrf-value')

    expect(fetchMock).toHaveBeenCalledWith('/api/mail/messages/msg%2F1/delete', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      body: JSON.stringify({ confirm: 'MOVE_TO_DELETED_ITEMS' }),
    }))
  })

  it('builds an encoded, explicit remote-image URL', () => {
    expect(messageHTMLURL('msg/1', false)).toBe('/api/mail/messages/msg%2F1/html?images=0')
    expect(messageHTMLURL('msg/1', true)).toBe('/api/mail/messages/msg%2F1/html?images=1')
  })
})

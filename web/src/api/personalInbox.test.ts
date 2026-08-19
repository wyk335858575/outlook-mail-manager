import { afterEach, describe, expect, it, vi } from 'vitest'

import { createPersonalInboxRule, deletePersonalInboxRule, updatePersonalInboxRule, type PersonalInboxRule } from './personalInbox'

const rule: PersonalInboxRule = {
  public_id: 'personal_1',
  name: '付款邮件',
  enabled: true,
  account_public_ids: ['acc_1'],
  group_names: [],
  tag_names: [],
  categories: ['important'],
  sender_address: '',
  sender_domain: '',
  subject_keywords: ['payment'],
  require_otp: false,
}

describe('personal inbox API', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('uses CSRF protection for create, update and delete', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(rule), { status: 201, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...rule, enabled: false }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await createPersonalInboxRule(rule, 'csrf-value')
    await updatePersonalInboxRule('personal_1', { ...rule, enabled: false }, 'csrf-value')
    await deletePersonalInboxRule('personal_1', 'csrf-value')

    for (const call of fetchMock.mock.calls) {
      expect(call[1]).toEqual(expect.objectContaining({
        headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      }))
    }
  })
})

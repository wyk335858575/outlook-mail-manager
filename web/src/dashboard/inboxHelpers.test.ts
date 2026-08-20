import { describe, expect, it } from 'vitest'

import type { Account } from '../api/accounts'
import { filterInboxAccounts, inboxRefreshInterval, removeItemByPublicID } from './inboxHelpers'

const accounts = [
  { public_id: 'acc_1', imported_email: 'imported@outlook.com', auth_method: 'web', primary_email: 'primary@outlook.com', display_name: 'Finance Box' },
  { public_id: 'acc_2', imported_email: 'other@hotmail.com', auth_method: 'oauth', display_name: 'Other' },
] as Account[]

describe('inbox helpers', () => {
  it('searches imported address, Microsoft primary address and display name', () => {
    expect(filterInboxAccounts(accounts, 'imported')).toEqual([accounts[0]])
    expect(filterInboxAccounts(accounts, 'PRIMARY')).toEqual([accounts[0]])
    expect(filterInboxAccounts(accounts, 'finance')).toEqual([accounts[0]])
    expect(filterInboxAccounts(accounts, '')).toEqual(accounts)
  })

  it('refreshes every five seconds in five-second mode and caps other modes at one minute', () => {
    expect(inboxRefreshInterval(5)).toBe(5_000)
    expect(inboxRefreshInterval(60)).toBe(60_000)
    expect(inboxRefreshInterval(600)).toBe(60_000)
  })

  it('removes a moved message from every cached list without changing other rows', () => {
    const messages = [{ public_id: 'msg_1' }, { public_id: 'msg_2' }]
    expect(removeItemByPublicID(messages, 'msg_1')).toEqual([{ public_id: 'msg_2' }])
  })
})

import { describe, expect, it } from 'vitest'

import type { Account } from '../api/accounts'
import { filterPersonalRuleAccounts } from './personalRuleAccounts'

function account(index: number): Account {
  return {
    public_id: `acc_${index}`,
    imported_email: `imported-${index}@outlook.com`,
    auth_method: 'web',
    primary_email: `primary-${index}@hotmail.com`,
    display_name: `Mailbox ${index}`,
    notes: '', status: 'active', consecutive_failures: 0, sync_failures: 0, sync_backlog: 0,
    groups: [], tags: [], cleanup_protected: false, authorization_in_progress: false,
  }
}

describe('personal inbox account search', () => {
  const accounts = Array.from({ length: 1000 }, (_, index) => account(index))

  it('renders only the first twenty accounts before searching', () => {
    expect(filterPersonalRuleAccounts(accounts, '')).toHaveLength(20)
  })

  it('searches imported email, primary email, and display name', () => {
    expect(filterPersonalRuleAccounts(accounts, 'imported-712')).toHaveLength(1)
    expect(filterPersonalRuleAccounts(accounts, 'primary-406')[0]?.public_id).toBe('acc_406')
    expect(filterPersonalRuleAccounts(accounts, 'Mailbox 999')[0]?.public_id).toBe('acc_999')
  })
})

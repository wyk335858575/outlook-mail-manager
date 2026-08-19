import type { Account } from '../api/accounts'

export function filterPersonalRuleAccounts(accounts: Account[], query: string, limit = 20) {
  const normalized = query.trim().toLocaleLowerCase()
  const matches = normalized === '' ? accounts : accounts.filter((account) => [
    account.imported_email,
    account.primary_email ?? '',
    account.display_name ?? '',
  ].some((value) => value.toLocaleLowerCase().includes(normalized)))
  return matches.slice(0, Math.max(0, limit))
}

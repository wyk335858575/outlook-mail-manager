import type { Account } from '../api/accounts'

export function filterInboxAccounts(accounts: Account[], query: string) {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return accounts
  return accounts.filter((account) => [account.imported_email, account.primary_email, account.display_name]
    .some((value) => value?.toLocaleLowerCase().includes(needle)))
}

export function inboxRefreshInterval(syncIntervalSeconds: number | undefined) {
  return Math.min(Math.max(syncIntervalSeconds ?? 60, 5), 60) * 1000
}

export function removeItemByPublicID<T extends { public_id: string }>(items: T[], publicID: string) {
  return items.filter((item) => item.public_id !== publicID)
}

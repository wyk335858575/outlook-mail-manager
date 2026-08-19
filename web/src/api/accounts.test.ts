import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  authorizationVerificationURL,
  deleteAccount,
  deleteAccounts,
  fetchAccountConfig,
  fetchAccountPage,
  fetchAccountSelection,
  fetchAccounts,
  importAccounts,
  microsoftAccountSignOutURL,
  restartAuthorization,
  setAccountsDisabled,
  startOAuthImport,
  startAuthorization,
  updateAccountConfig,
} from './accounts'

describe('accounts API', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('reads account status without exposing OAuth credentials', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      accounts: [{
        public_id: 'acc_public',
        imported_email: 'user@outlook.com',
        notes: '',
        status: 'pending',
        consecutive_failures: 0,
        sync_failures: 0,
        sync_backlog: 0,
        groups: [],
        tags: [],
        authorization_in_progress: false,
      }],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))

    const result = await fetchAccounts()
    expect(result.accounts[0]).toMatchObject({
      public_id: 'acc_public',
      status: 'pending',
    })
    expect(result.accounts[0]).not.toHaveProperty('access_token')
    expect(result.accounts[0]).not.toHaveProperty('refresh_token')
  })

  it('builds paginated search, selection and batch account requests', async () => {
    const page = { accounts: [], total: 0, page: 2, page_size: 50, status_counts: {} }
    const selection = { public_ids: ['acc_1'], total: 1 }
    const batch = { requested: 1, succeeded: 1, skipped: 0, failed: 0, results: [] }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(page), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(selection), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(batch), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(batch), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await fetchAccountPage({ q: 'Finance team', status: 'active', page: 2, pageSize: 50 })
    await fetchAccountSelection({ q: 'Finance team', status: 'active' })
    await setAccountsDisabled(['acc_1'], true, 'csrf-value')
    await deleteAccounts(['acc_1'], 'csrf-value')

    expect(fetchMock.mock.calls[0][0]).toBe('/api/accounts?page=2&page_size=50&q=Finance+team&status=active')
    expect(fetchMock.mock.calls[1][0]).toBe('/api/accounts/selection?q=Finance+team&status=active')
    expect(fetchMock.mock.calls[2][1]).toMatchObject({
      method: 'POST', body: JSON.stringify({ public_ids: ['acc_1'], disabled: true }),
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
    })
    expect(fetchMock.mock.calls[3][1]).toMatchObject({
      method: 'POST', body: JSON.stringify({ public_ids: ['acc_1'], confirm: 'DELETE_LOCAL_ACCOUNTS' }),
    })
  })

  it('adds CSRF protection to account mutations', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ created: 1, existing: 0 }), {
        status: 201,
        headers: { 'Content-Type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        id: 'oauth_job',
        account_public_id: 'acc_public',
        imported_email: 'user@outlook.com',
        state: 'waiting',
        expires_at: '2026-08-17T08:15:00Z',
      }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        id: 'oauth_job_restarted',
        account_public_id: 'acc_public',
        imported_email: 'user@outlook.com',
        state: 'waiting',
        expires_at: '2026-08-17T08:15:00Z',
      }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await importAccounts('user@outlook.com', 'csrf-value')
    await startAuthorization('acc_public', 'csrf-value')
    await restartAuthorization('oauth_job', 'csrf-value')
    await deleteAccount('acc_public', 'csrf-value')

    for (const call of fetchMock.mock.calls.slice(0, 3)) {
      expect(call[1]).toMatchObject({
        method: 'POST',
        headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      })
    }
    expect(fetchMock).toHaveBeenLastCalledWith('/api/accounts/acc_public', expect.objectContaining({
      method: 'DELETE',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
    }))
  })

  it('uses the generic device login page instead of a session-bound complete URL', () => {
    expect(authorizationVerificationURL({
      id: 'oauth_job',
      account_public_id: 'acc_public',
      imported_email: 'second@outlook.com',
      state: 'waiting',
      expires_at: '2026-08-17T08:15:00Z',
      verification_uri: 'https://microsoft.com/devicelogin',
      verification_uri_complete: 'https://login.live.com/oauth20_remoteconnect.srf?otc=ABCD',
    })).toBe('https://microsoft.com/devicelogin')
  })

  it('uses the current Microsoft link verification page returned by the provider', () => {
    expect(authorizationVerificationURL({
      id: 'oauth_job',
      account_public_id: 'acc_public',
      imported_email: 'second@outlook.com',
      state: 'waiting',
      expires_at: '2026-08-18T07:20:00Z',
      verification_uri: 'https://www.microsoft.com/link',
    })).toBe('https://www.microsoft.com/link')
  })

  it('uses the consumers logout endpoint when switching personal Microsoft accounts', () => {
    expect(microsoftAccountSignOutURL).toBe('https://login.microsoftonline.com/consumers/oauth2/v2.0/logout')
  })

  it('falls back to the generic device login page for missing or malformed provider URLs', () => {
    const authorization = {
      id: 'oauth_job',
      account_public_id: 'acc_public',
      imported_email: 'second@outlook.com',
      state: 'waiting' as const,
      expires_at: '2026-08-17T08:15:00Z',
    }

    expect(authorizationVerificationURL(authorization)).toBe('https://www.microsoft.com/link')
    expect(authorizationVerificationURL({
      ...authorization,
      verification_uri: 'https://login.live.com/undefined',
    })).toBe('https://www.microsoft.com/link')
    expect(authorizationVerificationURL({
      ...authorization,
      verification_uri: 'not a URL',
    })).toBe('https://www.microsoft.com/link')
  })

  it('loads and updates the Microsoft client ID with CSRF protection', async () => {
    const config = {
      microsoft_configured: true,
      client_id: '11111111-2222-4333-8444-555555555555',
      updated_at: '2026-08-17T08:00:00Z',
    }
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(config), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })))
    vi.stubGlobal('fetch', fetchMock)

    await expect(fetchAccountConfig()).resolves.toEqual(config)
    await expect(updateAccountConfig(config.client_id, 'csrf-value')).resolves.toEqual(config)

    expect(fetchMock).toHaveBeenLastCalledWith('/api/accounts/config', expect.objectContaining({
      method: 'PUT',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
      body: JSON.stringify({ client_id: config.client_id }),
    }))
  })

  it('sends only email, client ID and refresh token for OAuth imports', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: 'oauth_import_job', state: 'queued', total: 1, processed: 0, created: 0, updated: 0,
      skipped: 0, failed: 0, created_at: '2026-08-19T00:00:00Z', updated_at: '2026-08-19T00:00:00Z', items: [],
    }), { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await startOAuthImport([{
      email: 'user@outlook.com',
      client_id: '11111111-2222-4333-8444-555555555555',
      refresh_token: 'refresh-value',
    }], false, 'csrf-value')

    const body = String(fetchMock.mock.calls[0][1]?.body)
    expect(body).not.toContain('password')
    expect(JSON.parse(body)).toEqual({
      accounts: [{
        email: 'user@outlook.com',
        client_id: '11111111-2222-4333-8444-555555555555',
        refresh_token: 'refresh-value',
      }],
      overwrite_existing: false,
    })
  })
})

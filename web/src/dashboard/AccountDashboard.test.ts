import { describe, expect, it, vi } from 'vitest'

import { microsoftAccountSignOutURL, type Account } from '../api/accounts'
import { accountDeleteConfirmationMatches, accountIdentityDisplay, copyAuthorizationEmail, failedBatchAccountIDs, nextAuthorizationAction, openMicrosoftAuthorizationPopup } from './AccountDashboard'

function account(status: Account['status']): Account {
  return {
    public_id: `acc_${status}`,
    imported_email: `${status}@outlook.com`,
    auth_method: 'web',
    notes: '',
    status,
    consecutive_failures: 0,
    sync_failures: 0,
    sync_backlog: 0,
    groups: [],
    tags: [],
    cleanup_protected: false,
    authorization_in_progress: false,
  }
}

describe('account authorization queue', () => {
  it('explains that authorization is unavailable when every account is active', () => {
    expect(nextAuthorizationAction([account('active'), account('active')], true, false, false)).toMatchObject({
      account: undefined,
      disabled: true,
      label: '暂无待授权账号',
    })
  })

  it('selects the next account that needs authorization', () => {
    expect(nextAuthorizationAction([account('active'), account('pending')], true, false, false)).toMatchObject({
      account: { status: 'pending' },
      disabled: false,
      label: '授权下一个',
    })
  })

  it('does not send accounts with sync-only failures through Microsoft authorization again', () => {
    expect(nextAuthorizationAction([account('degraded'), account('active')], true, false, false)).toMatchObject({
      account: undefined,
      disabled: true,
      label: '暂无待授权账号',
    })
  })

  it('does not send O2 token accounts through webpage authorization', () => {
    expect(nextAuthorizationAction([{ ...account('reauth_required'), auth_method: 'oauth' }], true, false, false)).toMatchObject({
      account: undefined,
      disabled: true,
      label: '暂无待授权账号',
    })
  })
})

describe('account identity display', () => {
  it('shows the Microsoft primary email first and the display name second', () => {
    expect(accountIdentityDisplay({
      imported_email: 'alias@outlook.com',
      primary_email: 'primary@outlook.com',
      display_name: 'Finance Box',
    })).toEqual({
      primaryEmail: 'primary@outlook.com',
      displayName: 'Finance Box',
    })
  })

  it('uses the imported email for an unauthorized account', () => {
    expect(accountIdentityDisplay({
      imported_email: 'pending@outlook.com',
    })).toEqual({
      primaryEmail: 'pending@outlook.com',
      displayName: '',
    })
  })

  it('does not duplicate the primary email when no distinct display name exists', () => {
    expect(accountIdentityDisplay({
      imported_email: 'alias@outlook.com',
      primary_email: 'primary@outlook.com',
      display_name: ' PRIMARY@OUTLOOK.COM ',
    })).toEqual({
      primaryEmail: 'primary@outlook.com',
      displayName: '',
    })
  })
})

describe('authorization email copy', () => {
  it('copies the imported email shown for the current authorization', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)

    await expect(copyAuthorizationEmail(' target@outlook.com ', writeText)).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('target@outlook.com')
  })
})

describe('Microsoft authorization window', () => {
  it('signs out the previous Microsoft session before opening the verification page', () => {
    const popup = {
      closed: false,
      focus: vi.fn(),
      location: { href: '' },
    }
    const openWindow = vi.fn(() => popup)
    let navigate = () => {}
    const onNavigated = vi.fn()

    expect(openMicrosoftAuthorizationPopup({
      verificationURL: 'https://www.microsoft.com/link',
      openWindow,
      schedule: (callback, delay) => {
        expect(delay).toBe(4000)
        navigate = callback
      },
      onNavigated,
    })).toBe(true)

    expect(openWindow).toHaveBeenCalledWith(
      microsoftAccountSignOutURL,
      'outlook-mail-manager-microsoft-authorization',
    )
    navigate()
    expect(popup.location.href).toBe('https://www.microsoft.com/link')
    expect(popup.focus).toHaveBeenCalledOnce()
    expect(onNavigated).toHaveBeenCalledOnce()
  })

  it('reports a blocked authorization window', () => {
    expect(openMicrosoftAuthorizationPopup({
      verificationURL: 'https://www.microsoft.com/link',
      openWindow: () => null,
      schedule: vi.fn(),
    })).toBe(false)
  })
})

describe('account batch operations', () => {
  it('keeps only failed accounts selected after a partial result', () => {
    expect(failedBatchAccountIDs({
      requested: 3, succeeded: 1, skipped: 1, failed: 1,
      results: [
        { public_id: 'acc_ok', state: 'updated' },
        { public_id: 'acc_skip', state: 'skipped' },
        { public_id: 'acc_fail', state: 'failed', error: 'not_found' },
      ],
    })).toEqual(['acc_fail'])
  })

  it('requires the exact affected account count before deletion', () => {
    expect(accountDeleteConfirmationMatches(12, '删除 12 个账号')).toBe(true)
    expect(accountDeleteConfirmationMatches(12, '删除 11 个账号')).toBe(false)
  })
})

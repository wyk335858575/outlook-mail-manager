import { describe, expect, it } from 'vitest'

import { notificationRuleStart } from './NotificationsDashboard'

describe('notification rule creation', () => {
  it('starts with channel creation when no notification channel exists', () => {
    expect(notificationRuleStart(0)).toEqual({
      target: 'channel',
      label: '先新建通知通道',
    })
  })

  it('opens the rule form when a notification channel exists', () => {
    expect(notificationRuleStart(1)).toEqual({
      target: 'rule',
      label: '新建规则',
    })
  })
})

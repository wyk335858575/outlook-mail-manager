import { describe, expect, it } from 'vitest'

import {
  canCreateNotificationChannel,
  notificationChannelGuide,
  notificationCategoryLabel,
  notificationCategoryOptions,
  notificationRuleSummary,
  notificationRuleStart,
  wxPushConfigFingerprint,
} from './NotificationsDashboard'

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

describe('notification rule categories', () => {
  it('exposes only the five notification categories in Chinese', () => {
    expect(notificationCategoryOptions).toEqual([
      { value: 'important', label: '重要' },
      { value: 'verification', label: '验证码' },
      { value: 'marketing', label: '营销' },
      { value: 'spam', label: '垃圾邮件' },
      { value: 'normal', label: '普通' },
    ])
    expect(notificationCategoryLabel('uncertain')).toBe('待确认')
  })

  it('summarizes categories and personal-only filtering in Chinese', () => {
    const base = { categories: ['spam'], personal_only: true, sender_address: '', sender_domain: '', subject_keywords: [], group_names: [], tag_names: [] }
    expect(notificationRuleSummary(base)).toBe('垃圾邮件 · 个性化邮件')
    expect(notificationRuleSummary({ ...base, categories: [], personal_only: false })).toBe('所有新邮件')
  })
})

describe('notification channel guide', () => {
  it('matches the built-in WXPush fields used by this project', () => {
    const guide = notificationChannelGuide('wxpush')
    const content = [guide.intro, ...guide.steps, 'template' in guide ? guide.template : ''].join('\n')

    expect(guide.title).toBe('WXPush 接入教程')
		expect(content).toContain('AppID')
		expect(content).toContain('AppSecret')
		expect(content).toContain('OpenID')
		expect(content).toContain('{{title.DATA}}')
		expect(content).toContain('{{content.DATA}}')
		expect(content).toContain('{{sender.DATA}}')
		expect(content).toContain('{{subject.DATA}}')
		expect(content).toContain('{{body.DATA}}')
		expect(content).toContain('固定文字')
		expect(content).not.toContain('/wxsend')
		expect(content).not.toContain('API_TOKEN')
		expect(content).not.toContain('WX_BASE_URL')
  })
})

describe('WXPush pre-save testing', () => {
  const input = {
    name: 'WXPush', kind: 'wxpush' as const, enabled: true, system_enabled: true,
    wxpush_app_id: 'app-id', wxpush_app_secret: 'app-secret', wxpush_user_id: 'open-id', wxpush_template_id: 'template-id',
  }

  it('changes the fingerprint whenever a credential changes', () => {
    const fingerprint = wxPushConfigFingerprint(input)
    for (const key of ['wxpush_app_id', 'wxpush_app_secret', 'wxpush_user_id', 'wxpush_template_id'] as const) {
      expect(wxPushConfigFingerprint({ ...input, [key]: `${input[key]}-changed` })).not.toBe(fingerprint)
    }
  })

  it('requires the current WXPush credentials to have passed testing', () => {
    const fingerprint = wxPushConfigFingerprint(input)
    expect(canCreateNotificationChannel(input, '')).toBe(false)
    expect(canCreateNotificationChannel(input, fingerprint)).toBe(true)
    expect(canCreateNotificationChannel({ ...input, wxpush_user_id: 'changed' }, fingerprint)).toBe(false)
    expect(canCreateNotificationChannel({ ...input, name: '' }, fingerprint)).toBe(false)
  })
})

import { describe, expect, it } from 'vitest'

import { buildOTPExamples } from './apiExamples'

describe('OTP API examples', () => {
  it('includes scoped OTP parameters and reads the token from the environment', () => {
    const examples = buildOTPExamples({
      baseURL: 'https://mail.example.com/',
      account: 'user@outlook.com',
      after: '2026-08-18T08:00:00Z',
      sender: 'no-reply@example.com',
      subject: 'verification code',
    })

    for (const example of Object.values(examples)) {
      expect(example).toContain('account')
      expect(example).toMatch(/2026-08-18T08(?::|%3A)00(?::|%3A)00Z/)
      expect(example).toContain('wait_seconds')
      expect(example).toContain('sender')
      expect(example).toContain('subject')
      expect(example).not.toContain('omm_secret')
    }
    expect(examples.curl).toContain('$OMM_API_TOKEN')
    expect(examples.powershell).toContain('$env:OMM_API_TOKEN')
    expect(examples.node).toContain('process.env.OMM_API_TOKEN')
  })
})

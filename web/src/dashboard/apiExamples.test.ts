import { describe, expect, it } from 'vitest'

import { buildOTPExamples } from './apiExamples'

describe('OTP API examples', () => {
  it('includes scoped OTP parameters and reads the token from the environment', () => {
    const examples = buildOTPExamples({
      baseURL: 'https://mail.example.com/',
      account: 'user@outlook.com',
      sender: 'no-reply@example.com',
      subject: 'verification code',
    })

    for (const example of Object.values(examples)) {
      expect(example).toContain('account')
      expect(example).toContain('wait_seconds')
      expect(example).toContain('sender')
      expect(example).toContain('subject')
      expect(example).not.toContain('omm_secret')
    }
    expect(examples.curl).toContain('$OMM_API_TOKEN')
    expect(examples.curl).not.toContain('\n+')
    expect(examples.powershell).toContain('$env:OMM_API_TOKEN')
    expect(examples.node).toContain('process.env.OMM_API_TOKEN')
    expect(examples.csharp).toContain('AuthenticationHeaderValue("Bearer"')
    expect(examples.csharp).toContain('Environment.GetEnvironmentVariable("OMM_API_TOKEN")')
    expect(examples.curl).not.toContain('after=')
    expect(examples.node).not.toContain('after')
  })
})

import { describe, expect, it } from 'vitest'

import { parseOAuthCredentialImport } from './oauthImportParser'

describe('OAuth credential import parser', () => {
  it.each(['----', '\t', ',', ';'])('parses %j and drops the password before returning request data', (delimiter) => {
    const password = 'browser-only-secret'
    const result = parseOAuthCredentialImport(`user@outlook.com${delimiter}${password}${delimiter}11111111-2222-4333-8444-555555555555${delimiter}refresh-value`)

    expect(result).toEqual([{
      email: 'user@outlook.com',
      client_id: '11111111-2222-4333-8444-555555555555',
      refresh_token: 'refresh-value',
    }])
    expect(JSON.stringify(result)).not.toContain(password)
  })

  it('rejects malformed rows instead of guessing field positions', () => {
    expect(() => parseOAuthCredentialImport('user@outlook.com----password----client')).toThrow('第 1 行无法识别')
  })
})

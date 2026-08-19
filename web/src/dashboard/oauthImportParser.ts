import type { OAuthImportInput } from '../api/accounts'

const delimiters = ['----', '\t', ',', ';'] as const

export function parseOAuthCredentialImport(value: string): OAuthImportInput[] {
  const rows = value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  if (rows.length === 0 || rows.length > 1000) {
    throw new Error('每次需要导入 1 到 1000 个账号')
  }
  return rows.map((line, index) => {
    const delimiter = delimiters.find((candidate) => line.split(candidate).length === 4)
    if (!delimiter) throw new Error(`第 ${index + 1} 行无法识别，请使用 ----、Tab、逗号或分号分隔`)
    const [email, , clientID, refreshToken] = line.split(delimiter).map((field) => field.trim())
    if (!email || !clientID || !refreshToken) throw new Error(`第 ${index + 1} 行缺少邮箱、Client ID 或 refresh token`)
    return { email, client_id: clientID, refresh_token: refreshToken }
  })
}

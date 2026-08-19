export type OTPExampleInput = {
  baseURL: string
  account: string
  after: string
  sender?: string
  subject?: string
}

export function buildOTPExamples(input: OTPExampleInput) {
  const params = new URLSearchParams({ account: input.account, after: input.after, wait_seconds: '30' })
  if (input.sender?.trim()) params.set('sender', input.sender.trim())
  if (input.subject?.trim()) params.set('subject', input.subject.trim())
  const url = `${input.baseURL.replace(/\/$/, '')}/api/v1/otp/latest?${params.toString()}`
  const nodeParams = [...params.entries()].map(([key, value]) => `url.searchParams.set(${JSON.stringify(key)}, ${JSON.stringify(value)})`).join('\n')

  return {
    curl: `curl "${url}" \\\n+  -H "Authorization: Bearer $OMM_API_TOKEN"`,
    powershell: `$headers = @{ Authorization = "Bearer $env:OMM_API_TOKEN" }\nInvoke-RestMethod -Method Get -Uri "${url}" -Headers $headers`,
    node: `const url = new URL(${JSON.stringify(`${input.baseURL.replace(/\/$/, '')}/api/v1/otp/latest`)})\n${nodeParams}\n\nconst response = await fetch(url, {\n  headers: { Authorization: \`Bearer \${process.env.OMM_API_TOKEN}\` },\n})\nif (!response.ok) throw new Error(\`HTTP \${response.status}\`)\nconsole.log(await response.json())`,
  }
}

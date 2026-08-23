import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

import { BrowserAccountInput } from './APIDashboard'

describe('browser API account input', () => {
  it('renders a free-form account field without native suggestions', () => {
    const html = renderToStaticMarkup(<BrowserAccountInput
      value="manual@outlook.com"
      placeholder="请输入邮箱或账号 ID"
      onChange={vi.fn()}
    />)

    expect(html).toContain('type="text"')
    expect(html).toContain('value="manual@outlook.com"')
    expect(html).toContain('placeholder="请输入邮箱或账号 ID"')
    expect(html).not.toContain('<select')
    expect(html).not.toContain('datalist')
  })
})

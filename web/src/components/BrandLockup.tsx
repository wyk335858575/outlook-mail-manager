import { Mail } from 'lucide-react'

export function BrandLockup() {
  return (
    <div className="brand-lockup">
      <span className="brand-mark" aria-hidden="true">
        <Mail size={22} strokeWidth={1.8} />
      </span>
      <div>
        <strong>Outlook</strong>
        <span>邮箱管理台</span>
      </div>
    </div>
  )
}

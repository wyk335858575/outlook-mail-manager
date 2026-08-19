import type { ReactNode } from 'react'
import { Activity, BadgeCheck, BellRing, Inbox, KeyRound, Settings2, ShieldCheck, Star, Trash2, UsersRound } from 'lucide-react'

import { BrandLockup } from './BrandLockup'

type AppSection = 'inbox' | 'personal' | 'verification' | 'accounts' | 'cleanup' | 'notifications' | 'api' | 'health' | 'security' | 'settings'

const navigation: Array<{ id: AppSection; label: string; icon: typeof Inbox }> = [
  { id: 'inbox', label: '统一收件箱', icon: Inbox },
  { id: 'personal', label: '个性化收件箱', icon: Star },
  { id: 'verification', label: '验证码', icon: BadgeCheck },
  { id: 'accounts', label: '账号管理', icon: UsersRound },
  { id: 'cleanup', label: '清理中心', icon: Trash2 },
  { id: 'notifications', label: '通知中心', icon: BellRing },
  { id: 'api', label: 'API token', icon: KeyRound },
  { id: 'health', label: '健康与备份', icon: Activity },
  { id: 'security', label: '安全状态', icon: ShieldCheck },
  { id: 'settings', label: '设置', icon: Settings2 },
]

export function AppFrame({ active, children }: { active: AppSection; children: ReactNode }) {
  return (
    <div className="app-shell">
      <aside className="side-rail">
        <BrandLockup />
        <nav className="primary-nav" aria-label="主导航">
          {navigation.map((item) => {
            const Icon = item.icon
            return (
              <a key={item.id} className={`nav-item ${active === item.id ? 'is-active' : ''}`} href={`#${item.id}`} aria-current={active === item.id ? 'page' : undefined} title={item.label}>
                <Icon size={18} aria-hidden="true" /><span>{item.label}</span>
              </a>
            )
          })}
        </nav>
      </aside>
      {children}
    </div>
  )
}

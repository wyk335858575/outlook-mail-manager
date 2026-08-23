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
        <a className="github-repository-link" href="https://github.com/wyk335858575/outlook-mail-manager" target="_blank" rel="noreferrer">
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path fill="currentColor" d="M12 .7a11.5 11.5 0 0 0-3.64 22.41c.58.11.79-.25.79-.56v-2.23c-3.22.7-3.9-1.37-3.9-1.37-.53-1.34-1.29-1.7-1.29-1.7-1.05-.72.08-.71.08-.71 1.17.08 1.78 1.2 1.78 1.2 1.03 1.77 2.71 1.26 3.37.97.1-.75.4-1.26.74-1.55-2.57-.29-5.27-1.28-5.27-5.68 0-1.26.45-2.28 1.19-3.09-.12-.29-.52-1.47.11-3.05 0 0 .97-.31 3.16 1.18a10.96 10.96 0 0 1 5.76 0c2.2-1.49 3.16-1.18 3.16-1.18.63 1.58.23 2.76.11 3.05.74.81 1.19 1.83 1.19 3.09 0 4.41-2.71 5.38-5.29 5.67.42.36.79 1.06.79 2.14v3.18c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .7Z" />
          </svg>
          <span>GitHub</span>
        </a>
      </aside>
      {children}
    </div>
  )
}

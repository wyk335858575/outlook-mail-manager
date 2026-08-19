import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  Check,
  Clock3,
  KeyRound,
  LockKeyhole,
  LogOut,
  ShieldCheck,
  Smartphone,
  UserRound,
} from 'lucide-react'

import type { AuthStatus } from '../api/auth'
import { fetchHealth } from '../api/health'
import { AppFrame } from '../components/AppFrame'

const expiryFormatter = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
})

export function SecurityDashboard({
  status,
  onLogout,
  loggingOut,
  logoutError,
}: {
  status: AuthStatus
  onLogout: () => void
  loggingOut: boolean
  logoutError: string
}) {
  const health = useQuery({
    queryKey: ['health'],
    queryFn: ({ signal }) => fetchHealth(signal),
    refetchInterval: 15_000,
  })
  const serviceReady = health.data?.status === 'ok'
  const serviceState = health.isError ? '服务异常' : serviceReady ? '服务正常' : '正在检查'
  const expiresAt = status.session_expires_at
    ? expiryFormatter.format(new Date(status.session_expires_at))
    : '未知'

  return (
    <AppFrame active="security">
      <main className="main-workspace" id="security">
        <header className="workspace-header">
          <div>
            <p className="eyebrow">Administrator security</p>
            <h1>安全状态</h1>
          </div>
          <button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}>
            <LogOut size={17} aria-hidden="true" />
            {loggingOut ? '正在退出' : '退出登录'}
          </button>
        </header>

        {logoutError && <p className="form-error dashboard-error" role="alert">{logoutError}</p>}

        <section className="security-hero">
          <div className="security-hero-icon" aria-hidden="true"><ShieldCheck size={25} /></div>
          <div>
            <p>管理员保护</p>
            <h2>{status.username || '管理员'} 已启用双因素验证</h2>
            <span>密码用于验证管理员并解锁数据库中的敏感凭据。</span>
          </div>
          <span className="state-label state-ready"><Check size={14} /> 已保护</span>
        </section>

        <section className="security-route" aria-labelledby="protection-heading">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Protection route</p>
              <h2 id="protection-heading">验证链路</h2>
            </div>
            <span className={`state-label ${serviceReady ? 'state-ready' : health.isError ? 'state-error' : 'state-checking'}`}>
              <Activity size={13} /> {serviceState}
            </span>
          </div>
          <div className="route-track">
            <SecurityStep icon={<UserRound size={19} />} label="管理员账号" detail={status.username || '唯一管理员'} />
            <SecurityStep icon={<KeyRound size={19} />} label="管理员密码" detail="Argon2id" />
            <SecurityStep icon={<Smartphone size={19} />} label="身份验证器" detail="TOTP / 防重放" />
            <SecurityStep icon={<Clock3 size={19} />} label="当前会话" detail="12 小时有效期" />
          </div>
        </section>

        <div className="detail-grid">
          <section className="detail-panel" aria-labelledby="session-heading">
            <div className="section-heading compact"><h2 id="session-heading">当前会话</h2></div>
            <dl className="data-list">
              <div><dt>登录状态</dt><dd className="value-ready"><Check size={15} /> 已验证</dd></div>
              <div><dt>会话有效期</dt><dd>{expiresAt}</dd></div>
              <div><dt>Cookie 保护</dt><dd>HttpOnly / SameSite=Lax</dd></div>
            </dl>
          </section>

          <section className="detail-panel" aria-labelledby="encryption-heading">
            <div className="section-heading compact"><h2 id="encryption-heading">数据加密</h2></div>
            <dl className="data-list">
              <div><dt>解锁状态</dt><dd className="value-ready"><LockKeyhole size={15} /> 已解锁</dd></div>
              <div><dt>磁盘密钥文件</dt><dd>不使用</dd></div>
              <div><dt>重启后</dt><dd>管理员首次登录时解锁</dd></div>
            </dl>
          </section>
        </div>
      </main>
    </AppFrame>
  )
}

function SecurityStep({ icon, label, detail }: { icon: ReactNode; label: string; detail: string }) {
  return (
    <div className="route-step state-ready">
      <span className="route-node" aria-hidden="true">{icon}</span>
      <div><strong>{label}</strong><span>{detail}</span></div>
      <span className="route-result">已启用</span>
    </div>
  )
}

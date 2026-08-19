import type { ReactNode } from 'react'
import { Check, LockKeyhole } from 'lucide-react'

import { BrandLockup } from '../components/BrandLockup'

const steps = ['创建管理员账号', '绑定身份验证器']

export function AuthFrame({
  eyebrow,
  title,
  intro,
  currentStep,
  children,
}: {
  eyebrow: string
  title: string
  intro: string
  currentStep?: 1 | 2
  children: ReactNode
}) {
  return (
    <div className="auth-shell">
      <aside className="auth-rail">
        <BrandLockup />
        {currentStep ? (
          <ol className="setup-steps" aria-label="管理员设置进度">
            {steps.map((label, index) => {
              const step = index + 1
              const done = step < currentStep
              const active = step === currentStep
              return (
                <li key={label} className={done ? 'is-done' : active ? 'is-active' : ''}>
                  <span>{done ? <Check size={15} /> : step}</span>
                  <div>
                    <small>步骤 {step}</small>
                    <strong>{label}</strong>
                  </div>
                </li>
              )
            })}
          </ol>
        ) : (
          <div className="login-security-note">
            <LockKeyhole size={22} aria-hidden="true" />
            <div>
              <strong>双因素保护</strong>
              <span>密码与一次性验证信息缺一不可。</span>
            </div>
          </div>
        )}
        <p className="auth-rail-foot">单管理员 · 密码与 TOTP 保护</p>
      </aside>

      <main className="auth-workspace">
        <div className="auth-content">
          <header className="auth-heading">
            <p className="eyebrow">{eyebrow}</p>
            <h1>{title}</h1>
            <p>{intro}</p>
          </header>
          {children}
        </div>
      </main>
    </div>
  )
}

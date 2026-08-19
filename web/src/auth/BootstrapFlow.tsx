import { type FormEvent, useState } from 'react'
import { Check, Copy, Eye, EyeOff, KeyRound, LoaderCircle, UserRound } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'

import { APIError, completeSetup, startSetup, type SetupStartResult } from '../api/auth'
import { AuthFrame } from './AuthFrame'

export function BootstrapFlow({ onComplete }: { onComplete: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [passwordConfirmation, setPasswordConfirmation] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [challenge, setChallenge] = useState<SetupStartResult | null>(null)
  const [passcode, setPasscode] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [copied, setCopied] = useState(false)

  async function handleCredentials(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setSubmitting(true)
    try {
      const result = await startSetup({ username, password, passwordConfirmation })
      setChallenge(result)
    } catch (requestError) {
      setError(errorMessage(requestError))
    } finally {
      setSubmitting(false)
    }
  }

  async function handleVerification(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!challenge) return
    setError('')
    setSubmitting(true)
    try {
      await completeSetup({
        challengeID: challenge.challenge_id,
        passcode,
      })
      onComplete()
    } catch (requestError) {
      setError(errorMessage(requestError))
    } finally {
      setSubmitting(false)
    }
  }

  async function copySecret() {
    if (!challenge) return
    try {
      await navigator.clipboard.writeText(challenge.secret)
      setCopied(true)
    } catch {
      setError('无法写入剪贴板，请手动记录密钥')
    }
  }

  if (challenge) {
    return (
      <AuthFrame
        eyebrow="Authenticator setup"
        title="绑定身份验证器"
        intro="扫描二维码并输入当前六位验证码。验证通过后才会创建管理员。"
        currentStep={2}
      >
        <div className="totp-layout">
          <div className="qr-panel">
            <QRCodeSVG
              value={challenge.provisioning_uri}
              size={184}
              bgColor="#ffffff"
              fgColor="#17212b"
              level="M"
              marginSize={1}
              title="Outlook 邮箱管理台 TOTP 二维码"
            />
          </div>
          <div className="manual-secret">
            <span>无法扫码时输入密钥</span>
            <code>{challenge.secret}</code>
            <button type="button" className="quiet-button" onClick={copySecret}>
              {copied ? <Check size={16} /> : <Copy size={16} />}
              {copied ? '已复制' : '复制密钥'}
            </button>
          </div>
        </div>

        <form className="auth-form compact-form" onSubmit={handleVerification}>
          <label className="field-label" htmlFor="setup-passcode">六位验证码</label>
          <div className="field-control">
            <KeyRound size={18} aria-hidden="true" />
            <input
              id="setup-passcode"
              value={passcode}
              onChange={(event) => setPasscode(event.target.value.replace(/\D/g, '').slice(0, 6))}
              inputMode="numeric"
              autoComplete="one-time-code"
              placeholder="000000"
              pattern="[0-9]{6}"
              required
              autoFocus
            />
          </div>
          {error && <p className="form-error" role="alert">{error}</p>}
          <div className="form-actions split-actions">
            <button
              type="button"
              className="secondary-button"
              onClick={() => {
                setChallenge(null)
                setPasscode('')
                setError('')
              }}
            >
              返回
            </button>
            <button className="primary-button" type="submit" disabled={submitting || passcode.length !== 6}>
              {submitting && <LoaderCircle className="is-spinning" size={17} />}
              验证并创建管理员
            </button>
          </div>
        </form>
      </AuthFrame>
    )
  }

  return (
    <AuthFrame
      eyebrow="Administrator setup"
      title="创建管理员"
      intro="首次安装后创建唯一管理员账号，并设置密码和身份验证器。"
      currentStep={1}
    >
      <form className="auth-form" onSubmit={handleCredentials}>
        <label className="field-label" htmlFor="admin-username">管理员账号</label>
        <div className="field-control">
          <UserRound size={18} aria-hidden="true" />
          <input
            id="admin-username"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            type="text"
            autoComplete="username"
            spellCheck={false}
            minLength={3}
            maxLength={64}
            required
            autoFocus
          />
        </div>
        <p className="field-hint">3 到 64 个字符，可使用字母、数字及 . _ - @。</p>

        <label className="field-label" htmlFor="new-password">管理员密码</label>
        <div className="field-control">
          <KeyRound size={18} aria-hidden="true" />
          <input
            id="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            type={showPassword ? 'text' : 'password'}
            autoComplete="new-password"
            minLength={12}
            maxLength={1024}
            required
          />
          <button
            type="button"
            className="field-icon-button"
            onClick={() => setShowPassword((visible) => !visible)}
            aria-label={showPassword ? '隐藏密码' : '显示密码'}
            title={showPassword ? '隐藏密码' : '显示密码'}
          >
            {showPassword ? <EyeOff size={17} /> : <Eye size={17} />}
          </button>
        </div>
        <p className="field-hint">至少 12 个字符，建议使用不重复的长密码。</p>

        <label className="field-label" htmlFor="confirm-password">再次输入密码</label>
        <div className="field-control">
          <Check size={18} aria-hidden="true" />
          <input
            id="confirm-password"
            value={passwordConfirmation}
            onChange={(event) => setPasswordConfirmation(event.target.value)}
            type={showPassword ? 'text' : 'password'}
            autoComplete="new-password"
            minLength={12}
            maxLength={1024}
            required
          />
        </div>

        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="primary-button wide-button" type="submit" disabled={submitting || username.length < 3}>
          {submitting && <LoaderCircle className="is-spinning" size={17} />}
          继续绑定身份验证器
        </button>
      </form>
    </AuthFrame>
  )
}

function errorMessage(error: unknown) {
  return error instanceof APIError ? error.message : '服务暂时无法完成请求'
}

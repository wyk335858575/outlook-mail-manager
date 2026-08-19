import { type FormEvent, useState } from 'react'
import { Eye, EyeOff, KeyRound, LoaderCircle, Smartphone, UserRound } from 'lucide-react'

import { APIError, login } from '../api/auth'
import { AuthFrame } from './AuthFrame'

export function LoginScreen({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [passcode, setPasscode] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    try {
      await login({ username, password, passcode })
      onAuthenticated()
    } catch (requestError) {
      setError(requestError instanceof APIError ? requestError.message : '服务暂时无法完成请求')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <AuthFrame
      eyebrow="Administrator access"
      title="管理员登录"
      intro="输入管理员账号、密码和身份验证器中的六位验证码。"
    >
      <form className="auth-form" onSubmit={handleSubmit}>
        <label className="field-label" htmlFor="login-username">管理员账号</label>
        <div className="field-control">
          <UserRound size={18} aria-hidden="true" />
          <input
            id="login-username"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            type="text"
            autoComplete="username"
            required
            autoFocus
          />
        </div>

        <label className="field-label" htmlFor="login-password">管理员密码</label>
        <div className="field-control">
          <KeyRound size={18} aria-hidden="true" />
          <input
            id="login-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            type={showPassword ? 'text' : 'password'}
            autoComplete="current-password"
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

        <label className="field-label" htmlFor="login-factor">六位验证码</label>
        <div className="field-control">
          <Smartphone size={18} aria-hidden="true" />
          <input
            id="login-factor"
            value={passcode}
            onChange={(event) => setPasscode(event.target.value.replace(/\D/g, '').slice(0, 6))}
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder="000000"
            pattern="[0-9]{6}"
            required
          />
        </div>

        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="primary-button wide-button" type="submit" disabled={submitting || passcode.length !== 6}>
          {submitting && <LoaderCircle className="is-spinning" size={17} />}
          登录
        </button>
      </form>
    </AuthFrame>
  )
}

import { lazy, Suspense, useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertCircle, LoaderCircle, RefreshCw } from 'lucide-react'

import { BootstrapFlow } from './auth/BootstrapFlow'
import { LoginScreen } from './auth/LoginScreen'
import { fetchAuthStatus, logout } from './api/auth'
import { BrandLockup } from './components/BrandLockup'

const AccountDashboard = lazy(() => import('./dashboard/AccountDashboard').then((module) => ({ default: module.AccountDashboard })))
const APIDashboard = lazy(() => import('./dashboard/APIDashboard').then((module) => ({ default: module.APIDashboard })))
const CleanupDashboard = lazy(() => import('./dashboard/CleanupDashboard').then((module) => ({ default: module.CleanupDashboard })))
const HealthDashboard = lazy(() => import('./dashboard/HealthDashboard').then((module) => ({ default: module.HealthDashboard })))
const InboxDashboard = lazy(() => import('./dashboard/InboxDashboard').then((module) => ({ default: module.InboxDashboard })))
const NotificationsDashboard = lazy(() => import('./dashboard/NotificationsDashboard').then((module) => ({ default: module.NotificationsDashboard })))
const SecurityDashboard = lazy(() => import('./dashboard/SecurityDashboard').then((module) => ({ default: module.SecurityDashboard })))
const SettingsDashboard = lazy(() => import('./dashboard/SettingsDashboard').then((module) => ({ default: module.SettingsDashboard })))

type Section = 'inbox' | 'personal' | 'verification' | 'accounts' | 'cleanup' | 'notifications' | 'api' | 'health' | 'security' | 'settings'

function sectionFromHash(): Section {
  if (window.location.hash === '#personal') return 'personal'
  if (window.location.hash === '#verification') return 'verification'
  if (window.location.hash === '#accounts') return 'accounts'
  if (window.location.hash === '#cleanup') return 'cleanup'
  if (window.location.hash === '#notifications' || window.location.hash === '#notifications-rules') return 'notifications'
  if (window.location.hash === '#api') return 'api'
  if (window.location.hash === '#health') return 'health'
  if (window.location.hash === '#security') return 'security'
  if (window.location.hash === '#settings') return 'settings'
  return 'inbox'
}

export function App() {
  const queryClient = useQueryClient()
  const [loggingOut, setLoggingOut] = useState(false)
  const [logoutError, setLogoutError] = useState('')
  const [section, setSection] = useState<Section>(sectionFromHash)
  const status = useQuery({
    queryKey: ['auth-status'],
    queryFn: ({ signal }) => fetchAuthStatus(signal),
    retry: 1,
  })

  async function refreshStatus() {
    await queryClient.invalidateQueries({ queryKey: ['auth-status'] })
  }

  async function handleLogout() {
    if (!status.data?.csrf_token) return
    setLoggingOut(true)
    setLogoutError('')
    try {
      await logout(status.data.csrf_token)
      await refreshStatus()
    } catch {
      setLogoutError('退出失败，请刷新页面后重试')
    } finally {
      setLoggingOut(false)
    }
  }

  useEffect(() => {
    function readSection() {
      setSection(sectionFromHash())
    }
    window.addEventListener('hashchange', readSection)
    return () => window.removeEventListener('hashchange', readSection)
  }, [])

  if (status.isPending) {
    return (
      <div className="centered-state" aria-live="polite">
        <BrandLockup />
        <LoaderCircle className="is-spinning" size={24} />
        <span>正在读取安全状态</span>
      </div>
    )
  }

  if (status.isError || !status.data) {
    return (
      <div className="centered-state error-state">
        <BrandLockup />
        <AlertCircle size={26} />
        <h1>无法读取安全状态</h1>
        <p>请确认服务和数据库可用，然后重新检查。</p>
        <button className="primary-button" type="button" onClick={() => status.refetch()}>
          <RefreshCw size={17} /> 重新检查
        </button>
      </div>
    )
  }

  if (!status.data.initialized) {
    return <BootstrapFlow onComplete={() => void refreshStatus()} />
  }

  if (!status.data.authenticated) {
    return <LoginScreen onAuthenticated={() => window.location.reload()} />
  }

  const dashboardProps = {
    status: status.data,
    onLogout: handleLogout,
    loggingOut,
    logoutError,
  }
  let dashboard = <InboxDashboard key="inbox" {...dashboardProps} />
  if (section === 'security') dashboard = <SecurityDashboard {...dashboardProps} />
  else if (section === 'settings') dashboard = <SettingsDashboard {...dashboardProps} />
  else if (section === 'accounts') dashboard = <AccountDashboard {...dashboardProps} />
  else if (section === 'cleanup') dashboard = <CleanupDashboard {...dashboardProps} />
  else if (section === 'notifications') dashboard = <NotificationsDashboard {...dashboardProps} />
  else if (section === 'api') dashboard = <APIDashboard {...dashboardProps} />
  else if (section === 'health') dashboard = <HealthDashboard {...dashboardProps} />
  else if (section === 'personal') dashboard = <InboxDashboard key="personal" {...dashboardProps} view="personal" />
  else if (section === 'verification') dashboard = <InboxDashboard key="verification" {...dashboardProps} view="verification" />
  return (
    <Suspense fallback={<div className="centered-state" aria-live="polite"><LoaderCircle className="is-spinning" size={24} /><span>正在加载页面</span></div>}>
      {dashboard}
    </Suspense>
  )
}

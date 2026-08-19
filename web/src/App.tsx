import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertCircle, LoaderCircle, RefreshCw } from 'lucide-react'

import { BootstrapFlow } from './auth/BootstrapFlow'
import { LoginScreen } from './auth/LoginScreen'
import { fetchAuthStatus, logout } from './api/auth'
import { BrandLockup } from './components/BrandLockup'
import { AccountDashboard } from './dashboard/AccountDashboard'
import { APIDashboard } from './dashboard/APIDashboard'
import { CleanupDashboard } from './dashboard/CleanupDashboard'
import { HealthDashboard } from './dashboard/HealthDashboard'
import { InboxDashboard } from './dashboard/InboxDashboard'
import { NotificationsDashboard } from './dashboard/NotificationsDashboard'
import { SecurityDashboard } from './dashboard/SecurityDashboard'
import { SettingsDashboard } from './dashboard/SettingsDashboard'

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
  if (section === 'security') return <SecurityDashboard {...dashboardProps} />
  if (section === 'settings') return <SettingsDashboard {...dashboardProps} />
  if (section === 'accounts') return <AccountDashboard {...dashboardProps} />
  if (section === 'cleanup') return <CleanupDashboard {...dashboardProps} />
  if (section === 'notifications') return <NotificationsDashboard {...dashboardProps} />
  if (section === 'api') return <APIDashboard {...dashboardProps} />
  if (section === 'health') return <HealthDashboard {...dashboardProps} />
  if (section === 'personal') return <InboxDashboard key="personal" {...dashboardProps} view="personal" />
  if (section === 'verification') return <InboxDashboard key="verification" {...dashboardProps} view="verification" />
  return <InboxDashboard key="inbox" {...dashboardProps} />
}

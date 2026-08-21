import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Activity, CheckCircle2, Copy, Database, HardDrive, LoaderCircle, LogOut, RefreshCw, Save, Trash2, TriangleAlert } from 'lucide-react'

import { APIError, type AuthStatus } from '../api/auth'
import { createBackup, deleteBackup, fetchBackups, fetchDetailedHealth, fetchUpdateStatus, type Backup } from '../api/maintenance'
import { AppFrame } from '../components/AppFrame'

export function HealthDashboard({ status, onLogout, loggingOut, logoutError }: { status: AuthStatus; onLogout: () => void; loggingOut: boolean; logoutError: string }) {
  const queryClient = useQueryClient()
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const healthQuery = useQuery({ queryKey: ['detailed-health'], queryFn: ({ signal }) => fetchDetailedHealth(signal), refetchInterval: 30_000 })
  const backupsQuery = useQuery({ queryKey: ['backups'], queryFn: ({ signal }) => fetchBackups(signal) })
  const updateStatusQuery = useQuery({ queryKey: ['update-status'], queryFn: ({ signal }) => fetchUpdateStatus(signal), retry: false })
  const health = healthQuery.data

  async function backup() {
    if (!status.csrf_token) return
    setCreating(true); setError(''); setNotice('')
    try {
      const item = await createBackup(status.csrf_token)
      setNotice(`一致性备份已创建：${item.name}`)
      await Promise.all([queryClient.invalidateQueries({ queryKey: ['backups'] }), queryClient.invalidateQueries({ queryKey: ['detailed-health'] })])
    } catch (reason) { setError(messageFor(reason, '无法创建备份')) } finally { setCreating(false) }
  }

  async function removeBackup(item: Backup) {
    if (!status.csrf_token) return
    const warning = backupsQuery.data?.backups.length === 1 ? '\n\n这是当前最后一个备份，删除后将没有可用恢复点。' : ''
    if (!window.confirm(`确定删除备份 ${item.name} 吗？此操作无法撤销。${warning}`)) return
    setDeleting(item.name); setError(''); setNotice('')
    try {
      await deleteBackup(item.name, status.csrf_token)
      setNotice(`备份已删除：${item.name}`)
      await Promise.all([queryClient.invalidateQueries({ queryKey: ['backups'] }), queryClient.invalidateQueries({ queryKey: ['detailed-health'] })])
    } catch (reason) { setError(messageFor(reason, '无法删除备份')) } finally { setDeleting('') }
  }

  async function copyUpdateCommand(command: string) {
    setError(''); setNotice('')
    try {
      await navigator.clipboard.writeText(command)
      setNotice('升级命令已复制，请在宝塔 root 终端中执行')
    } catch { setError('浏览器无法复制升级命令') }
  }

  return <AppFrame active="health"><main className="main-workspace operations-workspace"><header className="workspace-header operations-header"><div><p className="eyebrow">System readiness</p><h1>健康与备份</h1></div><div className="header-actions"><button className="primary-button" type="button" disabled={creating} onClick={() => void backup()}>{creating ? <LoaderCircle className="is-spinning" size={17} /> : <Save size={17} />} 创建备份</button><button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}><LogOut size={17} />{loggingOut ? '正在退出' : '退出登录'}</button></div></header>
    {(logoutError || error || notice) && <p className={`operation-notice ${logoutError || error ? 'is-error' : ''}`} role="status">{logoutError || error || notice}</p>}
    {healthQuery.isPending ? <div className="operations-empty"><LoaderCircle className="is-spinning" size={20} />正在检查系统状态</div> : healthQuery.isError || !health ? <div className="health-alert"><TriangleAlert size={20} /><strong>无法读取详细健康状态</strong></div> : <>
      <section className="health-metrics"><HealthMetric icon={<Database size={20} />} label="SQLite" value={health.maintenance.database_ok ? '完整' : '异常'} detail={`Schema v${health.maintenance.schema_version} · ${formatBytes(health.maintenance.database_size_bytes)}`} ok={health.maintenance.database_ok} /><HealthMetric icon={<HardDrive size={20} />} label="数据磁盘" value={`${health.mail.disk.used_percent}%`} detail={diskLabel(health.mail.disk.level)} ok={health.mail.disk.level === 'normal'} /><HealthMetric icon={<Activity size={20} />} label="同步队列" value={String(health.mail.high_priority_queue + health.mail.background_queue)} detail={`${health.mail.active_accounts} 个可同步账号`} ok /><HealthMetric icon={<Save size={20} />} label="备份" value={String(health.maintenance.backup_count)} detail={health.maintenance.latest_backup ? `最近 ${formatDate(health.maintenance.latest_backup.created_at)}` : '尚无备份'} ok={health.maintenance.backup_count > 0} /></section>
      {(health.maintenance.failed_notifications > 0 || health.maintenance.cleanup_failures > 0 || health.mail.disk.level !== 'normal') && <section className="health-alert"><TriangleAlert size={20} /><div><strong>需要处理的运行状态</strong><span>通知失败 {health.maintenance.failed_notifications} 条 · 清理失败 {health.maintenance.cleanup_failures} 条 · 磁盘状态 {diskLabel(health.mail.disk.level)}</span></div></section>}
    </>}
    <section className="operations-section update-section"><div className="section-commandbar"><div><strong>版本与在线更新</strong><span>仅接受配置仓库中由 GitHub Actions OIDC/Cosign 签名的正式版本。</span></div><button className="icon-command" type="button" title="重新检查版本" aria-label="重新检查版本" onClick={() => void updateStatusQuery.refetch()}><RefreshCw className={updateStatusQuery.isFetching ? 'is-spinning' : ''} size={16} /></button></div>
      {updateStatusQuery.isPending ? <div className="operations-empty"><LoaderCircle className="is-spinning" size={18} />正在检查 GitHub Releases</div> : updateStatusQuery.isError || !updateStatusQuery.data ? <div className="health-alert"><TriangleAlert size={18} /><div><strong>版本检查暂时不可用</strong><span>{messageFor(updateStatusQuery.error, '请检查服务器网络和 GitHub 仓库配置')}</span></div></div> : <div className="update-overview"><div><span>当前版本</span><strong>{updateStatusQuery.data.current_version}</strong></div><div><span>最新稳定版</span><strong>{updateStatusQuery.data.latest_version || '未配置'}</strong></div><div><span>检查时间</span><strong>{formatDate(updateStatusQuery.data.checked_at)}</strong></div>{updateStatusQuery.data.update_available && updateStatusQuery.data.update_command ? <button className="primary-button" type="button" onClick={() => void copyUpdateCommand(updateStatusQuery.data.update_command!)}><Copy size={17} />复制升级命令</button> : <p>{updateStatusQuery.data.reason}</p>}</div>}
      {updateStatusQuery.data?.release_notes && <details className="release-notes"><summary>查看更新说明</summary><p>{updateStatusQuery.data.release_notes}</p></details>}
      {updateStatusQuery.data?.update_available && updateStatusQuery.data.update_command && <div className="update-command"><code>{updateStatusQuery.data.update_command}</code></div>}
    </section>
    <section className="operations-section"><div className="section-commandbar"><div><strong>一致性备份</strong><span>备份由 SQLite 在线生成并记录 SHA-256；恢复操作仅允许在服务停止时执行。</span></div><button className="icon-command" type="button" title="刷新备份" aria-label="刷新备份" onClick={() => void backupsQuery.refetch()}><RefreshCw size={16} /></button></div><div className="operations-table backup-table"><div className="operations-table-head"><span>创建时间</span><span>文件</span><span>大小</span><span>SHA-256</span><span>操作</span></div>{backupsQuery.isPending ? <div className="operations-empty"><LoaderCircle className="is-spinning" size={18} />正在读取备份</div> : (backupsQuery.data?.backups.length ?? 0) === 0 ? <div className="operations-empty">尚无备份</div> : backupsQuery.data?.backups.map((item) => <div className="operations-row" key={`${item.name}-${item.sha256}`}><time>{formatDate(item.created_at)}</time><strong>{item.name}</strong><span>{formatBytes(item.size_bytes)}</span><code title={item.sha256}>{item.sha256}</code><button className="icon-command danger-command" type="button" title="删除备份" aria-label={`删除备份 ${item.name}`} disabled={deleting !== ''} onClick={() => void removeBackup(item)}>{deleting === item.name ? <LoaderCircle className="is-spinning" size={16} /> : <Trash2 size={16} />}</button></div>)}</div></section>
  </main></AppFrame>
}

function HealthMetric({ icon, label, value, detail, ok }: { icon: React.ReactNode; label: string; value: string; detail: string; ok: boolean }) { return <div className={`health-metric ${ok ? 'is-ok' : 'needs-attention'}`}><span>{icon}</span><div><small>{label}</small><strong>{value}</strong><p>{detail}</p></div>{ok ? <CheckCircle2 size={17} /> : <TriangleAlert size={17} />}</div> }
function diskLabel(value: string) { return ({ normal: '正常', warning: '预警', critical: '严重', metadata_only: '仅同步元数据' }[value] ?? value) }
function formatBytes(value: number) { if (value < 1024) return `${value} B`; if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`; return `${(value / 1024 ** 2).toFixed(1)} MiB` }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(new Date(value)) }
function messageFor(error: unknown, fallback: string) { return error instanceof APIError ? error.message : fallback }


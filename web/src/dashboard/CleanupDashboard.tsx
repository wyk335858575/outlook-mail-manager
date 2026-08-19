import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Clock3, ListChecks, LoaderCircle, LogOut, Plus, RefreshCw, RotateCcw, ShieldCheck, Trash2, X } from 'lucide-react'

import { APIError, type AuthStatus } from '../api/auth'
import {
  approveCleanup,
  createClassificationRule,
  deleteClassificationRule,
  fetchAuditEvents,
  fetchClassificationRules,
  fetchCleanupActions,
  reclassifyMessages,
  restoreCleanup,
  retryCleanup,
  type CleanupState,
  type MailCategory,
} from '../api/classification'
import { AppFrame } from '../components/AppFrame'

type View = 'queue' | 'rules' | 'audit'
type QueueFilter = CleanupState | 'all'

const categoryLabels: Record<MailCategory, string> = {
  important: '重要', verification: '验证码', marketing: '营销', spam: '垃圾', normal: '普通', uncertain: '待确认',
}
const stateLabels: Record<CleanupState, string> = {
  candidate: '待审核', dismissed: '已忽略', holding: '宽限期', restored: '已恢复', deleted: '已移至已删除', failed: '失败',
}

export function CleanupDashboard({ status, onLogout, loggingOut, logoutError }: {
  status: AuthStatus
  onLogout: () => void
  loggingOut: boolean
  logoutError: string
}) {
  const queryClient = useQueryClient()
  const [view, setView] = useState<View>('queue')
  const [filter, setFilter] = useState<QueueFilter>('all')
  const [selected, setSelected] = useState<string[]>([])
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [approvalProgress, setApprovalProgress] = useState('')
  const [ruleOpen, setRuleOpen] = useState(false)
  const actionsQuery = useQuery({
    queryKey: ['cleanup-actions', filter], queryFn: ({ signal }) => fetchCleanupActions(filter, signal),
  })
  const rulesQuery = useQuery({ queryKey: ['classification-rules'], queryFn: ({ signal }) => fetchClassificationRules(signal) })
  const auditQuery = useQuery({ queryKey: ['audit-events'], queryFn: ({ signal }) => fetchAuditEvents(signal) })
  const actions = actionsQuery.data?.actions ?? []
  const candidates = actions.filter((item) => item.state === 'candidate')
  const selectedCandidates = selected.filter((id) => candidates.some((item) => item.public_id === id))
  const selectableCandidates = candidates.slice(0, 500)
  const allCandidatesSelected = selectableCandidates.length > 0 && selectableCandidates.every((item) => selected.includes(item.public_id))
  const counts = useMemo(() => {
    const result: Record<string, number> = { all: actions.length }
    for (const item of actions) result[item.state] = (result[item.state] ?? 0) + 1
    return result
  }, [actions])

  async function approveSelected() {
		if (!status.csrf_token || selectedCandidates.length === 0) return
		if (!window.confirm(`将 ${selectedCandidates.length} 封邮件移入“Outlook Manager 待清理”文件夹，并启动 14 天恢复期？`)) return
		const requested = selectedCandidates.slice(0, 500)
		const remaining = new Set(requested)
		let successful = 0
		let requestError = ''
		const itemErrors: string[] = []
		setBusy('approve'); setError(''); setNotice(''); setApprovalProgress(`正在批量处理 ${requested.length} 封`)
		try {
			const result = await approveCleanup(requested, status.csrf_token)
			const results = new Map(result.results.map((item) => [item.public_id, item]))
			for (const publicID of requested) {
				const item = results.get(publicID)
				if (item && !item.error) {
					remaining.delete(publicID)
					successful++
				} else itemErrors.push(item?.error || '服务未返回处理结果')
			}
		} catch (reason) {
			requestError = messageFor(reason, '无法批准清理')
		}
    setSelected([...remaining])
    setNotice(successful > 0 ? `已移动 ${successful} 封邮件，14 天内可恢复；${remaining.size} 封保持选中` : '')
    if (requestError) setError(`${requestError}；未处理邮件已保持选中`)
    else if (itemErrors.length > 0) setError(`${itemErrors.length} 封邮件未处理：${itemErrors[0]}；失败项已保持选中`)
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] }),
      queryClient.invalidateQueries({ queryKey: ['mail-messages'] }),
      queryClient.invalidateQueries({ queryKey: ['audit-events'] }),
    ])
    setApprovalProgress('')
    setBusy('')
  }

  function toggleAllCandidates(checked: boolean) {
    setSelected(checked ? selectableCandidates.map((item) => item.public_id) : [])
  }

  function toggleCandidate(publicID: string, checked: boolean) {
    setSelected((current) => {
      if (!checked) return current.filter((id) => id !== publicID)
      if (current.includes(publicID) || current.length >= 500) return current
      return [...current, publicID]
    })
  }

  async function runAction(publicID: string, action: 'restore' | 'retry') {
    if (!status.csrf_token) return
    if (action === 'restore' && !window.confirm('将这封邮件移回原文件夹？')) return
    setBusy(publicID); setError(''); setNotice('')
    try {
      if (action === 'restore') await restoreCleanup(publicID, status.csrf_token)
      else await retryCleanup(publicID, status.csrf_token)
      setNotice(action === 'restore' ? '邮件已恢复到原文件夹' : '已重新加入清理队列')
      await queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] })
      await queryClient.invalidateQueries({ queryKey: ['mail-messages'] })
      await queryClient.invalidateQueries({ queryKey: ['audit-events'] })
    } catch (reason) { setError(messageFor(reason, '操作未完成')) } finally { setBusy('') }
  }

  async function removeRule(publicID: string) {
    if (!status.csrf_token || !window.confirm('删除这条分类规则？已有邮件分类不会自动回退。')) return
    setBusy(publicID); setError('')
    try {
      await deleteClassificationRule(publicID, status.csrf_token)
      await queryClient.invalidateQueries({ queryKey: ['classification-rules'] })
    } catch (reason) { setError(messageFor(reason, '无法删除规则')) } finally { setBusy('') }
  }

  async function reclassify() {
    if (!status.csrf_token) return
    setBusy('reclassify'); setError(''); setNotice('')
    try {
      await reclassifyMessages(status.csrf_token)
      setNotice('已按当前规则重新计算非人工分类邮件')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['classification-rules'] }),
        queryClient.invalidateQueries({ queryKey: ['cleanup-actions'] }),
        queryClient.invalidateQueries({ queryKey: ['mail-messages'] }),
      ])
    } catch (reason) { setError(messageFor(reason, '无法重新分类')) } finally { setBusy('') }
  }

  return (
    <AppFrame active="cleanup">
      <main className="main-workspace operations-workspace">
        <header className="workspace-header operations-header">
          <div><p className="eyebrow">Review and retention</p><h1>清理中心</h1></div>
          <div className="header-actions">
            {view === 'rules' && <button className="primary-button" type="button" onClick={() => setRuleOpen(true)}><Plus size={17} /> 新建规则</button>}
            <button className="secondary-button logout-button" type="button" onClick={onLogout} disabled={loggingOut}><LogOut size={17} />{loggingOut ? '正在退出' : '退出登录'}</button>
          </div>
        </header>
        {(logoutError || error || notice) && <p className={`operation-notice ${logoutError || error ? 'is-error' : ''}`} role="status">{logoutError || error || notice}</p>}

        <div className="operations-tabs" role="tablist" aria-label="清理中心视图">
          <button type="button" className={view === 'queue' ? 'is-selected' : ''} onClick={() => setView('queue')}><ListChecks size={16} /> 审核队列</button>
          <button type="button" className={view === 'rules' ? 'is-selected' : ''} onClick={() => setView('rules')}><ShieldCheck size={16} /> 分类规则</button>
          <button type="button" className={view === 'audit' ? 'is-selected' : ''} onClick={() => setView('audit')}><Clock3 size={16} /> 操作审计</button>
        </div>

        {view === 'queue' && (
          <section className="operations-section">
            <div className="section-commandbar">
              <div className="compact-tabs">
                {(['all', 'candidate', 'holding', 'failed', 'restored', 'deleted'] as QueueFilter[]).map((state) => (
                  <button key={state} type="button" className={filter === state ? 'is-selected' : ''} onClick={() => { setFilter(state); setSelected([]) }}>
                    {state === 'all' ? '全部' : stateLabels[state]} <span>{counts[state] ?? 0}</span>
                  </button>
                ))}
              </div>
              <div className="cleanup-bulk-actions">
                <label className="cleanup-select-all"><input type="checkbox" checked={allCandidatesSelected} disabled={selectableCandidates.length === 0 || busy !== ''} onChange={(event) => toggleAllCandidates(event.target.checked)} /><span>全选待审核{selectableCandidates.length === 500 ? '（最多 500 封）' : ''}</span></label>
                <button className="primary-button" type="button" disabled={selectedCandidates.length === 0 || busy !== ''} onClick={() => void approveSelected()}>
                  {busy === 'approve' ? <LoaderCircle className="is-spinning" size={16} /> : <Trash2 size={16} />} {approvalProgress || `批准移入待清理 (${selectedCandidates.length})`}
                </button>
              </div>
            </div>
            <div className="operations-table cleanup-table">
              <div className="operations-table-head"><span>选择</span><span>邮件</span><span>分类与理由</span><span>状态</span><span>操作</span></div>
              {actionsQuery.isPending ? <EmptyState text="正在读取清理记录" loading /> : actionsQuery.isError ? <EmptyState text="无法读取清理记录" /> : actions.length === 0 ? <EmptyState text="当前没有清理记录" /> : actions.map((item) => (
                <div className="operations-row" key={item.public_id}>
                  <label className="row-check"><input type="checkbox" disabled={item.state !== 'candidate' || (!selected.includes(item.public_id) && selected.length >= 500)} checked={selected.includes(item.public_id)} onChange={(event) => toggleCandidate(item.public_id, event.target.checked)} /><span className="visually-hidden">选择 {item.subject}</span></label>
                  <div className="operation-identity"><strong>{item.subject || '（无主题）'}</strong><span>{item.sender_name || item.sender_address}</span><small>{item.account_name}</small></div>
                  <div><span className={`category-badge category-${item.category}`}>{categoryLabels[item.category]}</span><small className="cell-note">{item.candidate_reason}</small></div>
                  <div><span className={`state-pill state-${item.state}`}>{stateLabels[item.state]}</span><small className="cell-note">{item.execute_after ? `执行于 ${formatDate(item.execute_after)}` : item.last_error || formatDate(item.received_at)}</small></div>
                  <div className="row-actions">
                    {(item.state === 'holding' || item.state === 'failed') && <button className="icon-command" type="button" title="恢复邮件" aria-label="恢复邮件" disabled={busy !== ''} onClick={() => void runAction(item.public_id, 'restore')}>{busy === item.public_id ? <LoaderCircle className="is-spinning" size={16} /> : <RotateCcw size={16} />}</button>}
                    {item.state === 'failed' && <button className="icon-command" type="button" title="重试清理" aria-label="重试清理" disabled={busy !== ''} onClick={() => void runAction(item.public_id, 'retry')}><RefreshCw size={16} /></button>}
                  </div>
                </div>
              ))}
            </div>
          </section>
        )}

        {view === 'rules' && (
          <section className="operations-section">
            <div className="section-commandbar"><div><strong>可解释分类规则</strong><span>高优先级规则先执行；保护规则会阻止自动清理。</span></div><button className="secondary-button" type="button" disabled={busy !== ''} onClick={() => void reclassify()}>{busy === 'reclassify' ? <LoaderCircle className="is-spinning" size={16} /> : <RefreshCw size={16} />} 重新分类</button></div>
            <div className="operations-table rules-table">
              <div className="operations-table-head"><span>规则</span><span>匹配</span><span>结果</span><span>优先级</span><span>操作</span></div>
              {rulesQuery.isPending ? <EmptyState text="正在读取规则" loading /> : (rulesQuery.data?.rules.length ?? 0) === 0 ? <EmptyState text="尚未创建分类规则" /> : rulesQuery.data?.rules.map((rule) => (
                <div className="operations-row" key={rule.public_id}>
                  <div className="operation-identity"><strong>{rule.name}</strong><span>{rule.enabled ? '已启用' : '已停用'}</span></div>
                  <div><strong>{fieldLabel(rule.match_field)} {rule.match_operator === 'equals' ? '等于' : '包含'}</strong><small className="cell-note">{rule.match_value}</small></div>
                  <div>{rule.target_category && <span className={`category-badge category-${rule.target_category}`}>{categoryLabels[rule.target_category]}</span>}{rule.protects_cleanup && <span className="protection-mark"><ShieldCheck size={13} />保护</span>}</div>
                  <strong>{rule.priority}</strong>
                  <div className="row-actions"><button className="icon-command danger-command" type="button" title="删除规则" aria-label="删除规则" disabled={busy !== ''} onClick={() => void removeRule(rule.public_id)}><Trash2 size={16} /></button></div>
                </div>
              ))}
            </div>
          </section>
        )}

        {view === 'audit' && (
          <section className="operations-section">
            <div className="section-commandbar"><div><strong>不可变操作记录</strong><span>分类、清理、恢复、通知和 API token 操作均记录在此。</span></div><button className="icon-command" type="button" title="刷新审计" aria-label="刷新审计" onClick={() => void auditQuery.refetch()}><RefreshCw size={16} /></button></div>
            <div className="audit-list">
              {auditQuery.isPending ? <EmptyState text="正在读取审计记录" loading /> : (auditQuery.data?.events.length ?? 0) === 0 ? <EmptyState text="尚无审计记录" /> : auditQuery.data?.events.map((event) => (
                <div className="audit-row" key={event.public_id}><time>{formatDate(event.created_at)}</time><div><strong>{event.event_type}</strong><span>{event.actor} · {event.entity_type}{event.entity_public_id ? ` · ${event.entity_public_id}` : ''}</span></div><code>{Object.keys(event.detail).length ? JSON.stringify(event.detail) : '{}'}</code></div>
              ))}
            </div>
          </section>
        )}
      </main>
      {ruleOpen && <RuleDialog csrfToken={status.csrf_token ?? ''} onClose={() => setRuleOpen(false)} onCreated={async () => { setRuleOpen(false); await queryClient.invalidateQueries({ queryKey: ['classification-rules'] }) }} />}
    </AppFrame>
  )
}

function RuleDialog({ csrfToken, onClose, onCreated }: { csrfToken: string; onClose: () => void; onCreated: () => Promise<void> }) {
  const [name, setName] = useState('')
  const [field, setField] = useState<'sender' | 'domain' | 'subject' | 'body'>('sender')
  const [operator, setOperator] = useState<'equals' | 'contains'>('equals')
  const [value, setValue] = useState('')
  const [category, setCategory] = useState<MailCategory | ''>('normal')
  const [protects, setProtects] = useState(false)
  const [priority, setPriority] = useState(100)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  async function save() {
    setSaving(true); setError('')
    try {
      await createClassificationRule({ name, match_field: field, match_operator: operator, match_value: value, target_category: category || undefined, protects_cleanup: protects, priority, enabled: true }, csrfToken)
      await onCreated()
    } catch (reason) { setError(messageFor(reason, '无法创建规则')); setSaving(false) }
  }
  return <div className="dialog-backdrop" role="presentation"><section className="dialog-panel operation-dialog" role="dialog" aria-modal="true" aria-labelledby="rule-title">
    <div className="dialog-heading"><div><p className="eyebrow">Classification rule</p><h2 id="rule-title">新建分类规则</h2></div><button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button></div>
    <div className="form-grid two-columns">
      <label><span>规则名称</span><input value={name} onChange={(event) => setName(event.target.value)} /></label>
      <label><span>优先级</span><input type="number" value={priority} onChange={(event) => setPriority(Number(event.target.value))} /></label>
      <label><span>匹配字段</span><select value={field} onChange={(event) => setField(event.target.value as typeof field)}><option value="sender">发件人地址</option><option value="domain">发件域名</option><option value="subject">主题</option><option value="body">正文</option></select></label>
      <label><span>匹配方式</span><select value={operator} onChange={(event) => setOperator(event.target.value as typeof operator)}><option value="equals">完全等于</option><option value="contains">包含</option></select></label>
      <label className="wide-field"><span>匹配内容</span><input value={value} onChange={(event) => setValue(event.target.value)} /></label>
      <label><span>目标分类</span><select value={category} onChange={(event) => setCategory(event.target.value as MailCategory | '')}><option value="">不修改分类</option>{Object.entries(categoryLabels).map(([id, label]) => <option key={id} value={id}>{label}</option>)}</select></label>
      <label className="switch-field"><input type="checkbox" checked={protects} onChange={(event) => setProtects(event.target.checked)} /><span>同时保护命中邮件，不允许自动清理</span></label>
    </div>
    {error && <p className="form-error">{error}</p>}
    <div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={saving || !name.trim() || !value.trim()} onClick={() => void save()}>{saving ? <LoaderCircle className="is-spinning" size={16} /> : <Check size={16} />} 创建规则</button></div>
  </section></div>
}

function EmptyState({ text, loading = false }: { text: string; loading?: boolean }) {
  return <div className="operations-empty">{loading && <LoaderCircle className="is-spinning" size={18} />}{text}</div>
}
function fieldLabel(value: string) { return ({ sender: '发件人', domain: '域名', subject: '主题', body: '正文' }[value] ?? value) }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(new Date(value)) }
function messageFor(error: unknown, fallback: string) { return error instanceof APIError ? error.message : fallback }

import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Funnel, LoaderCircle, Pencil, Plus, Search, Trash2, X } from 'lucide-react'

import type { Account } from '../api/accounts'
import { APIError } from '../api/auth'
import type { MailCategory } from '../api/classification'
import {
  createPersonalInboxRule,
  deletePersonalInboxRule,
  fetchPersonalInboxRules,
  updatePersonalInboxRule,
  type PersonalInboxRule,
} from '../api/personalInbox'
import { filterPersonalRuleAccounts } from './personalRuleAccounts'

const categoryLabels: Record<MailCategory, string> = {
  important: '重要', verification: '验证码', marketing: '营销', spam: '垃圾', normal: '普通', uncertain: '待确认',
}

type RuleDraft = Omit<PersonalInboxRule, 'public_id' | 'created_at' | 'updated_at'>
type AccountScope = 'all' | 'selected'

function emptyDraft(): RuleDraft {
  return {
    name: '', enabled: true, account_public_ids: [], group_names: [], tag_names: [], categories: [],
    sender_address: '', sender_domain: '', subject_keywords: [], require_otp: false,
  }
}

export function PersonalRulesDialog({ accounts, csrfToken, onClose }: {
  accounts: Account[]
  csrfToken: string
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const rulesQuery = useQuery({ queryKey: ['personal-inbox-rules'], queryFn: ({ signal }) => fetchPersonalInboxRules(signal) })
  const [draft, setDraft] = useState<RuleDraft>(emptyDraft)
  const [editingID, setEditingID] = useState('')
  const [editorOpen, setEditorOpen] = useState(false)
  const [accountScope, setAccountScope] = useState<AccountScope>('all')
  const [accountSearch, setAccountSearch] = useState('')
  const [groupsText, setGroupsText] = useState('')
  const [tagsText, setTagsText] = useState('')
  const [keywordsText, setKeywordsText] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const rules = rulesQuery.data?.rules ?? []
  const accountNames = useMemo(() => new Map(accounts.map((account) => [account.public_id, accountLabel(account)])), [accounts])
  const accountsByID = useMemo(() => new Map(accounts.map((account) => [account.public_id, account])), [accounts])
  const visibleAccounts = useMemo(() => filterPersonalRuleAccounts(accounts, accountSearch), [accounts, accountSearch])
  const selectedAccountSet = useMemo(() => new Set(draft.account_public_ids), [draft.account_public_ids])
  const canSave = draft.name.trim() !== '' && (accountScope === 'all' || draft.account_public_ids.length > 0) && hasCondition({
    ...draft,
    group_names: split(groupsText),
    tag_names: split(tagsText),
    subject_keywords: split(keywordsText),
  })

  function beginCreate() {
    setEditingID('')
    setDraft(emptyDraft())
    setAccountScope('all')
    setAccountSearch('')
    setGroupsText('')
    setTagsText('')
    setKeywordsText('')
    setError('')
    setEditorOpen(true)
  }

  function beginEdit(rule: PersonalInboxRule) {
    setEditingID(rule.public_id ?? '')
    setDraft({
      name: rule.name,
      enabled: rule.enabled,
      account_public_ids: rule.account_public_ids,
      group_names: rule.group_names,
      tag_names: rule.tag_names,
      categories: rule.categories,
      sender_address: rule.sender_address,
      sender_domain: rule.sender_domain,
      subject_keywords: rule.subject_keywords,
      require_otp: rule.require_otp,
    })
    setAccountScope(rule.account_public_ids.length > 0 ? 'selected' : 'all')
    setAccountSearch('')
    setGroupsText(rule.group_names.join(', '))
    setTagsText(rule.tag_names.join(', '))
    setKeywordsText(rule.subject_keywords.join(', '))
    setError('')
    setEditorOpen(true)
  }

  async function refreshRules() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['personal-inbox-rules'] }),
      queryClient.invalidateQueries({ queryKey: ['mail-messages'] }),
    ])
  }

  async function saveRule() {
    if (!canSave || !csrfToken) return
    const input: RuleDraft = {
      ...draft,
      name: draft.name.trim(),
      sender_address: draft.sender_address.trim(),
      sender_domain: draft.sender_domain.trim(),
      group_names: split(groupsText),
      tag_names: split(tagsText),
      subject_keywords: split(keywordsText),
    }
    setBusy('save')
    setError('')
    try {
      if (editingID) await updatePersonalInboxRule(editingID, input, csrfToken)
      else await createPersonalInboxRule(input, csrfToken)
      await refreshRules()
      setEditorOpen(false)
    } catch (reason) {
      setError(messageFor(reason, '无法保存个性化规则'))
    } finally {
      setBusy('')
    }
  }

  async function toggleRule(rule: PersonalInboxRule, enabled: boolean) {
    if (!rule.public_id || !csrfToken) return
    setBusy(rule.public_id)
    setError('')
    try {
      await updatePersonalInboxRule(rule.public_id, { ...rule, enabled }, csrfToken)
      await refreshRules()
    } catch (reason) {
      setError(messageFor(reason, '无法更新个性化规则'))
    } finally {
      setBusy('')
    }
  }

  async function removeRule(rule: PersonalInboxRule) {
    if (!rule.public_id || !csrfToken || !window.confirm(`删除个性化规则“${rule.name}”？`)) return
    setBusy(rule.public_id)
    setError('')
    try {
      await deletePersonalInboxRule(rule.public_id, csrfToken)
      if (editingID === rule.public_id) setEditorOpen(false)
      await refreshRules()
    } catch (reason) {
      setError(messageFor(reason, '无法删除个性化规则'))
    } finally {
      setBusy('')
    }
  }

  function toggleAccount(publicID: string, selected: boolean) {
    setDraft((current) => ({
      ...current,
      account_public_ids: selected
        ? [...current.account_public_ids, publicID]
        : current.account_public_ids.filter((value) => value !== publicID),
    }))
  }

  function selectAccountScope(scope: AccountScope) {
    setAccountScope(scope)
    if (scope === 'all') setDraft((current) => ({ ...current, account_public_ids: [] }))
  }

  function toggleCategory(category: MailCategory, selected: boolean) {
    setDraft((current) => ({
      ...current,
      categories: selected
        ? [...current.categories, category]
        : current.categories.filter((value) => value !== category),
    }))
  }

  return <div className="dialog-backdrop" role="presentation">
    <section className="dialog-panel personal-rules-dialog" role="dialog" aria-modal="true" aria-labelledby="personal-rules-title">
      <div className="dialog-heading">
        <div><p className="eyebrow">Personal inbox</p><h2 id="personal-rules-title">个性化收件箱规则</h2></div>
        <button className="field-icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose}><X size={18} /></button>
      </div>
      <p className="dialog-copy">单条规则的条件同时满足才会命中；多条启用规则命中任意一条即进入个性化收件箱。此处不会发送通知。</p>
      {error && <p className="form-error" role="alert">{error}</p>}

      <div className="personal-rule-commandbar">
        <div><Funnel size={16} /><span>{rules.length} 条规则</span></div>
        <button className="secondary-button" type="button" onClick={beginCreate}><Plus size={16} /> 新建规则</button>
      </div>

      <div className="personal-rule-list">
        {rulesQuery.isPending ? <div className="operations-empty"><LoaderCircle className="is-spinning" size={18} />正在读取规则</div>
          : rulesQuery.isError ? <div className="operations-empty">无法读取个性化规则</div>
            : rules.length === 0 ? <div className="operations-empty">尚未创建个性化规则</div>
              : rules.map((rule) => <div className="personal-rule-row" key={rule.public_id}>
                <div><strong>{rule.name}</strong><span>{personalRuleSummary(rule, accountNames)}</span></div>
                <label className="inline-switch"><input type="checkbox" checked={rule.enabled} disabled={busy !== ''} onChange={(event) => void toggleRule(rule, event.target.checked)} /><span>{rule.enabled ? '启用' : '停用'}</span></label>
                <button className="icon-command" type="button" title="编辑规则" aria-label={`编辑规则 ${rule.name}`} disabled={busy !== ''} onClick={() => beginEdit(rule)}><Pencil size={15} /></button>
                <button className="icon-command danger-command" type="button" title="删除规则" aria-label={`删除规则 ${rule.name}`} disabled={busy !== ''} onClick={() => void removeRule(rule)}>{busy === rule.public_id ? <LoaderCircle className="is-spinning" size={15} /> : <Trash2 size={15} />}</button>
              </div>)}
      </div>

      {editorOpen && <div className="personal-rule-editor">
        <div className="personal-rule-editor-title"><strong>{editingID ? '编辑规则' : '新建规则'}</strong><button className="field-icon-button" type="button" title="收起编辑器" aria-label="收起编辑器" onClick={() => setEditorOpen(false)}><X size={16} /></button></div>
        <div className="form-grid two-columns">
          <label className="wide-field"><span>规则名称</span><input value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} /></label>
          <fieldset className="wide-field personal-account-scope"><legend>账号范围</legend>
            <div className="account-scope-options">
              <label><input type="radio" name="personal-account-scope" checked={accountScope === 'all'} onChange={() => selectAccountScope('all')} /><span>全部邮箱</span></label>
              <label><input type="radio" name="personal-account-scope" checked={accountScope === 'selected'} onChange={() => selectAccountScope('selected')} /><span>指定邮箱</span></label>
            </div>
            {accountScope === 'all' ? <p className="personal-account-note">匹配当前全部邮箱，也自动包含以后新增的邮箱。</p> : <div className="personal-account-picker">
              <label className="personal-account-search"><Search size={15} aria-hidden="true" /><span className="visually-hidden">搜索指定邮箱</span><input value={accountSearch} onChange={(event) => setAccountSearch(event.target.value)} placeholder="搜索邮箱或显示名称" /></label>
              {draft.account_public_ids.length > 0 && <div className="selected-account-list" aria-label="已选邮箱">{draft.account_public_ids.map((publicID) => {
                const account = accountsByID.get(publicID)
                return <span className="selected-account-chip" key={publicID}><span>{account ? accountLabel(account) : publicID}</span><button type="button" title="移除邮箱" aria-label={`移除邮箱 ${account ? accountLabel(account) : publicID}`} onClick={() => toggleAccount(publicID, false)}><X size={12} /></button></span>
              })}</div>}
              <div className="personal-account-results">{accounts.length === 0 ? <span className="muted-value">尚无已导入账号</span> : visibleAccounts.length === 0 ? <span className="muted-value">没有匹配的邮箱</span> : visibleAccounts.map((account) => <label key={account.public_id}><input type="checkbox" checked={selectedAccountSet.has(account.public_id)} onChange={(event) => toggleAccount(account.public_id, event.target.checked)} /><span>{accountLabel(account)}</span><code>{account.primary_email && account.primary_email !== account.imported_email ? account.imported_email : account.primary_email || account.imported_email}</code></label>)}</div>
              <p className="personal-account-note">最多显示 20 个搜索结果，已选 {draft.account_public_ids.length} 个邮箱。账号较多时也可直接使用分组或标签条件。</p>
            </div>}
          </fieldset>
          <fieldset className="wide-field checkbox-group personal-category-group"><legend>分类（可多选）</legend>{(Object.entries(categoryLabels) as Array<[MailCategory, string]>).map(([id, label]) => <label key={id}><input type="checkbox" checked={draft.categories.includes(id)} onChange={(event) => toggleCategory(id, event.target.checked)} /><span>{label}</span></label>)}</fieldset>
          <label><span>分组（逗号分隔）</span><input value={groupsText} onChange={(event) => setGroupsText(event.target.value)} /></label>
          <label><span>标签（逗号分隔）</span><input value={tagsText} onChange={(event) => setTagsText(event.target.value)} /></label>
          <label><span>发件人地址</span><input inputMode="email" value={draft.sender_address} onChange={(event) => setDraft({ ...draft, sender_address: event.target.value })} /></label>
          <label><span>发件域名</span><input value={draft.sender_domain} onChange={(event) => setDraft({ ...draft, sender_domain: event.target.value })} placeholder="example.com" /></label>
          <label className="wide-field"><span>主题关键词（多个关键词之间为 OR）</span><input value={keywordsText} onChange={(event) => setKeywordsText(event.target.value)} placeholder="付款, commission, PayPal" /></label>
          <label className="switch-field"><input type="checkbox" checked={draft.require_otp} onChange={(event) => setDraft({ ...draft, require_otp: event.target.checked })} /><span>必须包含已提取的验证码</span></label>
          <label className="switch-field"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /><span>创建后立即启用</span></label>
        </div>
        <p className="personal-rule-hint">规则名称必填，并且至少设置一个匹配条件。</p>
        <div className="form-actions dialog-actions"><button className="secondary-button" type="button" onClick={() => setEditorOpen(false)}>取消</button><button className="primary-button" type="button" disabled={busy !== '' || !canSave} onClick={() => void saveRule()}>{busy === 'save' ? <LoaderCircle className="is-spinning" size={16} /> : <Check size={16} />} {editingID ? '保存规则' : '创建规则'}</button></div>
      </div>}
    </section>
  </div>
}

function hasCondition(rule: RuleDraft) {
  return rule.account_public_ids.length > 0 || rule.group_names.length > 0 || rule.tag_names.length > 0 ||
    rule.categories.length > 0 || rule.sender_address.trim() !== '' || rule.sender_domain.trim() !== '' ||
    rule.subject_keywords.length > 0 || rule.require_otp
}

function split(value: string) {
  return [...new Set(value.split(/[,\n]/).map((item) => item.trim()).filter(Boolean))]
}

function accountLabel(account: Account) {
  return account.display_name || account.primary_email || account.imported_email
}

function personalRuleSummary(rule: PersonalInboxRule, accountNames: Map<string, string>) {
  const parts = [
    rule.account_public_ids.length === 0 ? '全部邮箱' : '',
    ...rule.account_public_ids.map((id) => accountNames.get(id) ?? id),
    ...rule.group_names.map((value) => `组:${value}`),
    ...rule.tag_names.map((value) => `标签:${value}`),
    ...rule.categories.map((value) => categoryLabels[value]),
    rule.sender_address,
    rule.sender_domain,
    ...rule.subject_keywords,
    rule.require_otp ? '含验证码' : '',
  ].filter(Boolean)
  return parts.join(' · ')
}

function messageFor(error: unknown, fallback: string) {
  return error instanceof APIError ? error.message : fallback
}

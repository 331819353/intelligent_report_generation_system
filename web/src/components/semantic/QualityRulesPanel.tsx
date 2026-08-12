import {
  CheckCircle,
  Clock,
  Database,
  PencilSimple,
  Plus,
  ShieldCheck,
  Trash,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useMemo, useState } from 'react'
import { AppButton } from '../AppButton'
import { currentDomainID } from '../../lib/domain-context'
import { semanticAPI, type SemanticObject } from '../../lib/semantic'

type Props = {
  models: SemanticObject[]
  rules: SemanticObject[]
  onChanged: (rules: SemanticObject[]) => void
  onNotice: (tone: 'success' | 'error', message: string) => void
}

type RuleCode = 'ROW_COUNT_NONNEGATIVE' | 'OUTPUT_GRAIN_UNIQUE_NOT_NULL'
type Severity = 'INFO' | 'WARNING' | 'BLOCKING'

type FormState = {
  code: string
  name: string
  targetVersionId: string
  datasetRuleCode: RuleCode
  severity: Severity
  maxAgeHours: string
}

const executableRules: Record<RuleCode, { name: string; description: string; evidence: string }> = {
  ROW_COUNT_NONNEGATIVE: {
    name: '产出行数有效',
    description: '验证物化产出的行数不为负数，并固定实际行数作为执行证据。',
    evidence: '每次物化完成时执行 · 读取当前模型固定的 ACTIVE 快照',
  },
  OUTPUT_GRAIN_UNIQUE_NOT_NULL: {
    name: '输出粒度唯一且非空',
    description: '验证数据集声明的粒度键没有重复值或空值，失败时阻断新快照激活。',
    evidence: '每次物化完成时执行 · 失败不会替换上一份可用快照',
  },
}

const severityLabels: Record<Severity, string> = {
  INFO: '提示',
  WARNING: '告警',
  BLOCKING: '阻断',
}

const emptyForm = (models: SemanticObject[]): FormState => ({
  code: '',
  name: '',
  targetVersionId: models.find(item => item.status === 'CERTIFIED')?.id ?? '',
  datasetRuleCode: 'OUTPUT_GRAIN_UNIQUE_NOT_NULL',
  severity: 'BLOCKING',
  maxAgeHours: '24',
})

function bindingOf(rule: SemanticObject) {
  const ast = rule.ruleAst && typeof rule.ruleAst === 'object' ? rule.ruleAst : {}
  return {
    datasetRuleCode: String(ast.datasetRuleCode ?? '') as RuleCode,
    maxAgeHours: typeof ast.maxAgeHours === 'number' ? ast.maxAgeHours : 0,
  }
}

function ruleName(rule: SemanticObject) {
  return rule.name || rule.code || '未命名质量规则'
}

function formatTime(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value))
}

export function QualityRulesPanel({ models, rules, onChanged, onNotice }: Props) {
  const eligibleModels = useMemo(
    () => models.filter(item => item.status === 'CERTIFIED' || item.status === 'ACTIVE'),
    [models],
  )
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<SemanticObject | null>(null)
  const [pendingDelete, setPendingDelete] = useState<SemanticObject | null>(null)
  const [form, setForm] = useState<FormState>(() => emptyForm(models))
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const refresh = async () => {
    const page = await semanticAPI.list('quality-rules', undefined, 200)
    onChanged(page.items ?? [])
  }

  const openCreate = () => {
    setEditing(null)
    setForm(emptyForm(eligibleModels))
    setError('')
    setFormOpen(true)
  }

  const openEdit = (rule: SemanticObject) => {
    const binding = bindingOf(rule)
    setEditing(rule)
    setForm({
      code: rule.code ?? '',
      name: rule.name ?? '',
      targetVersionId: rule.targetVersionId ?? '',
      datasetRuleCode: binding.datasetRuleCode || 'OUTPUT_GRAIN_UNIQUE_NOT_NULL',
      severity: rule.severity ?? 'BLOCKING',
      maxAgeHours: String(binding.maxAgeHours),
    })
    setError('')
    setFormOpen(true)
  }

  const validate = () => {
    if (!eligibleModels.length) return '请先完成一个语义模型的认证，再绑定质量规则。'
    if (!form.targetVersionId) return '请选择已认证语义模型。'
    if (!/^[A-Za-z][A-Za-z0-9_]{0,127}$/.test(form.code.trim())) return '规则编码需以字母开头，仅使用字母、数字和下划线。'
    if (!form.name.trim()) return '请填写规则名称。'
    const maxAge = Number(form.maxAgeHours)
    if (!Number.isInteger(maxAge) || maxAge < 0 || maxAge > 8760) return '测量有效期需为 0 到 8760 的整数小时。'
    return ''
  }

  const save = async (certify: boolean) => {
    const issue = validate()
    if (issue) { setError(issue); return }
    setBusy(certify ? 'save-certify' : 'save')
    setError('')
    try {
      const payload = {
        ...(editing ? {
          objectId: editing.objectId,
          versionNo: editing.versionNo,
          expectedUpdatedAt: editing.updatedAt,
        } : {}),
        targetType: 'SEMANTIC_MODEL',
        targetVersionId: form.targetVersionId,
        code: form.code.trim(),
        name: form.name.trim(),
        ruleAst: {
          type: 'DATASET_QUALITY_BINDING',
          version: 1,
          datasetRuleCode: form.datasetRuleCode,
          scope: 'DATASET',
          maxAgeHours: Number(form.maxAgeHours),
        },
        severity: form.severity,
      }
      const result = editing
        ? await semanticAPI.update('quality-rules', editing.id, payload)
        : await semanticAPI.create('quality-rules', payload)
      if (certify) {
        await semanticAPI.certify(
          currentDomainID(),
          [result.resourceId],
          '确认该规则绑定平台真实执行的物化质量检查，并随 Release 固定。',
        )
      }
      await refresh()
      setFormOpen(false)
      onNotice('success', certify ? '质量规则已保存并认证，可进入下一份 Release。' : '质量规则草稿已保存。')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '质量规则保存失败')
    } finally { setBusy('') }
  }

  const certifyRule = async (rule: SemanticObject) => {
    setBusy(`certify:${rule.id}`)
    try {
      await semanticAPI.certify(
        currentDomainID(),
        [rule.id],
        '确认该规则绑定平台真实执行的物化质量检查，并随 Release 固定。',
      )
      await refresh()
      onNotice('success', `${ruleName(rule)}已认证，可进入下一份 Release。`)
    } catch (cause) {
      onNotice('error', cause instanceof Error ? cause.message : '质量规则认证失败')
    } finally { setBusy('') }
  }

  const deleteRule = async () => {
    if (!pendingDelete?.updatedAt) return
    setBusy(`delete:${pendingDelete.id}`)
    try {
      await semanticAPI.remove('quality-rules', pendingDelete.id, pendingDelete.updatedAt)
      await refresh()
      onNotice('success', '未认证的质量规则草稿已删除。')
      setPendingDelete(null)
    } catch (cause) {
      onNotice('error', cause instanceof Error ? cause.message : '质量规则删除失败')
    } finally { setBusy('') }
  }

  const certifiedCount = rules.filter(item => item.status === 'CERTIFIED' || item.status === 'ACTIVE').length
  const coveredModels = new Set(rules.map(item => item.targetVersionId).filter(Boolean)).size

  return <div className="semantic-quality-rules">
    <section className="semantic-quality-summary" aria-label="数据质量规则概览">
      <article><ShieldCheck size={21} /><span><small>治理规则</small><strong>{rules.length}</strong></span></article>
      <article><CheckCircle size={21} /><span><small>已认证</small><strong>{certifiedCount}</strong></span></article>
      <article><Database size={21} /><span><small>覆盖模型</small><strong>{coveredModels}/{eligibleModels.length}</strong></span></article>
      <AppButton variant="primary" disabled={!eligibleModels.length} onClick={openCreate}><Plus size={16} />新建质量规则</AppButton>
    </section>

    <section className="semantic-quality-contract">
      <div><ShieldCheck size={20} /><span><strong>规则不是一段自由表达式</strong><small>这里只绑定物化流水线真实执行的封闭检查；无执行器的规则无法创建、认证或进入 Release。</small></span></div>
      <span>物化执行 → 固定测量 → Release 读取 → 问数返回</span>
    </section>

    {eligibleModels.length === 0 && <div className="semantic-ops-state is-error">
      <WarningCircle size={25} /><strong>还没有可绑定的已认证语义模型</strong><small>先在“语义模型”完成模型认证，质量规则才能固定其真实物化快照。</small>
    </div>}

    {eligibleModels.length > 0 && rules.length === 0 && <div className="semantic-ops-state">
      <ShieldCheck size={27} /><strong>为关键模型添加第一条可执行质量规则</strong><small>建议先启用“输出粒度唯一且非空”，质量失败时保留上一份 ACTIVE 快照。</small>
      <AppButton variant="primary" onClick={openCreate}><Plus size={15} />新建质量规则</AppButton>
    </div>}

    {rules.length > 0 && <div className="semantic-quality-list">
      {rules.map(rule => {
        const binding = bindingOf(rule)
        const definition = executableRules[binding.datasetRuleCode]
        const model = models.find(item => item.id === rule.targetVersionId)
        const draft = rule.status === 'DRAFT'
        return <article key={rule.id}>
          <span className={`semantic-quality-icon is-${String(rule.severity ?? 'INFO').toLowerCase()}`}><ShieldCheck size={20} /></span>
          <div className="semantic-quality-main">
            <header><div><strong>{ruleName(rule)}</strong><small>{rule.code}</small></div><b className={`semantic-status is-${rule.status.toLowerCase()}`}>{rule.status === 'DRAFT' ? '草稿' : '已认证'}</b></header>
            <p>{definition?.description ?? '该规则绑定平台执行的物化质量检查。'}</p>
            <dl>
              <div><dt>执行检查</dt><dd>{definition?.name ?? binding.datasetRuleCode}</dd></div>
              <div><dt>绑定模型</dt><dd>{model?.name ?? rule.targetVersionId ?? '—'}</dd></div>
              <div><dt>处置级别</dt><dd>{severityLabels[rule.severity ?? 'INFO']}</dd></div>
              <div><dt>测量有效期</dt><dd>{binding.maxAgeHours ? `${binding.maxAgeHours} 小时` : '固定快照内有效'}</dd></div>
            </dl>
            <small><Clock size={14} />{definition?.evidence} · 更新于 {formatTime(rule.updatedAt)}</small>
          </div>
          <footer>
            {draft && <AppButton disabled={busy !== ''} onClick={() => openEdit(rule)}><PencilSimple size={15} />编辑</AppButton>}
            {draft && <AppButton disabled={busy !== ''} onClick={() => setPendingDelete(rule)}><Trash size={15} />删除</AppButton>}
            {draft && <AppButton variant="primary" disabled={busy !== ''} onClick={() => void certifyRule(rule)}>{busy === `certify:${rule.id}` ? '正在认证…' : '认证规则'}</AppButton>}
          </footer>
        </article>
      })}
    </div>}

    {formOpen && <div className="semantic-dialog-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !busy) setFormOpen(false) }}>
      <form className="semantic-quality-dialog" role="dialog" aria-modal="true" aria-labelledby="quality-rule-title" onSubmit={event => { event.preventDefault(); void save(false) }}>
        <header><div><span>可执行质量合同</span><h2 id="quality-rule-title">{editing ? '编辑质量规则' : '新建质量规则'}</h2><p>绑定已认证模型与真实物化检查，不接受自由 SQL 或不可执行表达式。</p></div><button type="button" aria-label="关闭质量规则" disabled={busy !== ''} onClick={() => setFormOpen(false)}><X size={20} /></button></header>
        <div className="semantic-quality-dialog-body">
          <section className="semantic-form-grid">
            <label><span>规则编码</span><input value={form.code} maxLength={128} placeholder="sales_grain_integrity" onChange={event => setForm(current => ({ ...current, code: event.target.value }))} /></label>
            <label><span>规则名称</span><input value={form.name} maxLength={200} placeholder="销售明细粒度完整性" onChange={event => setForm(current => ({ ...current, name: event.target.value }))} /></label>
            <label className="is-wide"><span>已认证语义模型</span><select value={form.targetVersionId} onChange={event => setForm(current => ({ ...current, targetVersionId: event.target.value }))}><option value="">请选择模型</option>{eligibleModels.map(model => <option value={model.id} key={model.id}>{model.name} · {model.code}</option>)}</select></label>
            <label className="is-wide"><span>平台执行检查</span><select value={form.datasetRuleCode} onChange={event => setForm(current => ({ ...current, datasetRuleCode: event.target.value as RuleCode }))}>{Object.entries(executableRules).map(([code, value]) => <option value={code} key={code}>{value.name} · {code}</option>)}</select><small>{executableRules[form.datasetRuleCode].description}</small></label>
            <label><span>处置级别</span><select value={form.severity} onChange={event => setForm(current => ({ ...current, severity: event.target.value as Severity }))}><option value="BLOCKING">阻断 · 质量失败不可用</option><option value="WARNING">告警 · 返回风险提示</option><option value="INFO">提示 · 保留测量证据</option></select></label>
            <label><span>测量有效期（小时）</span><input type="number" min="0" max="8760" step="1" value={form.maxAgeHours} onChange={event => setForm(current => ({ ...current, maxAgeHours: event.target.value }))} /><small>0 表示当前固定物化快照内持续有效。</small></label>
          </section>
          <aside><ShieldCheck size={24} /><strong>运行与失败语义</strong><ol><li>数据集物化完成时执行检查</li><li>测量结果与物化快照共同固定</li><li>阻断级失败不替换上一份 ACTIVE 快照</li><li>规则随 Release 发布，问数读取同一快照证据</li></ol></aside>
          {error && <p className="semantic-quality-error" role="alert"><WarningCircle size={16} />{error}</p>}
        </div>
        <footer><AppButton type="button" disabled={busy !== ''} onClick={() => setFormOpen(false)}>取消</AppButton><AppButton type="submit" disabled={busy !== ''}>{busy === 'save' ? '正在保存…' : '保存草稿'}</AppButton><AppButton type="button" variant="primary" disabled={busy !== ''} onClick={() => void save(true)}>{busy === 'save-certify' ? '正在认证…' : '保存并认证'}</AppButton></footer>
      </form>
    </div>}

    {pendingDelete && <div className="semantic-dialog-backdrop" role="presentation">
      <section className="semantic-quality-confirm" role="dialog" aria-modal="true" aria-labelledby="quality-delete-title">
        <WarningCircle size={30} /><div><h3 id="quality-delete-title">删除质量规则草稿？</h3><p>将删除“{ruleName(pendingDelete)}”。只有未认证、且未被其他对象引用的草稿可以删除。</p></div><footer><AppButton disabled={busy !== ''} onClick={() => setPendingDelete(null)}>取消</AppButton><AppButton variant="danger" disabled={busy !== ''} onClick={() => void deleteRule()}>{busy === `delete:${pendingDelete.id}` ? '正在删除…' : '确认删除'}</AppButton></footer>
      </section>
    </div>}
  </div>
}

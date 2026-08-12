import {
  Command,
  PencilSimple,
  Plus,
  ShieldWarning,
  Trash,
  UsersThree,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { AppButton } from '../AppButton'
import {
  semanticAPI,
  type RowAccessAttributeCoverage,
  type SemanticObject,
} from '../../lib/semantic'

type Props = {
  models: SemanticObject[]
  policies: SemanticObject[]
  onChanged: (policies: SemanticObject[]) => void
  onNotice: (tone: 'success' | 'error', message: string) => void
}

// The operators a row access condition may use. Deliberately small: a row
// access predicate compares a governed field to who the reader is, and every
// extra operator is more surface to reason about in a security control.
type ConditionOperator = 'IN' | 'NOT_IN' | 'EQUALS' | 'NOT_EQUALS'

type Condition = {
  fieldId: string
  operator: ConditionOperator
  attributeKey: string
}

type FormState = {
  code: string
  name: string
  modelVersionId: string
  conditions: Condition[]
}

const operatorLabels: Record<ConditionOperator, string> = {
  IN: '属于（多值范围）',
  NOT_IN: '不属于（多值排除）',
  EQUALS: '等于（单值）',
  NOT_EQUALS: '不等于（单值排除）',
}

// IN/NOT_IN compare against the reader's whole value list; EQUALS/NOT_EQUALS
// against a single value. Both sides are bound parameters either way.
const collectionOperators = new Set<ConditionOperator>(['IN', 'NOT_IN'])

const emptyCondition = (): Condition => ({ fieldId: '', operator: 'IN', attributeKey: '' })

const emptyForm = (models: SemanticObject[]): FormState => ({
  code: '',
  name: '',
  modelVersionId: models.find(item => item.status === 'CERTIFIED')?.id ?? '',
  conditions: [emptyCondition()],
})

/**
 * Builds the governed predicate AST from the structured form.
 *
 * A single condition becomes the predicate itself; several are ANDed, which
 * matches how the server composes policies and keeps every additional condition
 * strictly narrowing.
 */
function buildPredicate(conditions: Condition[]): Record<string, unknown> {
  const nodes = conditions.map(condition => {
    const subject = { type: 'SUBJECT_ATTRIBUTE', attributeKey: condition.attributeKey.trim() }
    return {
      type: condition.operator,
      left: { type: 'FIELD_REF', fieldId: condition.fieldId.trim() },
      right: subject,
    }
  })
  return nodes.length === 1 ? nodes[0] : { type: 'AND', arguments: nodes }
}

/** Recovers the structured form from a stored predicate, when it has the shape
 *  the builder produces. Anything richer is shown read-only rather than
 *  silently flattened into something weaker. */
function readConditions(predicate: unknown): Condition[] | null {
  const asNode = (value: unknown) => (value && typeof value === 'object' ? value as Record<string, unknown> : null)
  const root = asNode(predicate)
  if (!root) return null
  const nodes = root.type === 'AND' && Array.isArray(root.arguments)
    ? root.arguments.map(asNode)
    : [root]
  const conditions: Condition[] = []
  for (const node of nodes) {
    if (!node) return null
    const operator = String(node.type ?? '') as ConditionOperator
    if (!operatorLabels[operator]) return null
    const left = asNode(node.left)
    const right = asNode(node.right)
    if (!left || !right || left.type !== 'FIELD_REF' || right.type !== 'SUBJECT_ATTRIBUTE') return null
    conditions.push({
      fieldId: String(left.fieldId ?? ''),
      operator,
      attributeKey: String(right.attributeKey ?? ''),
    })
  }
  return conditions.length ? conditions : null
}

function policyName(policy: SemanticObject) {
  return policy.name || policy.code || '未命名行级策略'
}

export function RowAccessPoliciesPanel({ models, policies, onChanged, onNotice }: Props) {
  const eligibleModels = useMemo(
    () => models.filter(item => item.status === 'CERTIFIED' || item.status === 'ACTIVE'),
    [models],
  )
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<SemanticObject | null>(null)
  const [pendingDelete, setPendingDelete] = useState<SemanticObject | null>(null)
  const [form, setForm] = useState<FormState>(() => emptyForm(models))
  const [coverage, setCoverage] = useState<RowAccessAttributeCoverage[]>([])
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const loadCoverage = useCallback(async () => {
    try {
      const result = await semanticAPI.rowAccessCoverage()
      setCoverage(result.items ?? [])
    } catch {
      // Coverage is advisory reporting; a failure here must not make the
      // policies themselves unmanageable.
      setCoverage([])
    }
  }, [])

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadCoverage() }, 0)
    return () => window.clearTimeout(timer)
  }, [loadCoverage, policies])

  const refresh = async () => {
    const page = await semanticAPI.list('row-access-policies', undefined, 200)
    onChanged(page.items ?? [])
  }

  const openCreate = () => {
    setEditing(null)
    setForm(emptyForm(eligibleModels))
    setError('')
    setFormOpen(true)
  }

  const openEdit = (policy: SemanticObject) => {
    const conditions = readConditions(policy.predicateAst)
    setEditing(policy)
    setForm({
      code: policy.code ?? '',
      name: policy.name ?? '',
      modelVersionId: policy.modelVersionId ?? '',
      conditions: conditions ?? [emptyCondition()],
    })
    setError(conditions ? '' : '该策略的谓词结构超出表单可编辑范围，保存会以下方条件覆盖原谓词。')
    setFormOpen(true)
  }

  const validate = () => {
    if (!eligibleModels.length) return '请先完成一个语义模型的认证，再配置行级访问策略。'
    if (!form.modelVersionId) return '请选择已认证语义模型。'
    if (!/^[A-Za-z][A-Za-z0-9_]{0,127}$/.test(form.code.trim())) return '策略编码需以字母开头，仅使用字母、数字和下划线。'
    if (!form.name.trim()) return '请填写策略名称。'
    if (!form.conditions.length) return '至少需要一个条件；没有主体条件的谓词不是访问控制。'
    for (const condition of form.conditions) {
      if (!/^[A-Za-z][A-Za-z0-9_]{0,127}$/.test(condition.fieldId.trim())) {
        return '模型字段标识需以字母开头，仅使用字母、数字和下划线。'
      }
      if (!/^[a-z][a-z0-9_]{0,63}$/.test(condition.attributeKey.trim())) {
        return '主体属性键需为小写字母开头，仅使用小写字母、数字和下划线。'
      }
    }
    return ''
  }

  const submit = async () => {
    const issue = validate()
    if (issue) {
      setError(issue)
      return
    }
    setBusy('save')
    setError('')
    try {
      // subjectAttributeKeys is intentionally not sent: the server derives it
      // from the predicate so the two can never disagree.
      const payload: Record<string, unknown> = {
        modelVersionId: form.modelVersionId,
        code: form.code.trim(),
        name: form.name.trim(),
        predicateAst: buildPredicate(form.conditions),
      }
      if (editing) {
        await semanticAPI.update('row-access-policies', editing.id, {
          ...payload,
          objectId: editing.objectId,
          versionNo: editing.versionNo,
          expectedUpdatedAt: editing.updatedAt,
        })
      } else {
        await semanticAPI.create('row-access-policies', payload)
      }
      await refresh()
      await loadCoverage()
      setFormOpen(false)
      onNotice('success', editing ? '行级访问策略已更新。' : '行级访问策略草稿已创建；认证并发布后生效。')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '行级访问策略保存失败')
    } finally {
      setBusy('')
    }
  }

  const remove = async () => {
    if (!pendingDelete) return
    setBusy('delete')
    try {
      await semanticAPI.remove('row-access-policies', pendingDelete.id, pendingDelete.updatedAt ?? '')
      await refresh()
      await loadCoverage()
      setPendingDelete(null)
      onNotice('success', '行级访问策略草稿已删除。')
    } catch (cause) {
      onNotice('error', cause instanceof Error ? cause.message : '行级访问策略删除失败')
    } finally {
      setBusy('')
    }
  }

  const modelName = (id?: string) => {
    const model = models.find(item => item.id === id)
    return model ? (model.name ?? model.code ?? id) : id ?? '—'
  }

  const uncovered = coverage.filter(item => item.coveredMemberCount < item.memberCount)

  return <section className="semantic-row-access">
    <header>
      <div>
        <span>行级访问</span>
        <h3>按阅读者裁剪的行范围</h3>
        <p>
          策略绑定到语义模型，谓词把模型字段与阅读者的业务属性比较。
          <strong>阅读者没有被授予所引用属性时，该策略拒绝其全部行</strong>
          ——不放行，也不报错。多条策略以 AND 组合，只能收窄。
        </p>
      </div>
      <AppButton variant="primary" disabled={!eligibleModels.length} onClick={openCreate}>
        <Plus size={15} />
        新建策略
      </AppButton>
    </header>

    {coverage.length > 0 && <div className={`semantic-row-access-coverage ${uncovered.length ? 'is-warning' : 'is-ready'}`}>
      {uncovered.length ? <ShieldWarning size={19} weight="fill" /> : <UsersThree size={19} weight="fill" />}
      <div>
        <strong>
          {uncovered.length
            ? '存在尚未覆盖全部成员的主体属性'
            : '已认证策略引用的主体属性均已覆盖全部成员'}
        </strong>
        <ul>
          {coverage.map(item => <li className={item.coveredMemberCount < item.memberCount ? 'is-warning' : ''} key={item.attributeKey}>
            <code>{item.attributeKey}</code>
            <span>{item.policyCount} 条策略引用</span>
            <em>{item.coveredMemberCount}/{item.memberCount} 名成员已授权</em>
          </li>)}
        </ul>
        <small>
          未被授权的成员在这些策略下读不到任何行。这是设计上的失败关闭方向，
          但它是静默的，因此在发布前先在此确认覆盖率。
        </small>
      </div>
    </div>}

    {policies.length === 0
      ? <div className="semantic-ops-state">
          <Command size={26} />
          <strong>当前领域没有行级访问策略</strong>
          <small>未配置时，模型的行范围仅由平台权限裁剪决定</small>
        </div>
      : <ul className="semantic-row-access-list">
          {policies.map(policy => <li key={policy.id}>
            <div className="semantic-row-access-head">
              <strong>{policyName(policy)}</strong>
              <em className={`semantic-status is-${String(policy.status).toLowerCase()}`}>{policy.status}</em>
            </div>
            <dl>
              <div><dt>作用模型</dt><dd>{modelName(policy.modelVersionId)}</dd></div>
              <div><dt>引用主体属性</dt><dd>{(policy.subjectAttributeKeys ?? []).join('、') || '—'}</dd></div>
              <div><dt>编码</dt><dd>{policy.code ?? '—'}</dd></div>
            </dl>
            {policy.status === 'DRAFT' && <footer>
              <AppButton disabled={busy !== ''} onClick={() => openEdit(policy)}>
                <PencilSimple size={14} />
                编辑
              </AppButton>
              <AppButton variant="danger" disabled={busy !== ''} onClick={() => setPendingDelete(policy)}>
                <Trash size={14} />
                删除
              </AppButton>
            </footer>}
          </li>)}
        </ul>}

    {formOpen && <div className="semantic-dialog-backdrop" role="presentation">
      <section className="semantic-row-access-dialog" role="dialog" aria-modal="true" aria-label="行级访问策略">
        <header>
          <div>
            <span>行级访问策略</span>
            <h2>{editing ? '编辑策略草稿' : '新建策略草稿'}</h2>
          </div>
          <button type="button" aria-label="关闭" onClick={() => setFormOpen(false)}><X size={18} /></button>
        </header>
        <div className="semantic-row-access-form">
          <label>
            <span>作用模型</span>
            <select
              value={form.modelVersionId}
              onChange={event => setForm(current => ({ ...current, modelVersionId: event.target.value }))}
            >
              <option value="">请选择已认证语义模型</option>
              {eligibleModels.map(model => <option value={model.id} key={model.id}>
                {model.name ?? model.code}
              </option>)}
            </select>
          </label>
          <div className="semantic-row-access-pair">
            <label>
              <span>策略编码</span>
              <input
                value={form.code}
                placeholder="region_scope"
                onChange={event => setForm(current => ({ ...current, code: event.target.value }))}
              />
            </label>
            <label>
              <span>策略名称</span>
              <input
                value={form.name}
                placeholder="按负责区域裁剪"
                onChange={event => setForm(current => ({ ...current, name: event.target.value }))}
              />
            </label>
          </div>

          <div className="semantic-row-access-conditions">
            <header>
              <strong>条件</strong>
              <small>全部条件以 AND 组合；每个条件都把一个模型字段与阅读者的一项属性比较。</small>
            </header>
            {form.conditions.map((condition, index) => <div className="semantic-row-access-condition" key={index}>
              <label>
                <span>模型字段</span>
                <input
                  value={condition.fieldId}
                  placeholder="region_code"
                  onChange={event => setForm(current => ({
                    ...current,
                    conditions: current.conditions.map((item, position) =>
                      position === index ? { ...item, fieldId: event.target.value } : item),
                  }))}
                />
              </label>
              <label>
                <span>比较方式</span>
                <select
                  value={condition.operator}
                  onChange={event => setForm(current => ({
                    ...current,
                    conditions: current.conditions.map((item, position) =>
                      position === index ? { ...item, operator: event.target.value as ConditionOperator } : item),
                  }))}
                >
                  {(Object.keys(operatorLabels) as ConditionOperator[]).map(operator =>
                    <option value={operator} key={operator}>{operatorLabels[operator]}</option>)}
                </select>
              </label>
              <label>
                <span>阅读者属性键</span>
                <input
                  value={condition.attributeKey}
                  placeholder="region_code"
                  onChange={event => setForm(current => ({
                    ...current,
                    conditions: current.conditions.map((item, position) =>
                      position === index ? { ...item, attributeKey: event.target.value } : item),
                  }))}
                />
              </label>
              {form.conditions.length > 1 && <AppButton
                text
                circle
                aria-label="移除条件"
                onClick={() => setForm(current => ({
                  ...current,
                  conditions: current.conditions.filter((_, position) => position !== index),
                }))}
              ><X size={15} /></AppButton>}
              {collectionOperators.has(condition.operator)
                ? <small>阅读者该属性的全部取值都会作为绑定参数参与比较。</small>
                : <small>阅读者该属性需恰好一个取值，多值会在服务端被拒绝。</small>}
            </div>)}
            <AppButton
              disabled={form.conditions.length >= 8}
              onClick={() => setForm(current => ({ ...current, conditions: [...current.conditions, emptyCondition()] }))}
            >
              <Plus size={14} />
              添加条件
            </AppButton>
          </div>

          {error && <p className="semantic-row-access-error"><WarningCircle size={16} />{error}</p>}
        </div>
        <footer>
          <AppButton onClick={() => setFormOpen(false)}>取消</AppButton>
          <AppButton variant="primary" disabled={busy !== ''} onClick={() => void submit()}>
            {busy === 'save' ? '正在保存…' : '保存草稿'}
          </AppButton>
        </footer>
      </section>
    </div>}

    {pendingDelete && <div className="semantic-dialog-backdrop" role="presentation">
      <section className="semantic-row-access-confirm" role="dialog" aria-modal="true" aria-label="删除行级访问策略">
        <strong>删除「{policyName(pendingDelete)}」草稿？</strong>
        <p>
          删除后该模型少一层行级收窄。已认证或已发布的版本不受影响，
          正在运行的 Release 继续沿用其冻结的策略集。
        </p>
        <footer>
          <AppButton onClick={() => setPendingDelete(null)}>取消</AppButton>
          <AppButton variant="danger" disabled={busy !== ''} onClick={() => void remove()}>
            {busy === 'delete' ? '正在删除…' : '确认删除'}
          </AppButton>
        </footer>
      </section>
    </div>}
  </section>
}

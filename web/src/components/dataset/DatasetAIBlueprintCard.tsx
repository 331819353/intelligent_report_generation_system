import { useMemo, useState } from 'react'
import { XIcon } from '@phosphor-icons/react'
import type { AssetTable } from '../../lib/datasets'
import {
  datasetAIBlueprintStageOrder,
  datasetAIStageLabels,
  type DatasetAIBlueprint,
  type DatasetAIBlueprintStage,
  type DatasetAIJoinKey,
  type DatasetAIMetricAggregation,
  type DatasetAISessionModelKind,
  type DatasetAIStageDecision,
  type DatasetAIStageResolution,
} from '../../lib/dataset-ai-session'

/**
 * The modeling blueprint card walks the user through the stage protocol
 * (docs/10 §2) in two passes: business grain/metric decisions before source
 * retrieval, then physical joins/bindings/transforms/filters/outputs.
 * Only the earliest unresolved stage is expanded. Confirming it reveals the next
 * applicable stage, so later decisions cannot distract from (or accidentally settle)
 * the current one. Confirmed predecessors remain as compact context; rule-skipped
 * stages are omitted from the active wizard and stay available in the final recap.
 */

const aggregationOptions: DatasetAIMetricAggregation[] = ['SUM', 'AVG', 'COUNT', 'COUNT_DISTINCT', 'MIN', 'MAX']
const arithmeticLabels: Record<string, string> = { ADD: '相加', SUBTRACT: '相减', MULTIPLY: '相乘', DIVIDE: '相除' }
const filterOperators = ['EQUALS', 'NOT_EQUALS', 'GT', 'GTE', 'LT', 'LTE', 'CONTAINS', 'NOT_CONTAINS', 'IN', 'NOT_IN', 'IS_NULL', 'IS_NOT_NULL']
const operatorLabels: Record<string, string> = {
  EQUALS: '等于', NOT_EQUALS: '不等于', GT: '大于', GTE: '大于等于', LT: '小于', LTE: '小于等于',
  CONTAINS: '包含', NOT_CONTAINS: '不包含', IN: '在列表中', NOT_IN: '不在列表中', IS_NULL: '为空', IS_NOT_NULL: '不为空',
}
const transformLabels: Record<string, string> = {
  TEXT_CASE: '大小写', TEXT_TRIM: '去空格', TEXT_REPLACE: '替换', TEXT_SUBSTRING: '截取', TEXT_CONCAT: '拼接',
  NUMBER_ABSOLUTE: '绝对值', NUMBER_ROUNDING: '取整', NUMBER_ARITHMETIC: '数值运算', DATE_CALCULATION: '日期计算',
  DATE_FORMAT: '日期格式化', NULL: '空值填充', CAST: '类型转换', CONDITION: '条件映射',
}
const statusLabels: Record<DatasetAIStageDecision['status'], string> = {
  PROPOSED: '待确认', AUTO_CONFIRMED: '已自动确认', USER_CONFIRMED: '已确认', SKIPPED: '已跳过',
}
const grainLabels: Record<string, string> = { '': '不按时间', DAY: '按日', WEEK: '按周', MONTH: '按月', QUARTER: '按季', YEAR: '按年' }
const optionalStages: DatasetAIBlueprintStage[] = ['TRANSFORM', 'FILTER']

const cloneDecision = (decision: DatasetAIStageDecision): DatasetAIStageDecision => JSON.parse(JSON.stringify(decision)) as DatasetAIStageDecision
const sameDecision = (left: DatasetAIStageDecision, right: DatasetAIStageDecision) => JSON.stringify(left) === JSON.stringify(right)

/** Only the payload of the stage itself is sent back; the server rejects mixed payloads. */
const payloadOf = (decision: DatasetAIStageDecision): Partial<DatasetAIStageDecision> => {
  const base: Partial<DatasetAIStageDecision> = { stage: decision.stage, status: decision.status, source: decision.source, confidence: decision.confidence, needsUserConfirmation: decision.needsUserConfirmation, reason: decision.reason, decidedAt: decision.decidedAt }
  switch (decision.stage) {
    case 'GRAIN': return { ...base, grain: decision.grain }
    case 'METRIC_DEFINITION': return { ...base, metrics: decision.metrics }
    case 'JOIN': return { ...base, joins: decision.joins }
    case 'METRIC_BINDING': return { ...base, bindings: decision.bindings }
    case 'TRANSFORM': return { ...base, transforms: decision.transforms }
    case 'FILTER': return { ...base, filters: decision.filters }
    case 'OUTPUT': return { ...base, outputs: decision.outputs }
  }
}

export function DatasetAIBlueprintCard({ blueprint, modelKind, tables, scopeTableCount, busy, collapsed, canGenerate = true, onResolve, onConfirmAll }: {
  blueprint: DatasetAIBlueprint
  modelKind: DatasetAISessionModelKind
  tables: AssetTable[]
  scopeTableCount: number
  busy: boolean
  /** While a DAG proposal is being reviewed the card is a read-only recap of the confirmed calibre. */
  collapsed: boolean
  /** False on an existing canvas: stages can still be adjusted (MODIFY re-entry) but generation goes through modification prompts. */
  canGenerate?: boolean
  onResolve: (resolution: DatasetAIStageResolution) => void
  onConfirmAll: () => void
}) {
  const [edits, setEdits] = useState<Partial<Record<DatasetAIBlueprintStage, DatasetAIStageDecision>>>({})
  // Local edits belong to one server revision of the blueprint: a new generation or a
  // resolved stage replaces the payloads, so pending edits are dropped (render-time
  // reset instead of an effect, per React's derived-state guidance).
  const revision = `${blueprint.generatedAt}|${blueprint.stages.map(stage => `${stage.stage}:${stage.status}:${stage.decidedAt}`).join(',')}`
  const [editsRevision, setEditsRevision] = useState(revision)
  if (editsRevision !== revision) {
    setEditsRevision(revision)
    setEdits({})
  }
  const [expandedOverride, setExpandedOverride] = useState<{ collapsed: boolean; value: boolean } | null>(null)
  const expanded = expandedOverride && expandedOverride.collapsed === collapsed ? expandedOverride.value : !collapsed
  const setExpanded = (value: boolean) => setExpandedOverride({ collapsed, value })
  const tableByID = useMemo(() => new Map(tables.map(table => [table.id, table])), [tables])
  const tableLabel = (tableId: string) => {
    const table = tableByID.get(tableId)
    return table ? (table.businessName || table.tableName) : tableId
  }
  const fieldLabel = (tableId: string, column: string) => `${tableLabel(tableId)} · ${column}`
  const stages = datasetAIBlueprintStageOrder.flatMap(stage => blueprint.stages.filter(item => item.stage === stage))
  const metricNames = new Map((stages.find(stage => stage.stage === 'METRIC_DEFINITION')?.metrics ?? []).map(metric => [metric.id, metric.name]))
  const pending = stages.filter(stage => stage.status === 'PROPOSED')
	const businessPhase = blueprint.phase === 'BUSINESS'
  const activeStage = pending[0]
  const activeStageIndex = activeStage ? stages.indexOf(activeStage) : -1
  const visibleStages = collapsed || !activeStage
    ? stages
    : stages.filter((stage, index) => index <= activeStageIndex && !(stage.status === 'SKIPPED' && stage.source === 'RULE'))
  const decisionFor = (stage: DatasetAIStageDecision) => edits[stage.stage] ?? stage
  const update = (stage: DatasetAIStageDecision, mutate: (draft: DatasetAIStageDecision) => void) => {
    setEdits(current => {
      const draft = cloneDecision(current[stage.stage] ?? stage)
      mutate(draft)
      return { ...current, [stage.stage]: draft }
    })
  }
  const confirm = (stage: DatasetAIStageDecision) => {
    const edited = edits[stage.stage]
    onResolve({ stage: stage.stage, action: 'CONFIRM', ...(edited && !sameDecision(edited, stage) ? { decision: payloadOf(edited) } : {}) })
  }
  const canSkip = (stage: DatasetAIStageDecision) => optionalStages.includes(stage.stage) || (stage.stage === 'JOIN' && scopeTableCount < 2)

  const renderBody = (stage: DatasetAIStageDecision) => {
    const decision = decisionFor(stage)
    const editable = stage.status === 'PROPOSED' && !busy && !collapsed
    switch (stage.stage) {
      case 'GRAIN': {
        const grain = decision.grain
        if (!grain) return null
        return <div className="dataset-ai-blueprint-grain">
          <label className="dataset-ai-blueprint-field is-wide">
            <span>每行代表</span>
            {editable
              ? <textarea aria-label="每行代表" rows={2} value={grain.description} onChange={event => update(stage, draft => { if (draft.grain) draft.grain.description = event.target.value })} />
              : <strong>{grain.description}</strong>}
          </label>
          <label className="dataset-ai-blueprint-field">
            <span>粒度键</span>
            {editable
              ? <input aria-label="粒度键" value={grain.keys.join('、')} placeholder="例如：销售订单、订单行" onChange={event => update(stage, draft => { if (draft.grain) draft.grain.keys = event.target.value.split(/[、,，]/).map(key => key.trim()).filter(Boolean) })} />
              : <span className="dataset-ai-blueprint-values">{grain.keys.length ? grain.keys.map(key => <em key={key}>{key}</em>) : <small>未指定</small>}</span>}
          </label>
          <label className="dataset-ai-blueprint-field">
            <span>时间口径</span>
            {editable
              ? <select aria-label="时间口径" value={grain.timeGrain ?? ''} onChange={event => update(stage, draft => { if (draft.grain) draft.grain.timeGrain = event.target.value as NonNullable<typeof grain.timeGrain> })}>{Object.entries(grainLabels).map(([value, label]) => <option value={value} key={value || 'none'}>{label}</option>)}</select>
              : <strong>{grainLabels[grain.timeGrain ?? '']}</strong>}
          </label>
          {grain.timeField && <div className="dataset-ai-blueprint-field is-wide"><span>时间字段</span><strong>{fieldLabel(grain.timeField.tableId, grain.timeField.column)}</strong></div>}
        </div>
      }
      case 'METRIC_DEFINITION':
        return <ul>{(decision.metrics ?? []).map((metric, index) => <li key={metric.id}>
          <span>
            {editable ? <input aria-label={`指标名称 ${index + 1}`} value={metric.name} onChange={event => update(stage, draft => { if (draft.metrics) draft.metrics[index].name = event.target.value })} /> : <strong>{metric.name}</strong>}
            {editable ? <input aria-label={`指标口径 ${index + 1}`} value={metric.definition} placeholder="业务口径" onChange={event => update(stage, draft => { if (draft.metrics) draft.metrics[index].definition = event.target.value })} /> : <small>{metric.definition}{metric.origin === 'REGISTRY' && metric.registryCode ? ` · 已治理指标 ${metric.registryCode}` : ''}</small>}
          </span>
          {editable && (decision.metrics?.length ?? 0) > 1 && <button type="button" aria-label={`移除指标${metric.name}`} onClick={() => update(stage, draft => { draft.metrics = (draft.metrics ?? []).filter((_, position) => position !== index) })}><XIcon size={13} weight="bold" /></button>}
        </li>)}</ul>
      case 'JOIN':
        return <ul>{(decision.joins ?? []).map((join, index) => {
          const original = stage.joins?.[index]
          const options: Array<{ keys: DatasetAIJoinKey[]; reason: string }> = original
            ? [{ keys: original.keys, reason: original.reason || '系统推荐' }, ...(original.alternatives ?? [])]
            : [{ keys: join.keys, reason: '' }]
          const keyText = (keys: DatasetAIJoinKey[]) => keys.map(key => `${key.leftColumn} = ${key.rightColumn}`).join('，')
          const selected = options.findIndex(option => keyText(option.keys) === keyText(join.keys))
          return <li key={join.id}>
            <span>
              <strong>{tableLabel(join.leftTableId)} {editable
                ? <select aria-label={`关联方式 ${index + 1}`} value={join.joinType} onChange={event => update(stage, draft => { if (draft.joins) draft.joins[index].joinType = event.target.value as 'INNER' | 'LEFT' })}><option value="LEFT">LEFT 关联</option><option value="INNER">INNER 关联</option></select>
                : `${join.joinType} 关联`} {tableLabel(join.rightTableId)}</strong>
              {editable && options.length > 1
                ? <span className="dataset-ai-blueprint-options" role="radiogroup" aria-label="关联键选择">{options.map((option, optionIndex) => <label key={optionIndex}><input type="radio" name={`join-${join.id}`} checked={selected === optionIndex} onChange={() => update(stage, draft => { if (draft.joins) { draft.joins[index].keys = option.keys.map(key => ({ ...key })); draft.joins[index].alternatives = []; draft.joins[index].provenance = 'USER' } })} /><span>{keyText(option.keys)}<small>{option.reason}</small></span></label>)}</span>
                : <small>关联键：{keyText(join.keys)}{join.cardinality && join.cardinality !== 'UNKNOWN' ? ` · ${join.cardinality.replace('_TO_', ' : ').replace('MANY', '多').replace('ONE', '一').replace('MANY', '多').replace('ONE', '一')}` : ''}{join.reason ? ` · ${join.reason}` : ''}</small>}
            </span>
          </li>
        })}</ul>
      case 'METRIC_BINDING':
        return <ul>{(decision.bindings ?? []).map((binding, index) => <li key={binding.metricId}>
          <span>
            <strong>{metricNames.get(binding.metricId) ?? binding.metricId}</strong>
            {(binding.mode ?? 'AGGREGATE') === 'AGGREGATE' && binding.tableId && binding.column && <small>{editable
              ? <select aria-label={`聚合方式 ${index + 1}`} value={binding.aggregation} onChange={event => update(stage, draft => { if (draft.bindings) { draft.bindings[index].aggregation = event.target.value as DatasetAIMetricAggregation; draft.bindings[index].distinct = event.target.value === 'COUNT_DISTINCT' } })}>{aggregationOptions.map(option => <option value={option} key={option}>{option}</option>)}</select>
              : binding.aggregation}（{fieldLabel(binding.tableId, binding.column)}）{binding.note ? ` · ${binding.note}` : ''}</small>}
            {binding.mode === 'PASSTHROUGH' && binding.tableId && binding.column && <small>直接使用上游指标（{fieldLabel(binding.tableId, binding.column)}）{binding.note ? ` · ${binding.note}` : ''}</small>}
            {binding.mode === 'DERIVED' && <small>{arithmeticLabels[binding.operation ?? ''] ?? binding.operation ?? '指标计算'}：{(binding.inputs ?? []).map(input => fieldLabel(input.tableId, input.column)).join('、')}{binding.note ? ` · ${binding.note}` : ''}</small>}
          </span>
        </li>)}</ul>
      case 'TRANSFORM':
        return <ul>{(decision.transforms ?? []).map((transform, index) => <li key={index}>
          <span><strong>{transformLabels[transform.componentType] ?? transform.componentType}{transform.operation ? ` · ${transform.operation}` : ''}<small className="dataset-ai-blueprint-tag">{transform.placement === 'BEFORE_GROUP' ? '分组前' : '仅展示'}</small></strong><small>{transform.description} · {transform.inputs.map(input => fieldLabel(input.tableId, input.column)).join('、')}</small></span>
          {editable && <button type="button" aria-label="移除该转换" onClick={() => update(stage, draft => { draft.transforms = (draft.transforms ?? []).filter((_, position) => position !== index) })}><XIcon size={13} weight="bold" /></button>}
        </li>)}</ul>
      case 'FILTER':
        return <ul>{(decision.filters ?? []).map((filter, index) => <li key={index}>
          <span>
            <strong>{fieldLabel(filter.tableId, filter.column)}</strong>
            {editable
              ? <small className="dataset-ai-blueprint-inline"><select aria-label={`过滤方式 ${index + 1}`} value={filter.operator} onChange={event => update(stage, draft => { if (draft.filters) draft.filters[index].operator = event.target.value })}>{filterOperators.map(option => <option value={option} key={option}>{operatorLabels[option]}</option>)}</select>{!['IS_NULL', 'IS_NOT_NULL'].includes(filter.operator) && <input aria-label={`过滤值 ${index + 1}`} value={filter.value} onChange={event => update(stage, draft => { if (draft.filters) draft.filters[index].value = event.target.value })} />}</small>
              : <small>{operatorLabels[filter.operator] ?? filter.operator}{['IS_NULL', 'IS_NOT_NULL'].includes(filter.operator) ? '' : ` ${filter.valueMode === 'FIELD' ? '字段 ' : ''}${filter.value}`}</small>}
          </span>
          {editable && <button type="button" aria-label="移除该过滤条件" onClick={() => update(stage, draft => { draft.filters = (draft.filters ?? []).filter((_, position) => position !== index) })}><XIcon size={13} weight="bold" /></button>}
        </li>)}</ul>
      case 'OUTPUT':
        return <ul>{(decision.outputs ?? []).map((output, index) => <li key={`${output.code}-${index}`}>
          <span>
            {editable ? <input aria-label={`输出名称 ${index + 1}`} value={output.name} onChange={event => update(stage, draft => { if (draft.outputs) draft.outputs[index].name = event.target.value })} /> : <strong>{output.name}</strong>}
            <small>{output.code} · {output.metricId ? `指标：${metricNames.get(output.metricId) ?? output.metricId}` : output.source ? fieldLabel(output.source.tableId, output.source.column) : ''}</small>
          </span>
          {editable && (decision.outputs?.length ?? 0) > 1 && <button type="button" aria-label={`移除输出${output.name}`} onClick={() => update(stage, draft => { draft.outputs = (draft.outputs ?? []).filter((_, position) => position !== index) })}><XIcon size={13} weight="bold" /></button>}
        </li>)}</ul>
    }
  }

  return <section className={`dataset-ai-scene-card dataset-ai-blueprint-card${collapsed ? ' is-collapsed' : ''}`} aria-label="建模蓝图确认">
    <header>
      <div><span>{collapsed ? '建模蓝图' : activeStage ? `正在确认：${datasetAIStageLabels[activeStage.stage]}` : businessPhase ? '业务口径已确认' : '蓝图已确认'}</span><strong>{collapsed ? '本次 DAG 遵循的建模口径' : businessPhase ? '当前项确认后，再进入下一项' : canGenerate ? '请逐段确认物理实现口径，或直接说要怎么改' : '建模口径'}</strong></div>
      <button type="button" onClick={() => setExpanded(!expanded)}>{expanded ? '收起' : '展开'}</button>
    </header>
    {blueprint.summary && <p>{blueprint.summary}</p>}
    {blueprint.knowledge && <small className="dataset-ai-blueprint-knowledge">{blueprint.knowledge.available
      ? `已参考业务知识：${[
        blueprint.knowledge.metrics ? `${blueprint.knowledge.metrics} 个已治理指标${blueprint.knowledge.metricCodes?.length ? `（${blueprint.knowledge.metricCodes.join('、')}）` : ''}` : '',
        blueprint.knowledge.dimensions ? `${blueprint.knowledge.dimensions} 个已治理维度` : '',
        blueprint.knowledge.relationships ? `${blueprint.knowledge.relationships} 条已认证关联` : '',
        blueprint.knowledge.terms ? `${blueprint.knowledge.terms} 个业务术语` : '',
      ].filter(Boolean).join('、') || '无命中'}${blueprint.knowledge.degraded ? `（${({ NO_ACTIVE_RELEASE: '所选范围没有已发布的语义 release', ACTOR_HAS_NO_ROLES: '当前用户没有可用角色', EMBEDDING_UNAVAILABLE: '向量检索不可用，仅按词典与关键词', RETRIEVER_UNAVAILABLE: '检索器未配置' } as Record<string, string>)[blueprint.knowledge.degradedReason ?? ''] ?? blueprint.knowledge.degradedReason ?? '部分能力降级'}）` : ''}`
      : '未接入业务知识：本蓝图仅基于表结构与字段元数据。'}</small>}
    {(blueprint.revisions?.length ?? 0) > 0 && <ol className="dataset-ai-blueprint-revisions">
      {blueprint.revisions!.slice(-3).map((revision, index) => <li key={`${revision.at}-${index}`}><strong>“{revision.instruction}”</strong><small>{revision.changedStages.length ? `改动：${revision.changedStages.map(stage => datasetAIStageLabels[stage]).join('、')}` : '无改动'}</small></li>)}
    </ol>}
    {expanded && <ol className="dataset-ai-blueprint-stages">
      {visibleStages.map(stage => {
        const applicable = stage.status !== 'SKIPPED' || stage.source !== 'RULE'
        const current = stage === activeStage
        const compact = Boolean(activeStage) && !current
        return <li key={stage.stage} className={`is-${stage.status.toLowerCase()}${stage.needsUserConfirmation ? ' needs-confirmation' : ''}${current ? ' is-current' : ''}${compact ? ' is-prior' : ''}`}>
          <div className="dataset-ai-blueprint-stage-head">
            <strong>{datasetAIStageLabels[stage.stage]}</strong>
            <em>{statusLabels[stage.status]}{stage.status !== 'SKIPPED' && stage.source === 'LLM' && stage.confidence > 0 ? ` · 把握 ${Math.round(stage.confidence * 100)}%` : ''}</em>
          </div>
          {!compact && stage.reason && <small className="dataset-ai-blueprint-reason">{stage.reason}</small>}
          {!compact && stage.status !== 'SKIPPED' && renderBody(stage)}
          {!collapsed && applicable && (!activeStage || current || compact) && <div className="dataset-ai-blueprint-actions">
            {stage.status === 'PROPOSED' && <button type="button" className="is-primary" disabled={busy} onClick={() => confirm(stage)}>确认{edits[stage.stage] && !sameDecision(edits[stage.stage]!, stage) ? '修改' : ''}</button>}
            {stage.status === 'PROPOSED' && canSkip(stage) && <button type="button" disabled={busy} onClick={() => onResolve({ stage: stage.stage, action: 'SKIP' })}>不需要</button>}
            {(stage.status === 'AUTO_CONFIRMED' || stage.status === 'USER_CONFIRMED' || (stage.status === 'SKIPPED' && stage.source !== 'RULE')) && <button type="button" disabled={busy} onClick={() => onResolve({ stage: stage.stage, action: 'REOPEN' })}>调整</button>}
          </div>}
        </li>
      })}
    </ol>}
    {!collapsed && <footer>
      <span>{pending.length ? `请先确认“${datasetAIStageLabels[activeStage!.stage]}”，确认后会自动展示下一项` : businessPhase ? '业务口径已落定，下一步只检索能实现这些口径的数据表' : `${modelKind} 建模口径已全部确认${canGenerate ? '' : '；后续修改会自动同步到这里'}`}</span>
      {canGenerate && pending.length === 0 && <button type="button" disabled={busy || Object.values(edits).some(edit => edit && stages.some(stage => stage.stage === edit.stage && !sameDecision(edit, stage)))} onClick={onConfirmAll}>{businessPhase ? '筛选数据来源' : '生成 DAG'}</button>}
    </footer>}
  </section>
}

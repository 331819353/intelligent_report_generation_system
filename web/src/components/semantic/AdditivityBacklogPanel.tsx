import { ArrowClockwise, CheckCircle, Function as FunctionIcon, Sigma, UserFocus, WarningCircle } from '@phosphor-icons/react'
import { useCallback, useEffect, useState } from 'react'
import { AppButton } from '../AppButton'
import {
  semanticAPI,
  type AdditivityCandidate,
  type AdditivityReadiness,
} from '../../lib/semantic'

type Props = {
  readiness: AdditivityReadiness
  onNotice: (tone: 'success' | 'error', message: string) => void
  onConfirmed: () => void | Promise<void>
}

const additivityLabels: Record<string, string> = {
  FULLY_ADDITIVE: '完全可加',
  SEMI_ADDITIVE: '半可加',
  NON_ADDITIVE: '不可加',
}

// Each rule states, in business terms, why the heuristic reached its suggestion.
// The Owner confirms the caliber, not the rule, so the reason has to be legible.
const ruleLabels: Record<string, string> = {
  FORMULA_ROOT_DIVIDE: '公式根节点是除法，跨行求和会得出错误比率',
  COUNT_DISTINCT: '聚合为去重计数，分组求和会重复计数',
  RATIO_LEXICON: '名称或别名含比率类词汇',
  SNAPSHOT_LEXICON: '名称或别名含时点快照类词汇',
  SNAPSHOT_GRAIN: '模型粒度声明为时点快照，时间维度上不可求和',
  SUM_AMOUNT_OR_QUANTITY: '按金额或数量求和，跨维度可加',
  NEEDS_HUMAN: '确定性规则无法判定，必须由 Owner 判断',
}

/**
 * ADD-002 additivity backfill worklist.
 *
 * The heuristic is advisory and is evaluated server-side; this panel makes the
 * two governed steps explicit and separate:
 *
 *  1. refresh - recompute and persist suggestions for unconfirmed draft metrics.
 *     Without this the confirmation path had nothing to match against, because
 *     suggestions were only ever written by the askdata-inventory CLI.
 *  2. confirm - an Owner accepts one suggested value for a whole group. This is
 *     the human act gated by HUMAN-008; the browser never proposes a value, and
 *     metrics the heuristic could not decide are never confirmed in bulk.
 */
export function AdditivityBacklogPanel({ readiness, onNotice, onConfirmed }: Props) {
  const [candidates, setCandidates] = useState<AdditivityCandidate[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const page = await semanticAPI.additivityCandidates()
      setCandidates(page.items ?? [])
    } catch (cause) {
      setCandidates([])
      setError(cause instanceof Error ? cause.message : '可加性补录清单读取失败')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() }, 0)
    return () => window.clearTimeout(timer)
  }, [load])

  const refresh = async () => {
    setBusy('refresh')
    try {
      const result = await semanticAPI.refreshAdditivitySuggestions()
      await load()
      onNotice(
        'success',
        `已评估 ${result.evaluatedCount} 个未确认指标，保存 ${result.persistedCount} 条建议，`
        + `${result.needsHumanCount} 个需要 Owner 直接判断。`,
      )
    } catch (cause) {
      onNotice('error', cause instanceof Error ? cause.message : '可加性建议生成失败')
    } finally { setBusy('') }
  }

  const confirmGroup = async (suggestion: string, ids: string[]) => {
    setBusy(`confirm:${suggestion}`)
    try {
      const result = await semanticAPI.confirmAdditivity(ids, suggestion)
      await load()
      await onConfirmed()
      onNotice('success', `已确认 ${result.confirmedCount} 个指标为「${additivityLabels[suggestion] ?? suggestion}」。`)
    } catch (cause) {
      onNotice('error', cause instanceof Error ? cause.message : '批量确认失败')
    } finally { setBusy('') }
  }

  // Only metrics whose suggestion is already persisted can be confirmed in bulk:
  // the server matches the confirmed value against the stored suggestion.
  const groups = new Map<string, AdditivityCandidate[]>()
  const needsHuman: AdditivityCandidate[] = []
  candidates.forEach(item => {
    const persisted = item.persistedSuggestion ?? ''
    if (!persisted || item.suggestion.needsHuman) { needsHuman.push(item); return }
    groups.set(persisted, [...(groups.get(persisted) ?? []), item])
  })

  const rate = Math.round((readiness.confirmationRate ?? 0) * 100)

  return <section className="semantic-additivity-backlog">
    <header>
      <div>
        <span>可加性补录</span>
        <h3>未确认指标批量确认</h3>
        <p>建议由服务端确定性规则给出且仅供参考；进入首个 Release 前每个指标都必须由 Owner 明确确认。</p>
      </div>
      <div className="semantic-additivity-actions">
        <strong>{readiness.confirmedCount}/{readiness.metricCount} 已确认 · {rate}%</strong>
        <AppButton disabled={busy !== ''} onClick={() => void refresh()}>
          <ArrowClockwise size={15} />
          {busy === 'refresh' ? '正在生成…' : '生成可加性建议'}
        </AppButton>
      </div>
    </header>

    {loading && <div className="semantic-ops-state"><Sigma size={24} /><strong>正在读取补录清单</strong></div>}

    {!loading && error && <div className="semantic-ops-state is-error">
      <WarningCircle size={24} /><strong>{error}</strong>
      <AppButton onClick={() => void load()}>重新加载</AppButton>
    </div>}

    {!loading && !error && candidates.length === 0 && <div className="semantic-ops-state">
      <CheckCircle size={26} />
      <strong>所有草稿指标均已确认可加性</strong>
      <small>新建或导入指标后，未确认项会重新出现在这里</small>
    </div>}

    {!loading && !error && candidates.length > 0 && <div className="semantic-additivity-groups">
      {[...groups.entries()].map(([suggestion, items]) => <article key={suggestion}>
        <header>
          <div>
            <strong>建议为「{additivityLabels[suggestion] ?? suggestion}」</strong>
            <small>{items.length} 个指标 · 确认后写入不可变内容哈希与确认人</small>
          </div>
          <AppButton
            variant="primary"
            disabled={busy !== ''}
            onClick={() => void confirmGroup(suggestion, items.map(item => item.metricVersionId))}
          >
            {busy === `confirm:${suggestion}` ? '正在确认…' : `确认这 ${items.length} 个指标`}
          </AppButton>
        </header>
        <ul>
          {items.map(item => <li key={item.metricVersionId}>
            <FunctionIcon size={15} />
            <div>
              <strong>{item.metricName}</strong>
              <small>{item.metricCode} · {ruleLabels[item.persistedRuleId ?? ''] ?? item.persistedRuleId}</small>
            </div>
          </li>)}
        </ul>
      </article>)}

      {needsHuman.length > 0 && <article className="is-manual">
        <header>
          <div>
            <strong>需要 Owner 逐项判断</strong>
            <small>{needsHuman.length} 个指标 · 确定性规则无法判定，不提供批量确认</small>
          </div>
        </header>
        <ul>
          {needsHuman.map(item => <li key={item.metricVersionId}>
            <UserFocus size={15} />
            <div>
              <strong>{item.metricName}</strong>
              <small>{item.metricCode} · {ruleLabels[item.suggestion.ruleId] ?? item.suggestion.ruleId}</small>
            </div>
          </li>)}
        </ul>
      </article>}
    </div>}
  </section>
}

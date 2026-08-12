import { Eye, LockKey, ShieldCheck, ShieldWarning, WarningCircle } from '@phosphor-icons/react'
import { useCallback, useEffect, useState } from 'react'
import { AppButton } from '../AppButton'
import {
  semanticAPI,
  type EvaluationSetCatalogItem,
  type EvaluationShardHealth,
} from '../../lib/semantic'

type Props = {
  evaluationSets: EvaluationSetCatalogItem[]
  onNotice: (tone: 'success' | 'error', message: string) => void
}

const retireReasonLabels: Record<string, string> = {
  USAGE_LIMIT: '使用次数达上限',
  EXPOSED: '已曝光',
  SUPERSEDED: '已被取代',
}

function formatTime(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value))
}

/**
 * Sealed evaluation set integrity.
 *
 * This panel reports how much sealed material is still usable and lets an
 * operator declare that a shard has been seen. It deliberately shows no sealed
 * question text: there is no read interface for sealed case bodies anywhere in
 * the product, because anything that can display them lets a viewer drain the
 * set and invalidate the 95 percent gate those cases exist to prove. Exposure is
 * therefore a declaration, and it retires a whole shard rather than one sample.
 */
export function SealedSetIntegrityPanel({ evaluationSets, onNotice }: Props) {
  const sealedSets = evaluationSets.filter(item => item.status === 'SEALED')
  const [selectedId, setSelectedId] = useState('')
  const [health, setHealth] = useState<EvaluationShardHealth | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(0)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(0)

  // Derived rather than synchronised through an effect: the selection is only
  // ever "the chosen set if it still exists, otherwise the first one".
  const activeId = selectedId && sealedSets.some(item => item.id === selectedId)
    ? selectedId
    : sealedSets[0]?.id ?? ''

  const load = useCallback(async (evaluationSetId: string) => {
    if (!evaluationSetId) { setHealth(null); return }
    setLoading(true); setError('')
    try {
      setHealth(await semanticAPI.evaluationShards(evaluationSetId))
    } catch (cause) {
      setHealth(null)
      setError(cause instanceof Error ? cause.message : '密封集分片状态读取失败')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load(activeId) }, 0)
    return () => window.clearTimeout(timer)
  }, [load, activeId])

  const expose = async (shardId: number) => {
    setBusy(shardId)
    try {
      const result = await semanticAPI.exposeEvaluationShard(activeId, shardId)
      setHealth(result)
      setConfirming(0)
      onNotice(
        'success',
        result.retired
          ? `分片 ${shardId} 已记为曝光并立即退役；剩余可用分片 ${result.availableShardIds.length} 个。`
          : `分片 ${shardId} 此前已退役，本次声明未改变状态。`,
      )
    } catch (cause) {
      onNotice('error', cause instanceof Error ? cause.message : '分片曝光声明失败')
    } finally { setBusy(0) }
  }

  return <section className="semantic-sealed-integrity">
    <header>
      <div>
        <span>密封集完整性</span>
        <h3>分片轮换与曝光退役</h3>
        <p>只展示计数与轮换状态，任何界面都不显示密封题面。曝光声明按分片生效且不可撤销。</p>
      </div>
      {sealedSets.length > 1 && <label>
        <span className="sr-only">选择密封评测集</span>
        <select aria-label="选择密封评测集" value={activeId} onChange={event => setSelectedId(event.target.value)}>
          {sealedSets.map(item => <option value={item.id} key={item.id}>{item.name} · V{item.versionNo}</option>)}
        </select>
      </label>}
    </header>

    {sealedSets.length === 0 && <div className="semantic-ops-state">
      <LockKey size={26} />
      <strong>当前业务域没有已密封的评测集</strong>
      <small>评测集完成双人独立复核并密封后，其分片轮换状态会出现在这里</small>
    </div>}

    {sealedSets.length > 0 && loading && <div className="semantic-ops-state"><LockKey size={24} /><strong>正在读取分片轮换状态</strong></div>}

    {sealedSets.length > 0 && !loading && error && <div className="semantic-ops-state is-error">
      <WarningCircle size={24} /><strong>{error}</strong>
      <AppButton onClick={() => void load(activeId)}>重新加载</AppButton>
    </div>}

    {health && !loading && !error && <>
      <div className={`semantic-sealed-verdict ${health.canIssue95Percent ? 'is-ready' : 'is-blocked'}`}>
        {health.canIssue95Percent ? <ShieldCheck size={20} weight="fill" /> : <ShieldWarning size={20} weight="fill" />}
        <span>
          <strong>{health.canIssue95Percent ? '四个分片均可用，支持 95% 结论' : '不足四个可用分片，无法签发 95% 结论'}</strong>
          <small>
            95% 结论要求四个分片全部在册；常规发布按轮换只消耗一个分片。
            当前可用分片：{health.availableShardIds.length ? health.availableShardIds.join('、') : '无'}。
          </small>
        </span>
      </div>

      <ul className="semantic-sealed-shards">
        {health.shards.map(shard => {
          const retired = Boolean(shard.retiredAt)
          const empty = shard.caseCount === 0
          return <li className={retired ? 'is-retired' : empty ? 'is-empty' : 'is-live'} key={shard.shardId}>
            <div className="semantic-sealed-shard-head">
              <strong>分片 {shard.shardId}</strong>
              <em>{retired ? retireReasonLabels[shard.retireReason ?? ''] ?? '已退役' : empty ? '无用例' : '可用'}</em>
            </div>
            <dl>
              <div><dt>用例数</dt><dd>{shard.caseCount}</dd></div>
              <div><dt>已运行</dt><dd>{shard.usageCount} 次</dd></div>
              <div><dt>曝光时间</dt><dd>{formatTime(shard.exposedAt)}</dd></div>
              <div><dt>退役时间</dt><dd>{formatTime(shard.retiredAt)}</dd></div>
            </dl>
            {!retired && !empty && confirming !== shard.shardId && <AppButton
              disabled={busy !== 0}
              onClick={() => setConfirming(shard.shardId)}
            >
              <Eye size={15} />
              声明该分片已曝光
            </AppButton>}
            {!retired && !empty && confirming === shard.shardId && <div className="semantic-sealed-confirm">
              <p>
                确认分片 {shard.shardId} 的密封内容已被人看到？该分片将立即永久退役，
                不再参与任何门禁运行，且此操作不可撤销。
              </p>
              <div>
                <AppButton disabled={busy !== 0} onClick={() => setConfirming(0)}>取消</AppButton>
                <AppButton variant="danger" disabled={busy !== 0} onClick={() => void expose(shard.shardId)}>
                  {busy === shard.shardId ? '正在记账…' : '确认曝光并退役'}
                </AppButton>
              </div>
            </div>}
          </li>
        })}
      </ul>
    </>}
  </section>
}

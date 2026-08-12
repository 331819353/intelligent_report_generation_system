import { useEffect, useState } from 'react'
import { CheckCircle, Info, ShieldCheck, Sparkle, SpinnerGap, WarningCircle } from '@phosphor-icons/react'
import { RequestError } from '../../lib/api.ts'
import {
  analysisMethodLabels, reportInsightAPI,
  type AnalysisMethod, type ArtifactRecord, type EvidenceRecord,
} from '../api/insight.ts'
import type { ReportComponent } from '../render/schema.ts'

/**
 * 结论证据面板。
 *
 * 「智能报告」的差异化在于结论可核验：数值来自一次真实执行，事实带有回指到具体
 * 单元格的引用，叙述必须通过校验器才能成为制品。此前这整条链只有服务端实现，
 * 界面上完全无法触达，于是发布前的「事实与结论核验」门禁始终空转。
 */

/** 组件绑定决定哪些分析方法有意义——没有维度就没有 Top N 或占比。 */
function availableMethods(component: ReportComponent): AnalysisMethod[] {
  const dimensions = component.dataBinding?.dimensions?.length ?? 0
  const measures = component.dataBinding?.measures?.length ?? 0
  if (measures === 0) return []
  if (dimensions === 0) return ['CURRENT_VALUE', 'PERIOD_COMPARISON']
  return ['TOP_N', 'CONTRIBUTION', 'SHARE_OF_TOTAL', 'TREND', 'ANOMALY_POINT', 'DATA_COMPLETENESS']
}

function formatTime(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date)
}

export function EvidencePanel({ reportId, component, canEdit }: {
  reportId: string
  component: ReportComponent
  canEdit: boolean
}) {
  const methods = availableMethods(component)
  const [method, setMethod] = useState<AnalysisMethod>(methods[0] ?? 'CURRENT_VALUE')
  const [evidence, setEvidence] = useState<EvidenceRecord | null>(null)
  const [artifact, setArtifact] = useState<ArtifactRecord | null>(null)
  const [busy, setBusy] = useState<'' | 'evidence' | 'conclusion'>('')
  const [error, setError] = useState('')
  const [rejected, setRejected] = useState<string[]>([])

  // 面板由调用方按组件 ID 加 key 挂载，切换组件即重新挂载，因此这里只需拉取
  // 当前结论；状态更新发生在异步回调中，不在 effect 体内同步触发。
  useEffect(() => {
    let cancelled = false
    void reportInsightAPI.getCurrent(reportId, component.id)
      .then(record => { if (!cancelled) setArtifact(record) })
      // 尚未生成结论是正常状态，不作为错误呈现。
      .catch(() => { if (!cancelled) setArtifact(null) })
    return () => { cancelled = true }
  }, [component.id, reportId])

  const derive = async () => {
    setBusy('evidence'); setError(''); setRejected([])
    try {
      setEvidence(await reportInsightAPI.deriveEvidence(reportId, component.id, { analysisMethod: method, topN: 5 }))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '证据生成失败')
    } finally { setBusy('') }
  }

  const generate = async () => {
    setBusy('conclusion'); setError(''); setRejected([])
    try {
      const result = await reportInsightAPI.generate(reportId, component.id, { analysisMethod: method, topN: 5 })
      setEvidence(result.evidence)
      setArtifact(result.artifact)
    } catch (cause) {
      // 未通过事实校验是正常结果而非故障：把校验器指出的具体问题原样呈现，
      // 不把它包装成一句笼统的失败。
      const detail = cause instanceof RequestError ? cause.detail as { verification?: { failures?: Array<{ reason: string; expected: string[] }> } } : undefined
      const failures = detail?.verification?.failures ?? []
      if (failures.length > 0) {
        setRejected(failures.map(item => `${item.reason}：需要 ${item.expected.join('、') || '可核验依据'}`))
        setError('生成的结论未通过事实校验，未予保存')
      } else {
        setError(cause instanceof Error ? cause.message : '结论生成失败')
      }
    } finally { setBusy('') }
  }

  if (methods.length === 0) {
    return <section className="report-evidence-panel" aria-label="结论证据">
      <header><strong>结论证据</strong></header>
      <p className="report-evidence-note"><Info size={15} />该组件没有绑定度量，无法生成可核验证据。</p>
    </section>
  }

  return <section className="report-evidence-panel" aria-label="结论证据">
    <header>
      <strong>结论证据</strong>
      <small>数值与事实由服务端按你的权限执行后推导，不接受前端提供的数字</small>
    </header>

    {/* 生成式结论必须通过共享的事实校验器，而校验器要求叙述绑定到一个语义发布
        版本。数据集字段绑定没有语义发布版本，因此这里只提供可核验的事实，不提供
        自动撰写的结论——把二者混为一谈会让「已核验」失去意义。 */}
    <p className="report-evidence-note">
      <Info size={15} />
      当前为数据集字段绑定：可生成可追溯到单元格的事实证据；自动撰写并通过事实校验的结论
      需要绑定语义指标（可从问数结果「加入报告」获得）。
    </p>

    {artifact && <div className={`report-evidence-artifact is-${artifact.artifact.status.toLocaleLowerCase()}`}>
      <span className="report-evidence-status">
        {artifact.artifact.status === 'CURRENT'
          ? <><CheckCircle size={14} weight="fill" />结论与当前证据一致</>
          : artifact.artifact.status === 'STALE'
            ? <><WarningCircle size={14} weight="fill" />数据已变化，结论待重新核验</>
            : <><WarningCircle size={14} weight="fill" />结论生成失败</>}
        {artifact.artifact.humanEdited && <em>已人工改写</em>}
      </span>
      {artifact.artifact.content.summary && <p>{artifact.artifact.content.summary}</p>}
      {artifact.artifact.citations.length > 0 && <small>
        {artifact.artifact.citations.length} 处引用回指到结果单元格
      </small>}
    </div>}

    <div className="report-evidence-form">
      <label>分析方法
        <select value={method} disabled={Boolean(busy) || !canEdit} onChange={event => setMethod(event.target.value as AnalysisMethod)}>
          {methods.map(item => <option key={item} value={item}>{analysisMethodLabels[item]}</option>)}
        </select>
      </label>
      <button className="quiet-button" type="button" disabled={Boolean(busy) || !canEdit} onClick={() => void derive()}>
        {busy === 'evidence' ? <SpinnerGap className="is-spinning" size={15} /> : <ShieldCheck size={15} />}
        {busy === 'evidence' ? '正在执行并推导…' : '生成证据'}
      </button>
      <button className="primary-button" type="button" disabled={Boolean(busy) || !canEdit} onClick={() => void generate()}>
        {busy === 'conclusion' ? <SpinnerGap className="is-spinning" size={15} /> : <Sparkle size={15} />}
        {busy === 'conclusion' ? '正在撰写并核验…' : '生成结论'}
      </button>
    </div>

    {error && <p className="report-evidence-note is-error"><WarningCircle size={15} />{error}</p>}
    {rejected.length > 0 && <ul className="report-evidence-warnings">
      {rejected.map((item, index) => <li key={index}><WarningCircle size={13} />{item}</li>)}
    </ul>}

    {evidence && <div className="report-evidence-result">
      <p className="report-evidence-meta">
        {analysisMethodLabels[evidence.evidence.analysisMethod] ?? evidence.evidence.analysisMethod}
        <span>· 数据截至 {formatTime(evidence.evidence.asOf)}</span>
        <span>· {evidence.evidence.facts.length} 条事实</span>
      </p>
      <ul className="report-evidence-facts">
        {evidence.evidence.facts.slice(0, 6).map(fact => <li key={fact.id}>
          <strong>{fact.currentValue}<small>{fact.unit}</small></strong>
          {fact.changeRate && <em>变化 {fact.changeRate}</em>}
          {/* 引用回指到真实单元格，是这条事实可被核验的依据。 */}
          <span>{fact.cellRefs.length} 个来源单元格</span>
        </li>)}
      </ul>
      {evidence.evidence.qualityWarnings.length > 0 && <ul className="report-evidence-warnings">
        {evidence.evidence.qualityWarnings.map(warning => <li key={warning.code}>
          <WarningCircle size={13} />{warning.message}
        </li>)}
      </ul>}
    </div>}
  </section>
}

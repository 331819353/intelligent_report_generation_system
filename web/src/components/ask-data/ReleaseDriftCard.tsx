import {
  ArrowRight,
  ArrowsClockwise,
  ChartBar,
  CurrencyCny,
  LockSimple,
  WarningCircle,
} from '@phosphor-icons/react'
import type { ReleaseChange, ReleaseDrift } from '../../lib/ask-data-api'

type ReleaseDriftCardProps = {
  drift: ReleaseDrift
  busy?: boolean
  error?: string
  expiredAt?: string
  onConfirm: () => void
  onHistory: () => void
}

function objectIcon(change: ReleaseChange) {
  return change.objectType === 'METRIC'
    ? <CurrencyCny size={17} weight="bold" aria-hidden="true" />
    : <ChartBar size={17} weight="bold" aria-hidden="true" />
}

function version(value: string | undefined) {
  return value || '—'
}

export function ReleaseDriftCard({
  drift,
  busy = false,
  error,
  expiredAt = '2026-08-08 10:30',
  onConfirm,
  onHistory,
}: ReleaseDriftCardProps) {
  const changes = drift.changes.slice(0, 4)

  return (
    <section className="ask-release-drift-card" aria-labelledby="ask-release-drift-title">
      <div className="ask-release-expired-notice" role="alert">
        <WarningCircle size={15} weight="fill" aria-hidden="true" />
        <strong>本次澄清已于 {expiredAt} 过期，无法继续原选择</strong>
      </div>

      <div className="ask-release-drift-content">
        <header className="ask-release-drift-title">
          <span><ArrowsClockwise size={20} weight="bold" aria-hidden="true" /></span>
          <div>
            <h2 id="ask-release-drift-title">口径已更新</h2>
            <p>您上次选择的口径已被新版本取代，请确认是否切换到最新生效口径</p>
          </div>
        </header>

        <div className="ask-release-version-grid">
          <div className="ask-release-version-heading">
            <strong>原口径 · Release {drift.previous.semanticVersion}</strong>
            <span>已被取代</span>
          </div>
          <div className="ask-release-version-heading is-active">
            <strong>当前口径 · Release {drift.active.semanticVersion}</strong>
            <span>当前生效</span>
          </div>

          {changes.map(change => <div className="ask-release-change-row" key={`${change.objectType}:${change.objectId}`}>
            <div className="ask-release-change-object is-previous">
              <span>{objectIcon(change)}</span>
              <div><strong>{change.name}</strong><small>{version(change.fromVersion)}</small></div>
            </div>
            <div className="ask-release-change-object is-current">
              <ArrowRight className="ask-release-change-arrow" size={18} weight="bold" aria-hidden="true" />
              <span>{objectIcon(change)}</span>
              <div><strong>{change.name}</strong><small>{version(change.toVersion)}</small></div>
              <em>{change.summary}</em>
            </div>
          </div>)}
        </div>

        {changes.length === 0 && <p className="ask-release-no-object-change">发布版本已更新，未检测到指标或维度对象差异。</p>}

        <div className="ask-release-drift-actions">
          <button className="primary-button" type="button" disabled={busy} onClick={onConfirm}>
            {busy ? '正在切换…' : '切换新口径并重新分析'}
          </button>
          <button type="button" disabled={busy} onClick={onHistory}>仅查看历史结果</button>
        </div>
        {error && <p className="ask-release-action-error" role="alert">{error}</p>}
        <p className="ask-release-lock-note"><LockSimple size={13} weight="fill" aria-hidden="true" />不会静默切换，重新分析将从绑定阶段开始</p>
      </div>
    </section>
  )
}

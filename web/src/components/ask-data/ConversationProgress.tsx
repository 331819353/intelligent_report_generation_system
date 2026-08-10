import {
  CheckCircle,
  Circle,
  SpinnerGap,
  WarningCircle,
  WifiHigh,
  WifiSlash,
  XCircle,
} from '@phosphor-icons/react'
import type { AskDataQuestionPhase } from '../../hooks/use-ask-data-question'
import type { QuestionRunEvent, QuestionRunState } from '../../lib/ask-data-api'
import { buildConversationProgress } from './conversation-progress'

type ConversationProgressProps = {
  phase: AskDataQuestionPhase | 'PREVIEW'
  currentState: QuestionRunState
  events: QuestionRunEvent[]
  onCancel?: () => void
}

const activePhases = new Set<ConversationProgressProps['phase']>([
  'CREATING',
  'CONNECTING',
  'STREAMING',
  'RECONNECTING',
  'PREVIEW',
])

function connectionLabel(phase: ConversationProgressProps['phase']) {
  if (phase === 'RECONNECTING') return '正在恢复事件流'
  if (phase === 'CREATING') return '正在创建运行'
  if (phase === 'CONNECTING') return '正在连接事件流'
  if (phase === 'CANCELED') return '运行已取消'
  if (phase === 'ERROR') return '事件流不可用'
  if (phase === 'TERMINAL') return '运行记录已封存'
  return '事件流已连接'
}

function ProgressIcon({ status }: { status: 'complete' | 'active' | 'pending' | 'blocked' }) {
  if (status === 'complete') return <CheckCircle size={17} weight="fill" aria-hidden="true" />
  if (status === 'active') return <SpinnerGap className="ask-progress-spinner" size={17} weight="bold" aria-hidden="true" />
  if (status === 'blocked') return <WarningCircle size={17} weight="fill" aria-hidden="true" />
  return <Circle size={17} weight="bold" aria-hidden="true" />
}

/**
 * 只把 Question API 的公开状态投影为受控文案。stage/code/hash 不直接展示，
 * 避免把内部推理、SQL 或原始工具内容泄漏到进度界面。
 */
export function ConversationProgress({ phase, currentState, events, onCancel }: ConversationProgressProps) {
  const items = buildConversationProgress(currentState, events)
  const running = activePhases.has(phase) && currentState !== 'ANSWERED' && currentState !== 'BLOCKED' && currentState !== 'CLARIFICATION_REQUIRED'
  const connectionClass = phase === 'RECONNECTING' || phase === 'ERROR' ? 'is-warning' : phase === 'CANCELED' ? 'is-muted' : ''

  return (
    <section className="ask-progress-panel" aria-labelledby="ask-progress-title">
      <header>
        <div className="ask-progress-heading">
          <strong id="ask-progress-title">{running ? '正在运行分析' : '本轮分析进度'}</strong>
          <span className={`ask-stream-status ${connectionClass}`.trim()} role="status" aria-live="polite">
            {phase === 'RECONNECTING' || phase === 'ERROR'
              ? <WifiSlash size={13} aria-hidden="true" />
              : phase === 'CANCELED'
                ? <XCircle size={13} aria-hidden="true" />
                : <WifiHigh size={13} aria-hidden="true" />}
            {connectionLabel(phase)}
          </span>
        </div>
        {running && onCancel && <button className="ask-cancel-run" type="button" onClick={onCancel}>取消本次运行</button>}
      </header>

      <ol className="ask-progress-timeline" aria-label="分析步骤">
        {items.map(item => <li className={`ask-progress-item is-${item.status}`} key={item.key}>
          <span className="ask-progress-node"><ProgressIcon status={item.status} /></span>
          <span className="ask-progress-copy">
            <span className="sr-only">{item.status === 'complete' ? '已完成：' : item.status === 'active' ? '进行中：' : item.status === 'blocked' ? '已阻断：' : '待开始：'}</span>
            <strong>{item.label}</strong>
            <small>{item.detail}</small>
          </span>
          {item.timestamp
            ? <time dateTime={item.timestamp}>{item.timestamp}</time>
            : <span className="ask-progress-waiting">{item.status === 'active' ? '进行中' : item.status === 'blocked' ? '需处理' : '待开始'}</span>}
        </li>)}
      </ol>
    </section>
  )
}

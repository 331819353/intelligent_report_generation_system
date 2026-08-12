import { Component, type ErrorInfo, type ReactNode, useEffect, useRef, useState } from 'react'
import {
  CheckCircle, Clock, Database, EyeSlash, SpinnerGap, Warning, WarningCircle,
} from '@phosphor-icons/react'
import { componentPresentation, type ReportComponentState } from './state'

type BoundaryProps = { children: ReactNode; fallback?: ReactNode }
type BoundaryState = { failed: boolean }

export class ReportComponentBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { failed: false }

  static getDerivedStateFromError(): BoundaryState { return { failed: true } }

  componentDidCatch(error: Error, info: ErrorInfo) {
    window.dispatchEvent(new CustomEvent('report-component-error', { detail: { message: error.message, stack: info.componentStack } }))
  }

  render() {
    if (this.state.failed) return this.props.fallback ?? <ComponentStateView state="ERROR" />
    return this.props.children
  }
}

export class ReportBlockBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { failed: false }

  static getDerivedStateFromError(): BoundaryState { return { failed: true } }

  componentDidCatch(error: Error, info: ErrorInfo) {
    window.dispatchEvent(new CustomEvent('report-block-error', { detail: { message: error.message, stack: info.componentStack } }))
  }

  render() {
    if (this.state.failed) return this.props.fallback ?? <ComponentStateView state="ERROR" />
    return this.props.children
  }
}

const stateIcons = {
  LOADING: SpinnerGap,
  READY: CheckCircle,
  EMPTY: Database,
  PARTIAL: WarningCircle,
  ERROR: Warning,
  NO_PERMISSION: EyeSlash,
  STALE: Clock,
  TIMEOUT: Clock,
} as const

export function ComponentStateView({ state, boundTitle, errorCode, onAction, compact = false }: {
  state: ReportComponentState | string
  boundTitle?: string
  /** 失败原因码，用于把「加载失败」替换为可执行的具体说明。 */
  errorCode?: string
  onAction?: () => void
  compact?: boolean
}) {
  const view = componentPresentation(state, boundTitle, errorCode)
  const Icon = stateIcons[view.state]
  return <section className={`report-component-state is-${view.tone} ${compact ? 'is-compact' : ''}`.trim()} aria-label={`${view.label}：${view.message}`}>
    <span className="report-state-badge">{view.label}</span>
    <Icon className={view.state === 'LOADING' ? 'is-spinning' : ''} size={compact ? 22 : 28} weight={view.state === 'READY' ? 'fill' : 'regular'} aria-hidden="true" />
    <strong>{view.title}</strong>
    <p>{view.message}</p>
    {view.action && <button type="button" onClick={onAction}>{view.action}</button>}
  </section>
}

export function LazyRuntimeBlock({ children, fallbackState = 'LOADING' }: { children: ReactNode; fallbackState?: ReportComponentState }) {
  const ref = useRef<HTMLDivElement>(null)
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    if (!ref.current) return undefined
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) {
        setVisible(true)
        observer.disconnect()
      }
    }, { rootMargin: '200px' })
    observer.observe(ref.current)
    return () => observer.disconnect()
  }, [])
  return <div ref={ref}>{visible ? children : <ComponentStateView state={fallbackState} compact />}</div>
}

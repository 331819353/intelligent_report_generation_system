import { Component, type ErrorInfo, type ReactNode } from 'react'

type ReportErrorBoundaryProps = {
  componentName: string
  resetKey?: unknown
  children: ReactNode
}

type ReportErrorBoundaryState = {
  failed: boolean
  resetKey?: unknown
}

/** 将单个组件异常限制在自身网格内，避免整份报告白屏。 */
export class ReportErrorBoundary extends Component<ReportErrorBoundaryProps, ReportErrorBoundaryState> {
  state: ReportErrorBoundaryState = { failed: false, resetKey: this.props.resetKey }

  static getDerivedStateFromError(): Partial<ReportErrorBoundaryState> {
    return { failed: true }
  }

  static getDerivedStateFromProps(props: ReportErrorBoundaryProps, state: ReportErrorBoundaryState): Partial<ReportErrorBoundaryState> | null {
    // 新一轮运行状态到达后允许组件重试渲染，避免一次失败永久锁死错误边界。
    return props.resetKey !== state.resetKey ? { failed: false, resetKey: props.resetKey } : null
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 这里只记录不含组件数据的诊断信息；后续观测服务可接管该接口。
    console.error(`报告组件渲染失败：${this.props.componentName}`, error.message, info.componentStack)
  }

  render() {
    if (this.state.failed) {
      return (
        <div className="report-component-state report-component-state--error" role="alert">
          <strong>组件暂时无法显示</strong>
          <span>{this.props.componentName} 已被隔离，其他内容不受影响。</span>
        </div>
      )
    }
    return this.props.children
  }
}

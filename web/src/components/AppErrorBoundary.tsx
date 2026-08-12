import { Component, type ErrorInfo, type ReactNode } from 'react'
import { ArrowClockwise, House, WarningCircle } from '@phosphor-icons/react'

type AppErrorBoundaryProps = {
  children: ReactNode
  resetKey: string
}

type AppErrorBoundaryState = {
  failed: boolean
}

/**
 * 最后一层页面故障隔离。业务组件仍应处理预期中的接口失败；这里只拦截
 * 未预期的渲染异常，确保一个页面不能把整个登录会话变成空白页。
 */
export class AppErrorBoundary extends Component<AppErrorBoundaryProps, AppErrorBoundaryState> {
  state: AppErrorBoundaryState = { failed: false }

  static getDerivedStateFromError(): AppErrorBoundaryState {
    return { failed: true }
  }

  componentDidUpdate(previous: AppErrorBoundaryProps) {
    if (this.state.failed && previous.resetKey !== this.props.resetKey) {
      this.setState({ failed: false })
    }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('APP_PAGE_RENDER_FAILED', error, info.componentStack)
  }

  render() {
    if (!this.state.failed) return this.props.children

    return <main className="app-failure-page" role="alert">
      <img src="/haier-logo.svg" alt="Haier 海尔" />
      <section>
        <span className="app-failure-icon"><WarningCircle size={34} weight="duotone" /></span>
        <h1>当前页面暂时无法显示</h1>
        <p>登录状态和已保存内容仍然保留。可以重新加载当前页面，或先返回分析首页继续工作。</p>
        <div>
          <button className="app-button is-primary" type="button" onClick={() => window.location.reload()}>
            <ArrowClockwise size={17} />重新加载
          </button>
          <button className="app-button is-plain" type="button" onClick={() => window.location.assign('/home')}>
            <House size={17} />返回首页
          </button>
        </div>
      </section>
    </main>
  }
}

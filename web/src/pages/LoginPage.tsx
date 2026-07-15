import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { login } from '../lib/auth'

/** 收集租户凭据、处理登录状态并进入工作台。 */
export function LoginPage() {
  const navigate = useNavigate()
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  /** 校验必填字段并提交登录请求，期间阻止重复提交。 */
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const tenantCode = String(form.get('tenantCode') ?? '')
    const email = String(form.get('email') ?? '')
    const password = String(form.get('password') ?? '')
    if (!tenantCode || !email || !password) {
      setError('请输入租户、账号和密码')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      await login(tenantCode, email, password)
      navigate('/admin')
    } catch {
      setError('租户、账号或密码错误')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="login-page">
      <section className="login-story">
        <span className="eyebrow light">INTELLIGENT REPORTING</span>
        <h1>让数据，形成可以行动的结论。</h1>
        <p>连接数据资产，组织指标口径，通过可视化画板与 AI 快速完成正式报告。</p>
        <div className="story-metric"><strong>12 × 10</strong><span>首屏主网格 · 纵向自由延展</span></div>
      </section>
      <section className="login-panel">
        <form onSubmit={submit}>
          <div className="brand-mark large">IR</div>
          <span className="eyebrow">欢迎回来</span>
          <h2>登录智能报告平台</h2>
          <label>租户<input name="tenantCode" defaultValue="demo" placeholder="请输入租户编码" /></label>
          <label>账号<input name="email" type="email" placeholder="name@company.com" /></label>
          <label>密码<input name="password" type="password" placeholder="请输入密码" /></label>
          {error && <p className="form-error" role="alert">{error}</p>}
          <button className="primary-button" type="submit" disabled={submitting}>{submitting ? '正在登录…' : '进入工作台'}</button>
          <p className="form-hint">登录即代表你同意遵守所属租户的数据安全策略。</p>
        </form>
      </section>
    </main>
  )
}

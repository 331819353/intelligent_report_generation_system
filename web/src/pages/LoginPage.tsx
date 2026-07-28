import { CheckCircle, FlowArrow, Sparkle } from '@phosphor-icons/react'
import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { login } from '../lib/auth'
import { RequestError } from '../lib/api'

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
    } catch (cause) {
      setError(cause instanceof RequestError && cause.detail.code === 'NO_ACTIVE_BUSINESS_DOMAIN'
        ? '当前账号没有可用的所属领域，请联系管理员分配或启用领域'
        : '租户、账号或密码错误')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="login-page">
      <section className="login-story">
        <div className="login-brand"><img className="brand-logo" src="/haier-logo.svg" alt="Haier 海尔" /><div><strong>智能分析决策平台</strong><span>Decision Intelligence</span></div></div>
        <div className="login-story-copy">
          <span className="eyebrow">DECISION INTELLIGENCE</span>
          <h1>把数据资产，转化为可信的分析结论。</h1>
          <p>从数据接入、语义治理到报告交付，在一个清晰、可追溯的分析工作空间内完成。</p>
          <ul className="login-benefits">
            <li><CheckCircle aria-hidden="true" weight="fill" /><span><strong>统一资产语义</strong><small>维度、指标与数据表关系清晰可查</small></span></li>
            <li><FlowArrow aria-hidden="true" weight="fill" /><span><strong>自动化数据流程</strong><small>由 LLM 设计，开发引擎稳定执行 DAG</small></span></li>
            <li><Sparkle aria-hidden="true" weight="fill" /><span><strong>智能分析问答</strong><small>基于可信链路检索并生成可验证结论</small></span></li>
          </ul>
        </div>
        <p className="login-story-footer">DATA · SEMANTICS · INSIGHT</p>
      </section>
      <section className="login-panel">
        <form onSubmit={submit}>
          <span className="eyebrow">欢迎回来</span>
          <h2>登录智能分析决策平台</h2>
          <p className="login-form-intro">使用组织账号进入你的数据分析工作空间。</p>
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

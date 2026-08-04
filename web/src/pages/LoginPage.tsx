import { Database, ShieldCheck, Stack } from '@phosphor-icons/react'
import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { login } from '../lib/auth'
import { administrationAPI } from '../lib/administration'

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
      const domains = await administrationAPI.listDomains()
      navigate(domains.length > 0 ? '/data-sources' : '/domain-access')
    } catch {
      setError('租户、账号或密码错误')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="login-page">
      <section className="login-story">
        <div className="login-brand"><img className="brand-logo" src="/haier-logo.svg" alt="Haier 海尔" /><div><strong>数据配置管理平台</strong><span>Data Administration</span></div></div>
        <div className="login-story-copy">
          <span className="eyebrow">DATA ADMINISTRATION</span>
          <h1>统一管理权限、数据源与数据集。</h1>
          <p>在一个清晰、可追溯的配置空间内完成访问控制、数据接入和数据集设计。</p>
          <ul className="login-benefits">
            <li><ShieldCheck aria-hidden="true" weight="fill" /><span><strong>权限设定</strong><small>按领域、成员与角色控制访问范围</small></span></li>
            <li><Database aria-hidden="true" weight="fill" /><span><strong>数据源配置</strong><small>集中维护数据库与文件数据连接</small></span></li>
            <li><Stack aria-hidden="true" weight="fill" /><span><strong>数据集配置</strong><small>设计、校验并发布可复用数据集</small></span></li>
          </ul>
        </div>
        <p className="login-story-footer">ACCESS · SOURCE · DATASET</p>
      </section>
      <section className="login-panel">
        <form onSubmit={submit}>
          <span className="eyebrow">欢迎回来</span>
          <h2>登录数据配置管理平台</h2>
          <p className="login-form-intro">使用组织账号进入你的数据配置空间。</p>
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

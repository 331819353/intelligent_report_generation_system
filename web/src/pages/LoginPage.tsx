import { Database, ShieldCheck, Stack } from '@phosphor-icons/react'
import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { RequestError } from '../lib/api'
import { login, register } from '../lib/auth'
import { administrationAPI } from '../lib/administration'

/** 收集租户凭据、处理登录状态并进入工作台。 */
export function LoginPage() {
  const navigate = useNavigate()
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  /** 校验必填字段并提交登录请求，期间阻止重复提交。 */
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const tenantCode = String(form.get('tenantCode') ?? '')
    const displayName = String(form.get('displayName') ?? '').trim()
    const email = String(form.get('email') ?? '').trim()
    const password = String(form.get('password') ?? '')
    const confirmPassword = String(form.get('confirmPassword') ?? '')
    if (!tenantCode || !email || !password || (mode === 'register' && !displayName)) {
      setError(mode === 'register' ? '请完整填写注册信息' : '请输入租户、账号和密码')
      return
    }
    if (mode === 'register' && password !== confirmPassword) {
      setError('两次输入的密码不一致')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      if (mode === 'register') await register(tenantCode, displayName, email, password)
      else await login(tenantCode, email, password)
      const domains = await administrationAPI.listDomains()
      navigate(domains.length > 0 ? '/data-sources' : '/domain-access')
    } catch (cause) {
      setError(cause instanceof RequestError
        ? cause.detail.message
        : mode === 'register' ? '注册失败，请稍后重试' : '租户、账号或密码错误')
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
            <li><ShieldCheck aria-hidden="true" weight="fill" /><span><strong>权限设定</strong><small>按平台、领域与用户管理固定权限</small></span></li>
            <li><Database aria-hidden="true" weight="fill" /><span><strong>数据源配置</strong><small>集中维护数据库与文件数据连接</small></span></li>
            <li><Stack aria-hidden="true" weight="fill" /><span><strong>数据集配置</strong><small>设计、校验并发布可复用数据集</small></span></li>
          </ul>
        </div>
        <p className="login-story-footer">ACCESS · SOURCE · DATASET</p>
      </section>
      <section className="login-panel">
        <form onSubmit={submit}>
          <div className="login-mode-switch" role="tablist" aria-label="账号操作">
            <button type="button" role="tab" aria-selected={mode === 'login'} className={mode === 'login' ? 'active' : ''} onClick={() => { setMode('login'); setError('') }}>登录</button>
            <button type="button" role="tab" aria-selected={mode === 'register'} className={mode === 'register' ? 'active' : ''} onClick={() => { setMode('register'); setError('') }}>注册</button>
          </div>
          <span className="eyebrow">{mode === 'register' ? '创建账号' : '欢迎回来'}</span>
          <h2>{mode === 'register' ? '注册数据配置账号' : '登录数据配置管理平台'}</h2>
          <p className="login-form-intro">{mode === 'register' ? '新账号自动加入默认领域并获得固定用户权限。' : '使用组织账号进入你的数据配置空间。'}</p>
          <label>租户<input name="tenantCode" defaultValue="demo" placeholder="请输入租户编码" /></label>
          {mode === 'register' && <label>姓名<input name="displayName" autoComplete="name" placeholder="请输入姓名" /></label>}
          <label>账号<input name="email" type="email" placeholder="name@company.com" /></label>
          <label>密码<input name="password" type="password" autoComplete={mode === 'register' ? 'new-password' : 'current-password'} placeholder={mode === 'register' ? '至少 10 位，包含大小写字母和数字' : '请输入密码'} /></label>
          {mode === 'register' && <label>确认密码<input name="confirmPassword" type="password" autoComplete="new-password" placeholder="请再次输入密码" /></label>}
          {error && <p className="form-error" role="alert">{error}</p>}
          <button className="primary-button" type="submit" disabled={submitting}>{submitting ? mode === 'register' ? '正在注册…' : '正在登录…' : mode === 'register' ? '注册并进入' : '进入工作台'}</button>
          <p className="form-hint">{mode === 'register' ? '注册后平台管理员可调整你的领域归属。' : '登录即代表你同意遵守所属租户的数据安全策略。'}</p>
        </form>
      </section>
    </main>
  )
}

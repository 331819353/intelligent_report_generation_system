import { Eye, EyeSlash, LockKey, ShieldCheck, User, UserFocus } from '@phosphor-icons/react'
import { FormEvent, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { AppButton } from '../components/AppButton'
import { apiRequest, RequestError } from '../lib/api'
import { login, register } from '../lib/auth'
import { administrationAPI } from '../lib/administration'

/** 收集平台账号凭据、处理登录状态并进入工作台。 */
export function LoginPage() {
  const location = useLocation()
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [error, setError] = useState('')
  const [supportNote, setSupportNote] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [passwordVisible, setPasswordVisible] = useState(false)
  const today = new Date()
  const qaViewport1920 = import.meta.env.DEV && new URLSearchParams(location.search).get('qa') === '1920'
  const dateTime = [today.getFullYear(), String(today.getMonth() + 1).padStart(2, '0'), String(today.getDate()).padStart(2, '0')].join('-')
  const displayDate = new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', weekday: 'long',
  }).format(today)

  function switchMode(nextMode: 'login' | 'register') {
    setMode(nextMode)
    setError('')
    setSupportNote('')
    setPasswordVisible(false)
  }

  /** 校验必填字段并提交登录请求，期间阻止重复提交。 */
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const employeeNo = String(form.get('employeeNo') ?? '').trim()
    const displayName = String(form.get('displayName') ?? '').trim()
    const account = String(form.get(mode === 'register' ? 'email' : 'account') ?? '').trim()
    const email = mode === 'register' ? account : ''
    const password = String(form.get('password') ?? '')
    const confirmPassword = String(form.get('confirmPassword') ?? '')
    if (!account || !password || (mode === 'register' && (!employeeNo || !displayName))) {
      setError(mode === 'register' ? '请完整填写注册信息' : '请输入账号和密码')
      return
    }
    if (mode === 'register' && password !== confirmPassword) {
      setError('两次输入的密码不一致')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      if (mode === 'register') await register(employeeNo, displayName, email, password)
      else await login(account, password)
      const [domains, access] = await Promise.all([
        administrationAPI.listDomains(),
        apiRequest<{ platformAdministrator: boolean }>('/v1/platform-management/access', { businessDomain: false, cache: 'no-store' }),
      ])
      const destination = domains.length > 0 ? '/home' : access.platformAdministrator ? '/platform-management/domains' : '/domain-access'
      // Login replaces the public route instead of leaving it in history. A
      // stale mounted login tree must not race the authenticated destination
      // while global session/domain listeners initialize.
      window.location.replace(destination)
    } catch (cause) {
      setError(cause instanceof RequestError
        ? mode === 'login' && cause.detail.code === 'INVALID_CREDENTIALS'
          ? '账号或密码错误'
          : cause.detail.message
        : mode === 'register' ? '注册失败，请稍后重试' : '账号或密码错误')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className={`login-page${qaViewport1920 ? ' qa-preview-1920' : ''}`}>
      <section className="login-story" aria-labelledby="login-story-title">
        <header className="login-brand">
          <img className="brand-logo" src="/haier-logo.svg" alt="Haier 海尔" />
          <div>
            <strong>智能分析决策平台</strong>
            <span>INTELLIGENT ANALYTICS &amp; DECISION PLATFORM</span>
          </div>
        </header>

        <div className="login-story-copy">
          <h1 id="login-story-title">深海洞察 · 智见未来</h1>
          <p>汇聚数据价值，洞察业务本质，驱动智能决策。</p>
        </div>

        <div className="login-landscape" aria-hidden="true">
          <img src="/login-data-landscape.png" alt="" />
          <span className="login-landscape-label label-insight">洞察</span>
          <span className="login-landscape-label label-analysis">分析</span>
          <span className="login-landscape-label label-decision">决策</span>
        </div>

        <footer className="login-story-footer">
          <span><ShieldCheck size={24} weight="regular" aria-hidden="true" />安全连接</span>
          <i aria-hidden="true" />
          <span><LockKey size={23} weight="regular" aria-hidden="true" />权限隔离</span>
          <i aria-hidden="true" />
          <span>© 2026 Haier. All Rights Reserved.</span>
        </footer>
      </section>

      <section className="login-panel">
        <form onSubmit={submit} data-mode={mode}>
          <div className="login-mode-switch" role="tablist" aria-label="账号操作">
            <AppButton text type="button" role="tab" aria-selected={mode === 'login'} className={mode === 'login' ? 'login-mode-tab active' : 'login-mode-tab'} onClick={() => switchMode('login')}>登录</AppButton>
            <AppButton text type="button" role="tab" aria-selected={mode === 'register'} className={mode === 'register' ? 'login-mode-tab active' : 'login-mode-tab'} onClick={() => switchMode('register')}>注册</AppButton>
          </div>

          {mode === 'register' && <p className="login-form-intro">注册只创建平台账号，所属业务领域将在登录后申请或由管理员分配。</p>}

          {mode === 'register' && <label className="login-field">姓名<div className="login-input"><UserFocus size={24} aria-hidden="true" /><input name="displayName" autoComplete="name" placeholder="请输入姓名" /></div></label>}
          {mode === 'register' && <label className="login-field">工号<div className="login-input"><User size={24} aria-hidden="true" /><input name="employeeNo" autoComplete="username" maxLength={32} placeholder="例如：A10248" /></div></label>}
          {mode === 'register'
            ? <label className="login-field">邮箱<div className="login-input"><User size={24} aria-hidden="true" /><input name="email" type="email" autoComplete="email" placeholder="name@company.com" /></div></label>
            : <label className="login-field">工号或邮箱<div className="login-input"><User size={24} aria-hidden="true" /><input name="account" autoComplete="username" placeholder="请输入工号或邮箱" autoFocus /></div></label>}

          <label className="login-field">密码
            <div className="login-input">
              <LockKey size={24} aria-hidden="true" />
              <input name="password" type={passwordVisible ? 'text' : 'password'} autoComplete={mode === 'register' ? 'new-password' : 'current-password'} placeholder={mode === 'register' ? '至少 10 位，包含大小写字母和数字' : '请输入密码'} />
              <AppButton text circle type="button" className="login-password-toggle" aria-label={passwordVisible ? '隐藏密码' : '显示密码'} onClick={() => setPasswordVisible((visible) => !visible)}>{passwordVisible ? <Eye size={24} /> : <EyeSlash size={24} />}</AppButton>
            </div>
          </label>

          {mode === 'register' && <label className="login-field">确认密码<div className="login-input"><LockKey size={24} aria-hidden="true" /><input name="confirmPassword" type={passwordVisible ? 'text' : 'password'} autoComplete="new-password" placeholder="请再次输入密码" /></div></label>}

          {mode === 'login' && <div className="login-assistance"><AppButton link type="button" onClick={() => { setSupportNote('请联系平台管理员完成密码重置。'); setError('') }}>忘记密码？</AppButton></div>}
          {supportNote && <p className="form-notice" role="status">{supportNote}</p>}
          {error && <p className="form-error" role="alert">{error}</p>}

          <AppButton variant="primary" size="large" className="login-submit" type="submit" disabled={submitting}>{submitting ? mode === 'register' ? '正在注册…' : '正在登录…' : mode === 'register' ? '注册并进入' : '进入工作台'}</AppButton>

          <p className="form-hint"><ShieldCheck size={26} weight="regular" aria-hidden="true" />{mode === 'register' ? '注册即代表你同意遵守平台的账号与数据安全策略。' : '登录即代表你同意遵守平台的数据安全策略。'}</p>
        </form>
        <time dateTime={dateTime} className="login-panel-date">{displayDate}</time>
      </section>
    </main>
  )
}

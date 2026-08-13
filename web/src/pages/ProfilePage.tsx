import { CheckCircle, IdentificationCard, Key, ShieldCheck, UserCircle, WarningCircle } from '@phosphor-icons/react'
import { type FormEvent, useEffect, useState } from 'react'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import '../styles/profile.css'
import { changeCurrentPassword, currentProfile, forceLogout, updateCurrentProfile, type CurrentProfile } from '../lib/auth'

type Feedback = { tone: 'success' | 'error'; message: string } | null

export function ProfilePage() {
  const [profile, setProfile] = useState<CurrentProfile | null>(null)
  const [displayName, setDisplayName] = useState('')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [busy, setBusy] = useState('')
  const [feedback, setFeedback] = useState<Feedback>(null)

  useEffect(() => {
    let active = true
    void currentProfile().then(value => {
      if (!active) return
      setProfile(value)
      setDisplayName(value.displayName)
    }).catch(cause => active && setFeedback({ tone: 'error', message: cause instanceof Error ? cause.message : '个人资料加载失败' }))
    return () => { active = false }
  }, [])

  const saveProfile = async (event: FormEvent) => {
    event.preventDefault()
    if (!displayName.trim() || busy) return
    setBusy('profile'); setFeedback(null)
    try {
      await updateCurrentProfile(displayName.trim())
      setProfile(current => current ? { ...current, displayName: displayName.trim() } : current)
      setFeedback({ tone: 'success', message: '个人资料已保存，导航栏将在刷新后使用新姓名' })
    } catch (cause) {
      setFeedback({ tone: 'error', message: cause instanceof Error ? cause.message : '个人资料保存失败' })
    } finally { setBusy('') }
  }

  const changePassword = async (event: FormEvent) => {
    event.preventDefault()
    if (busy) return
    if (newPassword !== confirmPassword) {
      setFeedback({ tone: 'error', message: '两次输入的新密码不一致' })
      return
    }
    setBusy('password'); setFeedback(null)
    try {
      await changeCurrentPassword(currentPassword, newPassword)
      setFeedback({ tone: 'success', message: '密码已修改，所有会话已安全撤销，请重新登录' })
      window.setTimeout(() => forceLogout('PASSWORD_CHANGED'), 900)
    } catch (cause) {
      setFeedback({ tone: 'error', message: cause instanceof Error ? cause.message : '密码修改失败' })
    } finally { setBusy('') }
  }

  return <AppShell className="profile-shell" title="个人设置" eyebrow="账号与安全" titleMeta={<span>维护个人资料与登录安全</span>}>
    <main className="profile-page">
      {feedback && <div className={`profile-feedback is-${feedback.tone}`} role={feedback.tone === 'error' ? 'alert' : 'status'}>{feedback.tone === 'success' ? <CheckCircle size={18} weight="fill" /> : <WarningCircle size={18} weight="fill" />}{feedback.message}</div>}
      <section className="profile-overview">
        <span><UserCircle size={44} weight="duotone" /></span>
        <div><h2>{profile?.displayName || '正在加载个人资料'}</h2><p>{profile?.email || '—'} · {profile?.status === 'ACTIVE' ? '账号正常' : profile?.status || '—'}</p></div>
        <em><ShieldCheck size={16} />{profile?.roles.includes('platform_admin') ? '平台管理员' : profile?.roles.includes('DOMAIN_ADMIN') ? '领域管理员' : '业务成员'}</em>
      </section>
      <div className="profile-grid">
        <form className="profile-card" onSubmit={saveProfile}>
          <header><span><IdentificationCard size={21} weight="duotone" /></span><div><h2>基础资料</h2><p>工号和邮箱由组织管理员维护</p></div></header>
          <label>显示姓名<input value={displayName} maxLength={100} onChange={event => setDisplayName(event.target.value)} /></label>
          <label>员工工号<input value={profile?.employeeNo || ''} disabled /></label>
          <label>企业邮箱<input value={profile?.email || ''} disabled /></label>
          <footer><AppButton variant="primary" type="submit" disabled={!displayName.trim() || busy === 'profile'}>{busy === 'profile' ? '保存中…' : '保存资料'}</AppButton></footer>
        </form>
        <form className="profile-card" onSubmit={changePassword}>
          <header><span><Key size={21} weight="duotone" /></span><div><h2>修改密码</h2><p>修改后所有终端需要重新登录</p></div></header>
          <label>当前密码<input type="password" autoComplete="current-password" value={currentPassword} onChange={event => setCurrentPassword(event.target.value)} /></label>
          <label>新密码<input type="password" autoComplete="new-password" value={newPassword} onChange={event => setNewPassword(event.target.value)} /><small>10–128 位，同时包含大小写字母和数字</small></label>
          <label>确认新密码<input type="password" autoComplete="new-password" value={confirmPassword} onChange={event => setConfirmPassword(event.target.value)} /></label>
          <footer><AppButton variant="primary" type="submit" disabled={!currentPassword || !newPassword || !confirmPassword || busy === 'password'}>{busy === 'password' ? '修改中…' : '修改密码'}</AppButton></footer>
        </form>
      </div>
    </main>
  </AppShell>
}

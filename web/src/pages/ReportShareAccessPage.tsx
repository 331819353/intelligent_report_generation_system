import { WarningCircle } from '@phosphor-icons/react'
import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { reportRuntimeAPI } from '../report/api/runtime'

export function ReportShareAccessPage() {
  const navigate = useNavigate()
  const { shareToken = '' } = useParams()
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    void reportRuntimeAPI.accessShare(shareToken)
      .then(({ version, filterSnapshot }) => {
        if (cancelled) return
        sessionStorage.setItem(`intelligent-report-share:${shareToken}`, JSON.stringify({
          reportId: version.reportId,
          versionNo: version.versionNo,
          filterSnapshot: filterSnapshot ?? {},
        }))
        navigate(`/reports/${encodeURIComponent(version.reportId)}?version=${version.versionNo}&share=${encodeURIComponent(shareToken)}`, { replace: true })
      })
      .catch(cause => {
        if (!cancelled) setError(cause instanceof Error ? cause.message : '分享链接暂时无法打开')
      })
    return () => { cancelled = true }
  }, [navigate, shareToken])

  return <AppShell>
    <main className="report-share-access" aria-live="polite">
      {!error && <div className="report-share-access-card is-loading">
        <span className="report-share-access-spinner" aria-hidden="true" />
        <h1>正在安全打开报告</h1>
        <p>正在校验接收身份、报告权限与固定版本。</p>
      </div>}
      {error && <div className="report-share-access-card is-error">
        <WarningCircle size={32} weight="fill" />
        <h1>无法打开这份分享</h1>
        <p>{error}</p>
        <button type="button" onClick={() => navigate('/reports', { replace: true })}>返回报告中心</button>
      </div>}
    </main>
  </AppShell>
}

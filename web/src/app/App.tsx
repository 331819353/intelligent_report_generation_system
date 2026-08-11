import { useEffect, useState } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { DatasetCenterPage } from '../pages/DatasetCenterPage'
import { DataSourceCenterPage } from '../pages/DataSourceCenterPage'
import { LoginPage } from '../pages/LoginPage'
import { ManagementCenterPage } from '../pages/ManagementCenterPage'
import { DomainAccessPage } from '../pages/DomainAccessPage'
import { AskDataPage } from '../pages/AskDataPage'
import { ReportAssetsPage } from '../pages/ReportAssetsPage'
import { ReportPage } from '../pages/ReportPage'
import { ReportEditorPage } from '../pages/ReportEditorPage'
import { ReportPublishReviewPage } from '../pages/ReportPublishReviewPage'
import { HomePage } from '../pages/HomePage'
import { ApprovalsPage, TasksPage } from '../pages/TasksPage'
import { DecisionsPage } from '../pages/DecisionsPage'
import { RequireAuth } from '../components/RequireAuth'
import { RequirePlatformAdministrator } from '../components/RequirePlatformAdministrator'
import { RequireBusinessDomain } from '../components/RequireBusinessDomain'
import { domainChangedEvent } from '../lib/domain-context'

/** 定义公开登录页、受保护业务页和兜底跳转。 */
export function App() {
  const [domainRevision, setDomainRevision] = useState(0)

  useEffect(() => {
    const refreshCurrentRoute = () => setDomainRevision(revision => revision + 1)
    window.addEventListener(domainChangedEvent, refreshCurrentRoute)
    return () => window.removeEventListener(domainChangedEvent, refreshCurrentRoute)
  }, [])

  return (
    <Routes key={domainRevision}>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/domain-access" element={<RequireAuth><DomainAccessPage /></RequireAuth>} />
      <Route path="/home" element={<RequireAuth><RequireBusinessDomain><HomePage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/approvals" element={<RequireAuth><RequireBusinessDomain><ApprovalsPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/tasks" element={<RequireAuth><RequireBusinessDomain><TasksPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/decisions" element={<RequireAuth><RequireBusinessDomain><DecisionsPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/platform-management" element={<Navigate to="/platform-management/domains" replace />} />
      <Route path="/platform-management/:section" element={<RequireAuth><RequirePlatformAdministrator><ManagementCenterPage /></RequirePlatformAdministrator></RequireAuth>} />
      <Route path="/platform-settings" element={<Navigate to="/platform-management/domains" replace />} />
      <Route path="/permissions" element={<Navigate to="/platform-management/permissions" replace />} />
      <Route path="/data-sources" element={<RequireAuth><RequireBusinessDomain><DataSourceCenterPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/datasets" element={<RequireAuth><RequireBusinessDomain><DatasetCenterPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/datasets/:datasetId/edit" element={<RequireAuth><RequireBusinessDomain><DatasetCenterPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/ask-data" element={<RequireAuth><RequireBusinessDomain><AskDataPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/ask-data/conversations/:conversationId" element={<RequireAuth><RequireBusinessDomain><AskDataPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/reports" element={<RequireAuth><RequireBusinessDomain><ReportAssetsPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/reports/new" element={<RequireAuth><RequireBusinessDomain><ReportEditorPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/reports/:reportId/publish-review" element={<RequireAuth><RequireBusinessDomain><ReportPublishReviewPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/reports/:reportId" element={<RequireAuth><RequireBusinessDomain><ReportPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="*" element={<Navigate to="/home" replace />} />
    </Routes>
  )
}

import { useEffect, useState } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { DatasetCenterPage } from '../pages/DatasetCenterPage'
import { DataSourceCenterPage } from '../pages/DataSourceCenterPage'
import { LoginPage } from '../pages/LoginPage'
import { ManagementCenterPage } from '../pages/ManagementCenterPage'
import { DomainAccessPage } from '../pages/DomainAccessPage'
import { AskDataPage } from '../pages/AskDataPage'
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
      <Route path="/platform-management" element={<Navigate to="/platform-management/domains" replace />} />
      <Route path="/platform-management/:section" element={<RequireAuth><RequirePlatformAdministrator><ManagementCenterPage /></RequirePlatformAdministrator></RequireAuth>} />
      <Route path="/platform-settings" element={<Navigate to="/platform-management/domains" replace />} />
      <Route path="/permissions" element={<Navigate to="/platform-management/permissions" replace />} />
      <Route path="/data-sources" element={<RequireAuth><RequireBusinessDomain><DataSourceCenterPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/datasets" element={<RequireAuth><RequireBusinessDomain><DatasetCenterPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/datasets/:datasetId/edit" element={<RequireAuth><RequireBusinessDomain><DatasetCenterPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="/ask-data" element={<RequireAuth><RequireBusinessDomain><AskDataPage /></RequireBusinessDomain></RequireAuth>} />
      <Route path="*" element={<Navigate to="/data-sources" replace />} />
    </Routes>
  )
}

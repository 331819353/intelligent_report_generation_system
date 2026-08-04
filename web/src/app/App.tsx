import { useEffect, useState } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { DatasetCenterPage } from '../pages/DatasetCenterPage'
import { DataSourceCenterPage } from '../pages/DataSourceCenterPage'
import { LoginPage } from '../pages/LoginPage'
import { ManagementCenterPage } from '../pages/ManagementCenterPage'
import { DomainAccessPage } from '../pages/DomainAccessPage'
import { RequireAuth } from '../components/RequireAuth'
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
      <Route path="/permissions" element={<RequireAuth><ManagementCenterPage /></RequireAuth>} />
      <Route path="/data-sources" element={<RequireAuth><DataSourceCenterPage /></RequireAuth>} />
      <Route path="/datasets" element={<RequireAuth><DatasetCenterPage /></RequireAuth>} />
      <Route path="/datasets/:datasetId/edit" element={<RequireAuth><DatasetCenterPage /></RequireAuth>} />
      <Route path="*" element={<Navigate to="/data-sources" replace />} />
    </Routes>
  )
}

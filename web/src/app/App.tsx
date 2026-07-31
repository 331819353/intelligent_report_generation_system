import { useEffect, useState } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { AdminPage } from '../pages/AdminPage'
import { DesignerPage } from '../pages/DesignerPage'
import { DatasetCenterPage } from '../pages/DatasetCenterPage'
import { DataSourceCenterPage } from '../pages/DataSourceCenterPage'
import { DimensionValueGraphPage } from '../pages/DimensionValueGraphPage'
import { LoginPage } from '../pages/LoginPage'
import { MetricCatalogPage } from '../pages/MetricCatalogPage'
import { MetricCenterPage } from '../pages/MetricCenterPage'
import { ManagementCenterPage } from '../pages/ManagementCenterPage'
import { SemanticAssetPage } from '../pages/SemanticAssetPage'
import { SemanticParsingRulePage } from '../pages/SemanticParsingRulePage'
import { SemanticChatPage } from '../pages/SemanticChatPage'
import { ViewerPage } from '../pages/ViewerPage'
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
      <Route path="/admin" element={<RequireAuth><AdminPage /></RequireAuth>} />
      <Route path="/management" element={<RequireAuth><ManagementCenterPage /></RequireAuth>} />
      <Route path="/data-sources" element={<RequireAuth><DataSourceCenterPage /></RequireAuth>} />
      <Route path="/datasets" element={<RequireAuth><DatasetCenterPage /></RequireAuth>} />
      <Route path="/datasets/:datasetId/edit" element={<RequireAuth><DatasetCenterPage /></RequireAuth>} />
      <Route path="/assets/metrics" element={<RequireAuth><MetricCatalogPage /></RequireAuth>} />
      <Route path="/assets/semantics" element={<RequireAuth><SemanticAssetPage /></RequireAuth>} />
      <Route path="/assets/parsing-rules" element={<RequireAuth><SemanticParsingRulePage /></RequireAuth>} />
      <Route path="/assets/dimensions" element={<Navigate to="/assets/metrics" replace />} />
      <Route path="/assets/dimension-values" element={<RequireAuth><DimensionValueGraphPage /></RequireAuth>} />
      <Route path="/assistant" element={<RequireAuth><SemanticChatPage /></RequireAuth>} />
      <Route path="/metrics" element={<Navigate to="/assets/metrics" replace />} />
      <Route path="/metrics/new" element={<RequireAuth><MetricCenterPage /></RequireAuth>} />
      <Route path="/metrics/:metricId/edit" element={<RequireAuth><MetricCenterPage /></RequireAuth>} />
      <Route path="/semantic-governance" element={<Navigate to="/assets/metrics" replace />} />
      <Route path="/designer/:reportId" element={<RequireAuth><DesignerPage /></RequireAuth>} />
      <Route path="/reports/:reportId" element={<RequireAuth><ViewerPage /></RequireAuth>} />
      <Route path="*" element={<Navigate to="/login" replace />} />
    </Routes>
  )
}

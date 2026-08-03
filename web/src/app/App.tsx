import { lazy, Suspense, useEffect, useState, type ReactNode } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { AdminPage } from '../pages/AdminPage'
import { DatasetCenterPage } from '../pages/DatasetCenterPage'
import { DataSourceCenterPage } from '../pages/DataSourceCenterPage'
import { DimensionValueGraphPage } from '../pages/DimensionValueGraphPage'
import { LoginPage } from '../pages/LoginPage'
import { MetricCatalogPage } from '../pages/MetricCatalogPage'
import { MetricCenterPage } from '../pages/MetricCenterPage'
import { ManagementCenterPage } from '../pages/ManagementCenterPage'
import { SemanticAssetPage } from '../pages/SemanticAssetPage'
import { AssetOverviewPage } from '../pages/AssetOverviewPage'
import { SemanticParsingRulePage } from '../pages/SemanticParsingRulePage'
import { SemanticChatPage } from '../pages/SemanticChatPage'
import { RequireAuth } from '../components/RequireAuth'
import { domainChangedEvent } from '../lib/domain-context'

const DesignerPage = lazy(() => import('../pages/DesignerPage').then(module => ({ default: module.DesignerPage })))
const ViewerPage = lazy(() => import('../pages/ViewerPage').then(module => ({ default: module.ViewerPage })))

function LazyReportPage({ children }: { children: ReactNode }) {
  return <Suspense fallback={<main className="viewer-page"><div className="report-runtime-state"><strong>正在加载报表模块</strong><span>初始化 Card SDK 与共享 Renderer…</span></div></main>}>{children}</Suspense>
}

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
      <Route path="/assets" element={<Navigate to="/assets/overview" replace />} />
      <Route path="/assets/overview" element={<RequireAuth><AssetOverviewPage /></RequireAuth>} />
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
      <Route path="/report-studio/:reportId" element={<RequireAuth><LazyReportPage><DesignerPage /></LazyReportPage></RequireAuth>} />
      <Route path="/designer/:reportId" element={<RequireAuth><LazyReportPage><DesignerPage /></LazyReportPage></RequireAuth>} />
      <Route path="/reports/:reportId" element={<RequireAuth><LazyReportPage><ViewerPage /></LazyReportPage></RequireAuth>} />
      <Route path="*" element={<Navigate to="/login" replace />} />
    </Routes>
  )
}

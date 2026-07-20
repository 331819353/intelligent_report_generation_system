import { Navigate, Route, Routes } from 'react-router-dom'
import { AdminPage } from '../pages/AdminPage'
import { DesignerPage } from '../pages/DesignerPage'
import { DatasetCenterPage } from '../pages/DatasetCenterPage'
import { DataSourceCenterPage } from '../pages/DataSourceCenterPage'
import { LoginPage } from '../pages/LoginPage'
import { MetricCatalogPage } from '../pages/MetricCatalogPage'
import { MetricCenterPage } from '../pages/MetricCenterPage'
import { ViewerPage } from '../pages/ViewerPage'
import { RequireAuth } from '../components/RequireAuth'

/** 定义公开登录页、受保护业务页和兜底跳转。 */
export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/admin" element={<RequireAuth><AdminPage /></RequireAuth>} />
      <Route path="/data-sources" element={<RequireAuth><DataSourceCenterPage /></RequireAuth>} />
      <Route path="/datasets" element={<RequireAuth><DatasetCenterPage /></RequireAuth>} />
      <Route path="/datasets/:datasetId/edit" element={<RequireAuth><DatasetCenterPage /></RequireAuth>} />
      <Route path="/metrics" element={<RequireAuth><MetricCatalogPage /></RequireAuth>} />
      <Route path="/metrics/new" element={<RequireAuth><MetricCenterPage /></RequireAuth>} />
      <Route path="/metrics/:metricId/edit" element={<RequireAuth><MetricCenterPage /></RequireAuth>} />
      <Route path="/designer/:reportId" element={<RequireAuth><DesignerPage /></RequireAuth>} />
      <Route path="/reports/:reportId" element={<RequireAuth><ViewerPage /></RequireAuth>} />
      <Route path="*" element={<Navigate to="/login" replace />} />
    </Routes>
  )
}

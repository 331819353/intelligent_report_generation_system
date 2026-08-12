import { lazy, Suspense, useEffect, useState } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { RequireAuth } from '../components/RequireAuth'
import { RequirePlatformAdministrator } from '../components/RequirePlatformAdministrator'
import { RequireBusinessDomain } from '../components/RequireBusinessDomain'
import { domainChangedEvent } from '../lib/domain-context'
import { AppErrorBoundary } from '../components/AppErrorBoundary'

const DatasetCenterPage = lazy(() => import('../pages/DatasetCenterPage').then(module => ({ default: module.DatasetCenterPage })))
const DataSourceCenterPage = lazy(() => import('../pages/DataSourceCenterPage').then(module => ({ default: module.DataSourceCenterPage })))
const DataSourceAssetsPage = lazy(() => import('../pages/DataSourceAssetsPage').then(module => ({ default: module.DataSourceAssetsPage })))
const LoginPage = lazy(() => import('../pages/LoginPage').then(module => ({ default: module.LoginPage })))
const ManagementCenterPage = lazy(() => import('../pages/ManagementCenterPage').then(module => ({ default: module.ManagementCenterPage })))
const DomainAccessPage = lazy(() => import('../pages/DomainAccessPage').then(module => ({ default: module.DomainAccessPage })))
const AskDataPage = lazy(() => import('../pages/AskDataPage').then(module => ({ default: module.AskDataPage })))
const ReportAssetsPage = lazy(() => import('../pages/ReportAssetsPage').then(module => ({ default: module.ReportAssetsPage })))
const ReportPage = lazy(() => import('../pages/ReportPage').then(module => ({ default: module.ReportPage })))
const ReportEditorPage = lazy(() => import('../pages/ReportEditorPage').then(module => ({ default: module.ReportEditorPage })))
const ReportPublishReviewPage = lazy(() => import('../pages/ReportPublishReviewPage').then(module => ({ default: module.ReportPublishReviewPage })))
const ReportShareAccessPage = lazy(() => import('../pages/ReportShareAccessPage').then(module => ({ default: module.ReportShareAccessPage })))
const HomePage = lazy(() => import('../pages/HomePage').then(module => ({ default: module.HomePage })))
const ApprovalsPage = lazy(() => import('../pages/TasksPage').then(module => ({ default: module.ApprovalsPage })))
const TasksPage = lazy(() => import('../pages/TasksPage').then(module => ({ default: module.TasksPage })))
const DecisionsPage = lazy(() => import('../pages/DecisionsPage').then(module => ({ default: module.DecisionsPage })))
const UserPermissionsPage = lazy(() => import('../pages/UserPermissionsPage').then(module => ({ default: module.UserPermissionsPage })))
const SemanticCenterPage = lazy(() => import('../pages/SemanticCenterPage').then(module => ({ default: module.SemanticCenterPage })))
const RuntimeConfigPage = lazy(() => import('../pages/RuntimeConfigPage').then(module => ({ default: module.RuntimeConfigPage })))
const ProfilePage = lazy(() => import('../pages/ProfilePage').then(module => ({ default: module.ProfilePage })))
const HelpPage = lazy(() => import('../pages/HelpPage').then(module => ({ default: module.HelpPage })))

function RouteLoading() {
  return <main className="route-loading" aria-live="polite" aria-busy="true">
    <img src="/haier-logo.svg" alt="" />
    <span><i /><i /><i /></span>
    <strong>正在加载工作台</strong>
  </main>
}

/** 定义公开登录页、受保护业务页和兜底跳转。 */
export function App() {
  const location = useLocation()
  const [domainRevision, setDomainRevision] = useState(0)

  useEffect(() => {
    const refreshCurrentRoute = () => {
      // 视觉回归快照需要在当前页面内展示切换后的状态；生产环境仍通过
      // 重新挂载当前路由，确保所有领域级数据和权限上下文完整刷新。
      const snapshot = import.meta.env.DEV && new URLSearchParams(window.location.search).has('snapshot')
      if (!snapshot) setDomainRevision(revision => revision + 1)
    }
    window.addEventListener(domainChangedEvent, refreshCurrentRoute)
    return () => window.removeEventListener(domainChangedEvent, refreshCurrentRoute)
  }, [])

  return (
    <AppErrorBoundary resetKey={`${domainRevision}:${location.pathname}`}>
      <Suspense fallback={<RouteLoading />}>
        <Routes key={domainRevision}>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/domain-access" element={<RequireAuth><DomainAccessPage /></RequireAuth>} />
        <Route path="/home" element={<RequireAuth><RequireBusinessDomain><HomePage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/profile" element={<RequireAuth><RequireBusinessDomain><ProfilePage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/help" element={<RequireAuth><RequireBusinessDomain><HelpPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/approvals" element={<RequireAuth><RequireBusinessDomain><ApprovalsPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/tasks" element={<RequireAuth><RequireBusinessDomain><TasksPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/decisions" element={<RequireAuth><RequireBusinessDomain><DecisionsPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/platform-management" element={<Navigate to="/platform-management/domains" replace />} />
        <Route path="/platform-management/users" element={<RequireAuth><RequirePlatformAdministrator><UserPermissionsPage /></RequirePlatformAdministrator></RequireAuth>} />
        <Route path="/platform-management/runtime-config" element={<RequireAuth><RequirePlatformAdministrator><RuntimeConfigPage /></RequirePlatformAdministrator></RequireAuth>} />
        <Route path="/platform-management/:section" element={<RequireAuth><RequirePlatformAdministrator><ManagementCenterPage /></RequirePlatformAdministrator></RequireAuth>} />
        <Route path="/platform-settings" element={<Navigate to="/platform-management/domains" replace />} />
        <Route path="/permissions" element={<Navigate to="/platform-management/permissions" replace />} />
        <Route path="/data-sources" element={<RequireAuth><RequireBusinessDomain><DataSourceCenterPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/data-sources/:sourceId/assets" element={<RequireAuth><RequireBusinessDomain><DataSourceAssetsPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/data-sources/:sourceId/assets/discover" element={<RequireAuth><RequireBusinessDomain><DataSourceAssetsPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/data-sources/:sourceId/assets/:tableId" element={<RequireAuth><RequireBusinessDomain><DataSourceAssetsPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/datasets" element={<RequireAuth><RequireBusinessDomain><DatasetCenterPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/datasets/:datasetId/edit" element={<RequireAuth><RequireBusinessDomain><DatasetCenterPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/semantic" element={<RequireAuth><RequireBusinessDomain><SemanticCenterPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/ask-data" element={<RequireAuth><RequireBusinessDomain><AskDataPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/ask-data/conversations/:conversationId" element={<RequireAuth><RequireBusinessDomain><AskDataPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/reports" element={<RequireAuth><RequireBusinessDomain><ReportAssetsPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/report-shares/:shareToken" element={<RequireAuth><RequireBusinessDomain><ReportShareAccessPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/reports/new" element={<RequireAuth><RequireBusinessDomain><ReportEditorPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/reports/:reportId/publish-review" element={<RequireAuth><RequireBusinessDomain><ReportPublishReviewPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="/reports/:reportId" element={<RequireAuth><RequireBusinessDomain><ReportPage /></RequireBusinessDomain></RequireAuth>} />
        <Route path="*" element={<Navigate to="/home" replace />} />
        </Routes>
      </Suspense>
    </AppErrorBoundary>
  )
}

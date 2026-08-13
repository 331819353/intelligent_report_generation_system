import { lazy, Suspense, useEffect, useState, type ReactNode } from 'react'
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

function AuthenticatedRoute({ children }: { children: ReactNode }) {
  return <RequireAuth>{children}</RequireAuth>
}

function BusinessRoute({ children }: { children: ReactNode }) {
  return <AuthenticatedRoute><RequireBusinessDomain>{children}</RequireBusinessDomain></AuthenticatedRoute>
}

function AdministrationRoute({ children }: { children: ReactNode }) {
  return <AuthenticatedRoute><RequirePlatformAdministrator>{children}</RequirePlatformAdministrator></AuthenticatedRoute>
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
        <Route path="/domain-access" element={<AuthenticatedRoute><DomainAccessPage /></AuthenticatedRoute>} />
        <Route path="/home" element={<BusinessRoute><HomePage /></BusinessRoute>} />
        <Route path="/profile" element={<BusinessRoute><ProfilePage /></BusinessRoute>} />
        <Route path="/help" element={<BusinessRoute><HelpPage /></BusinessRoute>} />
        <Route path="/approvals" element={<BusinessRoute><ApprovalsPage /></BusinessRoute>} />
        <Route path="/tasks" element={<BusinessRoute><TasksPage /></BusinessRoute>} />
        <Route path="/decisions" element={<BusinessRoute><DecisionsPage /></BusinessRoute>} />
        <Route path="/platform-management" element={<Navigate to="/platform-management/domains" replace />} />
        <Route path="/platform-management/users" element={<AdministrationRoute><UserPermissionsPage /></AdministrationRoute>} />
        <Route path="/platform-management/runtime-config" element={<AdministrationRoute><RuntimeConfigPage /></AdministrationRoute>} />
        <Route path="/platform-management/:section" element={<AdministrationRoute><ManagementCenterPage /></AdministrationRoute>} />
        <Route path="/platform-settings" element={<Navigate to="/platform-management/domains" replace />} />
        <Route path="/permissions" element={<Navigate to="/platform-management/permissions" replace />} />
        <Route path="/data-sources" element={<BusinessRoute><DataSourceCenterPage /></BusinessRoute>} />
        <Route path="/data-sources/:sourceId/assets" element={<BusinessRoute><DataSourceAssetsPage /></BusinessRoute>} />
        <Route path="/data-sources/:sourceId/assets/discover" element={<BusinessRoute><DataSourceAssetsPage /></BusinessRoute>} />
        <Route path="/data-sources/:sourceId/assets/:tableId" element={<BusinessRoute><DataSourceAssetsPage /></BusinessRoute>} />
        <Route path="/datasets" element={<BusinessRoute><DatasetCenterPage /></BusinessRoute>} />
        <Route path="/datasets/:datasetId/edit" element={<BusinessRoute><DatasetCenterPage /></BusinessRoute>} />
        <Route path="/semantic" element={<BusinessRoute><SemanticCenterPage /></BusinessRoute>} />
        <Route path="/semantic/:section" element={<BusinessRoute><SemanticCenterPage /></BusinessRoute>} />
        <Route path="/ask-data" element={<BusinessRoute><AskDataPage /></BusinessRoute>} />
        <Route path="/ask-data/conversations/:conversationId" element={<BusinessRoute><AskDataPage /></BusinessRoute>} />
        <Route path="/reports" element={<BusinessRoute><ReportAssetsPage /></BusinessRoute>} />
        <Route path="/report-shares/:shareToken" element={<BusinessRoute><ReportShareAccessPage /></BusinessRoute>} />
        <Route path="/reports/new" element={<BusinessRoute><ReportEditorPage /></BusinessRoute>} />
        <Route path="/reports/:reportId/publish-review" element={<BusinessRoute><ReportPublishReviewPage /></BusinessRoute>} />
        <Route path="/reports/:reportId" element={<BusinessRoute><ReportPage /></BusinessRoute>} />
        <Route path="*" element={<Navigate to="/home" replace />} />
        </Routes>
      </Suspense>
    </AppErrorBoundary>
  )
}

import { Navigate, Route, Routes } from 'react-router-dom'
import { AdminPage } from '../pages/AdminPage'
import { DesignerPage } from '../pages/DesignerPage'
import { LoginPage } from '../pages/LoginPage'
import { ViewerPage } from '../pages/ViewerPage'
import { RequireAuth } from '../components/RequireAuth'

/** 定义公开登录页、受保护业务页和兜底跳转。 */
export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/admin" element={<RequireAuth><AdminPage /></RequireAuth>} />
      <Route path="/designer/:reportId" element={<RequireAuth><DesignerPage /></RequireAuth>} />
      <Route path="/reports/:reportId" element={<RequireAuth><ViewerPage /></RequireAuth>} />
      <Route path="*" element={<Navigate to="/login" replace />} />
    </Routes>
  )
}

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { App } from './app/App'
import './styles/global.css'
import './styles/dataset-designer.css'
import './styles/data-source-center.css'
import './styles/data-source-assets.css'
import './styles/dataset-center.css'
import './styles/administration.css'
import './styles/ask-data.css'
import './styles/data-request.css'
import './styles/report.css'
import './styles/shell-v2.css'
import './styles/home.css'
import './styles/tasks.css'
import './styles/decisions.css'
import './styles/user-permissions.css'
import './styles/semantic-center.css'
import './styles/runtime-config.css'
import './styles/operational-observability.css'
import './styles/profile.css'
import './styles/help.css'

// 在严格模式和浏览器路由上下文中挂载应用根组件。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)

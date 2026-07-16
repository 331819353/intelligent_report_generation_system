import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { App } from './app/App'
import './styles/global.css'
import './styles/dataset-designer.css'
import './styles/metric-center.css'
import './styles/data-source-center.css'
import './styles/report-renderer.css'

// 在严格模式和浏览器路由上下文中挂载应用根组件。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)

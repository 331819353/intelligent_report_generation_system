import { StrictMode } from 'react'
import ConfigProvider from 'antd/es/config-provider'
import zhCN from 'antd/locale/zh_CN'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { App } from './app/App'
import './styles/global.css'
import './styles/dataset-designer.css'
import './styles/data-source-center.css'
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

// 在严格模式和浏览器路由上下文中挂载应用根组件。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {/* 界面语言为简体中文，必须显式指定 antd locale，
        否则分页、空态、确认弹窗等内置文案会回落为英文。 */}
    <ConfigProvider locale={zhCN} theme={{
      token: {
        colorPrimary: '#0872d3',
        borderRadius: 5,
        controlHeight: 40,
        fontSize: 14,
        fontFamily: 'Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
      },
    }}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </ConfigProvider>
  </StrictMode>,
)

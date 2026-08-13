import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    // ECharts' canvas renderer is an isolated, route-loaded chunk (~524 kB).
    // Keep the budget just above that known dependency so application growth
    // still produces a warning instead of disabling chunk-size checks.
    chunkSizeWarningLimit: 550,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': process.env.VITE_API_PROXY_TARGET || 'http://localhost:8080',
    },
  },
})

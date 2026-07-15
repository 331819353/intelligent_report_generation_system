import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { expect, test } from 'vitest'
import { App } from './App'

test('renders login route', () => {
  render(<MemoryRouter initialEntries={['/login']}><App /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: '登录智能报告平台' })).toBeInTheDocument()
})

test('renders designer route', () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  render(<MemoryRouter initialEntries={['/designer/draft']}><App /></MemoryRouter>)
  expect(screen.getByRole('heading', { level: 1, name: '企业经营月度分析' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /AI 修改/ })).toBeInTheDocument()
})

test('redirects anonymous users away from protected routes', () => {
  sessionStorage.clear()
  render(<MemoryRouter initialEntries={['/admin']}><App /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: '登录智能报告平台' })).toBeInTheDocument()
})

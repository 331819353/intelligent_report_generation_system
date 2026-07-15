import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { PreviewTable } from './DatasetDesignerPage'

test('数据预览展示结构化 Join 风险告警', () => {
  render(<PreviewTable preview={{
    queryId: 'query-1', columns: ['revenue'], rows: [[20]], rowCount: 1, durationMs: 8,
    warnings: [{ code: 'JOIN_FANOUT_RISK', message: '关联结果可能发生扇出。', joinId: 'orders_customers', estimatedRows: 4 }],
  }} />)
  expect(screen.getByRole('region', { name: 'Join 风险提示' })).toBeInTheDocument()
  expect(screen.getByText('关联结果可能发生扇出。')).toBeInTheDocument()
  expect(screen.getByText('预计 4 行')).toBeInTheDocument()
})

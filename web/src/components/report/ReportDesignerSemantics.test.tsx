import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { demoReportRuntime } from '../../lib/demo-report-runtime'
import { createReportDesignerTemplate } from '../../lib/report-designer-template'
import { ReportDesignerCanvas } from './ReportDesignerCanvas'
import { ReportRenderer } from './ReportRenderer'

describe('report designer semantic blocks', () => {
  it('renders the real menu block and a hidden content-area placeholder', () => {
    const view = render(<ReportRenderer document={createReportDesignerTemplate()} runtime={demoReportRuntime} mode="designer" />)
    expect(view.container.querySelectorAll('.report-block--menu')).toHaveLength(1)
    expect(screen.getByRole('navigation', { name: '报告导航' })).toBeInTheDocument()
    expect(screen.getByText('结论区已隐藏')).toBeInTheDocument()
  })

  it('stores custom menu ratios in the report history JSON', () => {
    const onChange = vi.fn()
    render(<ReportDesignerCanvas source={createReportDesignerTemplate()} runtime={demoReportRuntime} onChange={onChange} />)

    fireEvent.change(screen.getByRole('spinbutton', { name: '第一行 · Logo/标题 : 功能区第一项' }), { target: { value: '4' } })
    const document = onChange.mock.calls.at(-1)?.[0]
    const menu = document.pages[0].blocks.find((block: { kind?: string }) => block.kind === 'MENU')
    expect(menu.menuLayout.ratios.topColumns).toEqual([4, 1])
    expect(menu.menuLayout.usesDefaultRatios).toBe(false)
    expect(screen.getByRole('button', { name: '恢复默认比例 3:1 / 1:1 / 2:1' })).toBeEnabled()
  })
})

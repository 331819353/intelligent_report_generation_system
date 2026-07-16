import type { DragEvent } from 'react'
import type { ComponentType } from '../../lib/report-contract'
import { REPORT_COMPONENT_DRAG_MIME, reportComponentPaletteItems } from './componentDrag'

type ComponentPaletteProps = {
  selectedType?: ComponentType
  onSelect?: (type: ComponentType) => void
}

/** 组件面板只传递受支持的类型编码，实际组件对象由布局领域函数创建。 */
export function ComponentPalette({ selectedType, onSelect }: ComponentPaletteProps) {
  function handleDragStart(event: DragEvent<HTMLButtonElement>, type: ComponentType) {
    event.dataTransfer.effectAllowed = 'copy'
    event.dataTransfer.setData(REPORT_COMPONENT_DRAG_MIME, type)
  }

  return reportComponentPaletteItems.map(item => (
    <button
      key={item.type}
      className="component-tile"
      type="button"
      draggable
      data-component-palette-type={item.type}
      aria-label={`拖入${item.label}组件`}
      aria-pressed={selectedType === item.type}
      onDragStart={event => handleDragStart(event, item.type)}
      onClick={() => onSelect?.(item.type)}
    >
      ＋ {item.label}
    </button>
  ))
}

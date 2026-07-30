import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { MagicWandIcon } from '@phosphor-icons/react'
import type { CanvasPoint } from '../../lib/dataset-graph'

type DatasetAIDockProps = {
  hasProposal: boolean
  children: ReactNode
}

export function DatasetAIDock({ hasProposal, children }: DatasetAIDockProps) {
  const dockRef = useRef<HTMLElement | null>(null)
  const dragRef = useRef<{
    parent: HTMLElement
    pointerID: number
    clientX: number
    clientY: number
    x: number
    y: number
  } | null>(null)
  const [position, setPosition] = useState<CanvasPoint | null>(null)
  const [dragging, setDragging] = useState(false)
  const [opensRight, setOpensRight] = useState(false)

  const beginDrag = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0 || !event.isPrimary) return
    const dock = dockRef.current
    const parent = dock?.parentElement
    if (!dock || !parent) return
    const dockBounds = dock.getBoundingClientRect()
    const parentBounds = parent.getBoundingClientRect()
    const x = position?.x ?? dockBounds.left - parentBounds.left + parent.scrollLeft
    const y = position?.y ?? dockBounds.top - parentBounds.top + parent.scrollTop
    dragRef.current = {
      parent,
      pointerID: event.pointerId,
      clientX: event.clientX,
      clientY: event.clientY,
      x,
      y,
    }
    setPosition({ x, y })
    setDragging(true)
    event.preventDefault()
    event.stopPropagation()
  }

  useEffect(() => {
    if (!dragging) return
    const moveDock = (event: globalThis.PointerEvent) => {
      const drag = dragRef.current
      if (!drag || event.pointerId !== drag.pointerID) return
      event.preventDefault()
      const minX = drag.parent.scrollLeft + 12
      const minY = drag.parent.scrollTop + 12
      const maxX = minX + Math.max(0, drag.parent.clientWidth - 76)
      const maxY = minY + Math.max(0, drag.parent.clientHeight - 76)
      const x = Math.min(maxX, Math.max(minX, drag.x + event.clientX - drag.clientX))
      const y = Math.min(maxY, Math.max(minY, drag.y + event.clientY - drag.clientY))
      setPosition({ x, y })
      setOpensRight(x - drag.parent.scrollLeft < drag.parent.clientWidth / 2)
    }
    const finishDrag = (event: globalThis.PointerEvent) => {
      const drag = dragRef.current
      if (!drag || event.pointerId !== drag.pointerID) return
      dragRef.current = null
      setDragging(false)
    }
    window.addEventListener('pointermove', moveDock, { passive: false })
    window.addEventListener('pointerup', finishDrag)
    window.addEventListener('pointercancel', finishDrag)
    return () => {
      window.removeEventListener('pointermove', moveDock)
      window.removeEventListener('pointerup', finishDrag)
      window.removeEventListener('pointercancel', finishDrag)
    }
  }, [dragging])

  return <section
    ref={dockRef}
    className={`dataset-ai-composer ${hasProposal ? 'has-proposal' : ''} ${dragging ? 'is-dragging' : ''} ${opensRight ? 'opens-right' : ''}`}
    style={position ? { left: position.x, top: position.y, right: 'auto', transform: 'none' } : undefined}
    aria-label="AI 自动配置数据流"
    onMouseDown={event => event.stopPropagation()}
    onClick={event => event.stopPropagation()}
    onDrop={event => event.stopPropagation()}
  >
    <button className="dataset-ai-dock-trigger" type="button" aria-label="拖动或悬停打开 AI 助手" onPointerDown={beginDrag}>
      <MagicWandIcon aria-hidden="true" size={22} weight="fill" />
      <span>AI</span>
    </button>
    <div className="dataset-ai-conversation" role="dialog" aria-label="AI 数据流助手">
      {children}
    </div>
  </section>
}

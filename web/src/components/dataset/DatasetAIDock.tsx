import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { MagicWandIcon, XIcon } from '@phosphor-icons/react'
import type { CanvasPoint } from '../../lib/dataset-graph'

type DatasetAIDockProps = {
  hasProposal: boolean
  existingDAG?: boolean
  children: ReactNode
}

export function DatasetAIDock({ hasProposal, existingDAG = false, children }: DatasetAIDockProps) {
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
  const [open, setOpen] = useState(false)
  const suppressClickRef = useRef(false)

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
    suppressClickRef.current = false
    event.preventDefault()
    event.stopPropagation()
  }

  useEffect(() => {
    if (!dragging) return
    const moveDock = (event: globalThis.PointerEvent) => {
      const drag = dragRef.current
      if (!drag || event.pointerId !== drag.pointerID) return
      event.preventDefault()
      if (Math.abs(event.clientX - drag.clientX) > 4 || Math.abs(event.clientY - drag.clientY) > 4) suppressClickRef.current = true
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
    className={`dataset-ai-composer ${hasProposal ? 'has-proposal' : ''} ${dragging ? 'is-dragging' : ''} ${opensRight ? 'opens-right' : ''} ${open ? 'is-open' : ''}`}
    style={position ? { left: position.x, top: position.y, right: 'auto', transform: 'none' } : undefined}
    aria-label="AI 自动配置数据流"
    onMouseDown={event => event.stopPropagation()}
    onClick={event => event.stopPropagation()}
    onDrop={event => event.stopPropagation()}
  >
    <button className="dataset-ai-dock-trigger" type="button" aria-label={open ? '关闭 AI 数据流助手' : '打开 AI 数据流助手'} aria-expanded={open} onPointerDown={beginDrag} onClick={() => { if (suppressClickRef.current) { suppressClickRef.current = false; return } setOpen(current => !current) }}>
      <MagicWandIcon aria-hidden="true" size={22} weight="fill" />
      <span>AI</span>
    </button>
    <div className="dataset-ai-conversation" role="dialog" aria-label="AI 数据流助手">
      <header className="dataset-ai-conversation-header"><span aria-hidden="true"><MagicWandIcon size={18} weight="fill" /></span><div><strong>AI 数据流助手</strong><small>{existingDAG ? hasProposal ? '继续调整组件方案，确认后应用修改' : '修改、新增或删除当前画布组件' : hasProposal ? '对话调整方案，确认后生成到画布' : '分阶段确认并生成可编辑 DAG'}</small></div><button type="button" aria-label="关闭 AI 数据流助手" onClick={() => setOpen(false)}><XIcon size={16} weight="bold" /></button></header>
      {children}
    </div>
  </section>
}

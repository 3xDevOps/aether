import { Minus, Plus } from '@/components/icons'
import { useCallback, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, PointerEvent, ReactNode } from 'react'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { runLabel } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import { layoutRunMap, type RunMapLayout } from '@/routes/board/map-layout'
import { RunCard } from '@/routes/board/run-card'
import type { BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { maxBoardMapZoom, minBoardMapZoom, normalizeBoardMapViewport, type BoardMapViewport } from '@/store/ui'

const controls = 'button:not([data-card-open]), a, input, select, textarea, [role="button"], [contenteditable], [data-slot="card-controls"]'

type MapProps = {
  scope: string
  agentNames: Record<string, string>
  actions?: ReactNode
  empty?: ReactNode
}

export function RunMap({ cards, ...props }: MapProps & { cards: BoardCard[] }) {
  const layout = useMemo(() => layoutRunMap(cards), [cards])
  return <MapViewport key={props.scope} layout={layout} {...props} />
}

function MapViewport({ layout, scope, agentNames, actions, empty }: MapProps & { layout: RunMapLayout }) {
  const setSavedViewport = useStore((s) => s.setBoardMapViewport)
  const [viewport, setViewport] = useState<BoardMapViewport>(() =>
    normalizeBoardMapViewport(useStore.getState().boardMapViewports[scope]) ?? { x: 0, y: 0, zoom: 1 },
  )
  const camera = useRef(viewport)
  const initialized = useRef(Boolean(normalizeBoardMapViewport(useStore.getState().boardMapViewports[scope])))
  const canvas = useRef<HTMLDivElement>(null)
  const saveFrame = useRef<number | null>(null)
  const dirty = useRef(false)
  const pointers = useRef(new Map<number, { x: number; y: number; startX: number; startY: number; pan: boolean }>())
  const suppressClick = useRef(false)
  const [dragging, setDragging] = useState(false)
  const instructionsId = useId()
  const arrowId = useId().replace(/:/g, '')
  const nodesByKey = useMemo(() => new Map(layout.nodes.map((node) => [node.key, node])), [layout])

  const commit = useCallback((candidate: BoardMapViewport) => {
    const next = normalizeBoardMapViewport(candidate)
    if (!next) return
    camera.current = next
    initialized.current = true
    setViewport(next)
    dirty.current = true
    if (saveFrame.current === null) {
      saveFrame.current = requestAnimationFrame(() => {
        saveFrame.current = null
        dirty.current = false
        setSavedViewport(scope, camera.current)
      })
    }
  }, [scope, setSavedViewport])

  const fit = useCallback(() => {
    const element = canvas.current
    if (!element?.clientWidth || !element.clientHeight || !layout.nodes.length) return
    const zoom = Math.max(minBoardMapZoom, Math.min(1,
      (element.clientWidth - 24) / layout.width,
      (element.clientHeight - 24) / layout.height,
    ))
    commit({
      x: (element.clientWidth - layout.width * zoom) / 2,
      y: (element.clientHeight - layout.height * zoom) / 2,
      zoom,
    })
  }, [commit, layout.width, layout.height, layout.nodes.length])

  const zoomAt = useCallback((factor: number, x?: number, y?: number) => {
    const element = canvas.current
    if (!element) return
    const current = camera.current
    const zoom = Math.max(minBoardMapZoom, Math.min(maxBoardMapZoom, current.zoom * factor))
    const anchorX = x ?? element.clientWidth / 2
    const anchorY = y ?? element.clientHeight / 2
    const ratio = zoom / current.zoom
    commit({ x: anchorX - (anchorX - current.x) * ratio, y: anchorY - (anchorY - current.y) * ratio, zoom })
  }, [commit])

  useLayoutEffect(() => {
    // Live events never reset a chosen camera, even if every card moves offscreen.
    if (initialized.current) return
    fit()
    const observer = new ResizeObserver(() => { if (!initialized.current) fit() })
    if (canvas.current) observer.observe(canvas.current)
    return () => observer.disconnect()
  }, [fit])

  useLayoutEffect(() => {
    const element = canvas.current
    if (!element) return
    const wheel = (event: WheelEvent) => {
      if (event.target instanceof Element && event.target.closest('input, select, textarea, [contenteditable]')) return
      event.preventDefault()
      const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? element.clientHeight : 1
      if (event.ctrlKey || event.metaKey) {
        const rect = element.getBoundingClientRect()
        zoomAt(Math.exp(-event.deltaY * unit * 0.002), event.clientX - rect.left, event.clientY - rect.top)
      } else {
        const x = event.shiftKey && !event.deltaX ? event.deltaY : event.deltaX
        const y = event.shiftKey && !event.deltaX ? 0 : event.deltaY
        commit({ ...camera.current, x: camera.current.x - x * unit, y: camera.current.y - y * unit })
      }
    }
    element.addEventListener('wheel', wheel, { passive: false })
    return () => {
      element.removeEventListener('wheel', wheel)
      if (saveFrame.current !== null) cancelAnimationFrame(saveFrame.current)
      saveFrame.current = null
      if (dirty.current) {
        dirty.current = false
        setSavedViewport(scope, camera.current)
      }
    }
  }, [commit, scope, setSavedViewport, zoomAt])

  function pointerDown(event: PointerEvent<HTMLDivElement>) {
    if (event.button !== 0 || !event.currentTarget.contains(event.target as Node)) return
    const target = event.target instanceof Element ? event.target : null
    const touch = event.pointerType === 'touch'
    if (!pointers.current.size) suppressClick.current = false
    const control = Boolean(target?.closest(controls))
    if (!touch && control) return
    pointers.current.set(event.pointerId, {
      x: event.clientX, y: event.clientY, startX: event.clientX, startY: event.clientY, pan: !control,
    })
    if (pointers.current.size > 1) {
      suppressClick.current = true
      for (const id of pointers.current.keys()) event.currentTarget.setPointerCapture(id)
      window.getSelection()?.removeAllRanges()
      event.preventDefault()
      event.stopPropagation()
      setDragging(true)
    } else if (!target?.closest('article') && !control) {
      event.currentTarget.setPointerCapture(event.pointerId)
      if (!touch) event.currentTarget.focus({ preventScroll: true })
      event.preventDefault()
    }
  }

  function pointerMove(event: PointerEvent<HTMLDivElement>) {
    const previous = pointers.current.get(event.pointerId)
    if (!previous) return
    const before = [...pointers.current.values()]
    pointers.current.set(event.pointerId, { ...previous, x: event.clientX, y: event.clientY })
    if (before.length === 1 && !suppressClick.current) {
      if (!previous.pan || Math.hypot(event.clientX - previous.startX, event.clientY - previous.startY) <= 5) return
      suppressClick.current = true
      window.getSelection()?.removeAllRanges()
    }
    if (!event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.setPointerCapture(event.pointerId)
    setDragging(true)
    const after = [...pointers.current.values()]
    if (before.length > 1) {
      const distance = Math.hypot(before[1].x - before[0].x, before[1].y - before[0].y)
      const nextDistance = Math.hypot(after[1].x - after[0].x, after[1].y - after[0].y)
      if (distance < 1 || nextDistance < 1) return
      const rect = event.currentTarget.getBoundingClientRect()
      const oldX = (before[0].x + before[1].x) / 2 - rect.left
      const oldY = (before[0].y + before[1].y) / 2 - rect.top
      const newX = (after[0].x + after[1].x) / 2 - rect.left
      const newY = (after[0].y + after[1].y) / 2 - rect.top
      const current = camera.current
      const zoom = Math.max(minBoardMapZoom, Math.min(maxBoardMapZoom, current.zoom * nextDistance / distance))
      const ratio = zoom / current.zoom
      commit({ x: newX - (oldX - current.x) * ratio, y: newY - (oldY - current.y) * ratio, zoom })
    } else {
      commit({ ...camera.current, x: camera.current.x + event.clientX - previous.x, y: camera.current.y + event.clientY - previous.y })
    }
    event.preventDefault()
  }

  function pointerEnd(event: PointerEvent<HTMLDivElement>) {
    if (!pointers.current.delete(event.pointerId)) return
    if (suppressClick.current) {
      event.preventDefault()
      event.stopPropagation()
    }
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    if (!pointers.current.size) setDragging(false)
  }

  function keyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.target !== event.currentTarget || event.ctrlKey || event.metaKey || event.altKey) return
    const step = event.shiftKey ? 120 : 40
    const current = camera.current
    switch (event.key) {
      case 'ArrowLeft': commit({ ...current, x: current.x + step }); break
      case 'ArrowRight': commit({ ...current, x: current.x - step }); break
      case 'ArrowUp': commit({ ...current, y: current.y + step }); break
      case 'ArrowDown': commit({ ...current, y: current.y - step }); break
      case '+': case '=': zoomAt(1.2); break
      case '-': case '_': zoomAt(1 / 1.2); break
      case '0': case 'Home': fit(); break
      default: return
    }
    event.preventDefault()
    event.stopPropagation()
  }

  return (
    <section aria-label="Workspace run map" className="flex min-h-0 min-w-0 flex-1 flex-col">
      <header className="flex flex-wrap items-center gap-2 border-b border-seam px-4 py-2">
        <h2 className="text-ui-sm font-medium">Runs <span className="text-muted tabular-nums">{layout.nodes.length}</span></h2>
        {actions}
        <div role="group" aria-label="Map camera" className="ml-auto flex items-center gap-1">
          <Button variant="ghost" size="icon-sm" label="Zoom out" disabled={viewport.zoom <= minBoardMapZoom} onClick={() => zoomAt(1 / 1.2)}><Minus /></Button>
          <output aria-label="Map zoom" className="min-w-10 text-center text-ui-sm text-muted tabular-nums">{Math.round(viewport.zoom * 100)}%</output>
          <Button variant="ghost" size="icon-sm" label="Zoom in" disabled={viewport.zoom >= maxBoardMapZoom} onClick={() => zoomAt(1.2)}><Plus /></Button>
          <Button variant="ghost" size="sm" onClick={fit} aria-label="Fit map to view">Fit</Button>
        </div>
      </header>
      <p id={instructionsId} className="sr-only">
        Drag the canvas or use the arrow keys to pan. Scroll to pan; Control or Command plus scroll zooms around the pointer.
        On touch screens, drag to pan or pinch to zoom. Plus and minus zoom; Home or zero fits all runs. Tab reaches each run and its controls.
        Needs you includes runs from every workspace, even with Mine selected. Dashed arrows connect different owners.
      </p>
      <div
        ref={canvas}
        data-run-map-canvas
        role="region"
        aria-label="Map canvas"
        aria-describedby={instructionsId}
        tabIndex={0}
        className={cn(focusRing, 'relative min-h-0 min-w-0 flex-1 touch-none overflow-hidden bg-canvas outline-offset-[-2px]', dragging ? 'cursor-grabbing select-none' : 'cursor-grab')}
        onPointerDownCapture={pointerDown}
        onPointerMoveCapture={pointerMove}
        onPointerUpCapture={pointerEnd}
        onPointerCancelCapture={pointerEnd}
        onLostPointerCapture={(event) => { if (event.target === event.currentTarget) pointerEnd(event) }}
        onKeyDown={keyDown}
        onClickCapture={(event) => {
          if (!event.currentTarget.contains(event.target as Node) || event.detail === 0) return
          if (suppressClick.current || window.getSelection()?.isCollapsed === false) {
            event.preventDefault()
            event.stopPropagation()
          }
        }}
        onFocusCapture={(event) => {
          if (event.target === event.currentTarget || !(event.target instanceof Element) || !event.currentTarget.contains(event.target)) return
          if (!event.target.matches(':focus-visible')) return
          const card = event.target.closest('article')
          if (!card) return
          event.currentTarget.scrollLeft = 0
          event.currentTarget.scrollTop = 0
          const bounds = event.currentTarget.getBoundingClientRect()
          const rect = card.getBoundingClientRect()
          const dx = rect.left < bounds.left + 12 ? bounds.left + 12 - rect.left
            : rect.right > bounds.right - 12 ? bounds.right - 12 - rect.right : 0
          const dy = rect.top < bounds.top + 12 ? bounds.top + 12 - rect.top
            : rect.bottom > bounds.bottom - 12 ? bounds.bottom - 12 - rect.bottom : 0
          if (dx || dy) commit({ ...camera.current, x: camera.current.x + dx, y: camera.current.y + dy })
        }}
      >
        {layout.nodes.length === 0 && (empty ?? <p className="p-4 text-ui-sm text-muted">No runs in this view.</p>)}
        <div data-run-map-world className="absolute top-0 left-0 origin-top-left" style={{ width: layout.width, height: layout.height, transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.zoom})` }}>
          {layout.groups.map((group) => (
            <section key={group.key} aria-label={`${group.label}'s runs`} className="absolute rounded-panel border border-seam bg-chrome" style={{ left: group.x, top: group.y, width: group.width, height: group.height }}>
              <header className="flex h-9 min-w-0 items-center gap-2 px-8">
                <Avatar name={group.label} color={group.member?.color} />
                <h3 className="truncate text-ui font-medium">{group.label}</h3>
                <span className="ml-auto text-ui-sm text-muted tabular-nums">{group.count} {group.count === 1 ? 'run' : 'runs'}</span>
              </header>
            </section>
          ))}
          <svg aria-hidden className="pointer-events-none absolute inset-0" width={layout.width} height={layout.height}>
            <defs>
              <marker id={arrowId} viewBox="0 0 8 8" refX="8" refY="4" markerWidth={Math.max(8, 6 / viewport.zoom)} markerHeight={Math.max(8, 6 / viewport.zoom)} orient="auto" markerUnits="userSpaceOnUse">
                <path d="M 0 0 L 8 4 L 0 8 Z" fill="var(--text-muted)" />
              </marker>
            </defs>
            {layout.connectors.map((connector) => (
              <polyline key={connector.to} points={connector.points.map((point) => `${point.x},${point.y}`).join(' ')} fill="none" stroke="var(--text-muted)" strokeWidth={1.25} vectorEffect="non-scaling-stroke" strokeDasharray={connector.crossMember ? '5 3' : undefined} markerEnd={`url(#${arrowId})`} />
            ))}
          </svg>
          {layout.groups.flatMap((group) => group.units.filter((unit) => unit.nodes.some((node) => node.role !== 'standalone')).map((unit) => (
            <div key={`${group.key}:${unit.key}`} className="pointer-events-none absolute truncate text-ui-sm text-muted" style={{ left: unit.x, top: unit.y, width: unit.width }} title={unit.missionId}>
              {unit.label}{unit.missionId && <span className="ml-2 font-code">{unit.missionId}</span>}
            </div>
          )))}
          {layout.nodes.map((node) => {
            const parent = node.parentKey ? nodesByKey.get(node.parentKey) : undefined
            const description = node.role === 'worker'
              ? parent ? `Worker coordinated by ${runLabel(parent.card.run)}` : 'Worker; integrator not visible in this map'
              : node.role === 'integrator' ? 'Swarm integrator' : 'Standalone run'
            return (
              <div key={node.key} role="group" aria-label={description} className="absolute cursor-default rounded-panel bg-canvas" style={{ left: node.x, top: node.y, width: node.width, height: node.height }}>
                {node.role !== 'standalone' && <span className={cn('pointer-events-none absolute -top-5 text-ui-xs font-medium text-muted', node.role === 'worker' ? 'right-0' : 'left-0')}>{node.role === 'integrator' ? 'Integrator' : 'Worker'}</span>}
                <RunCard card={node.card} agentName={agentNames[node.card.run.harness]} variant="map" />
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

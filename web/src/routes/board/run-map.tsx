import { Minus, Plus } from '@/components/icons'
import { useCallback, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, KeyboardEvent, PointerEvent, ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { cn, focusRing } from '@/lib/utils'
import { MapHeader } from '@/routes/board/map-header'
import { layoutRunMap, mapMetrics, runKey, type RunMapLayout } from '@/routes/board/map-layout'
import { MapWorld } from '@/routes/board/map-world'
import type { MapCard } from '@/routes/board/selectors'
import { useMapMotion } from '@/routes/board/use-map-motion'
import { useStore } from '@/store'
import { maxBoardMapZoom, minBoardMapZoom, normalizeBoardMapViewport, type BoardMapViewport } from '@/store/ui'

const controls = 'button:not([data-card-open]), a, input, select, textarea, [role="button"], [contenteditable], [data-slot="card-controls"]'
// Below this fit zoom cards are unreadable, so a first visit starts at 100% on the newest runs instead.
const legibleZoom = 0.5

type MapProps = {
  scope: string
  agentNames: Record<string, string>
  actions?: ReactNode
  empty?: ReactNode
}

export function RunMap({ cards, ...props }: MapProps & { cards: MapCard[] }) {
  const viewerID = useStore((s) => s.info?.member.id ?? null)
  const folded = useStore((s) => s.boardMapFolded)
  const textSize = useStore((s) => s.textSize)
  const coarse = useMediaQuery(coarsePointer)
  const metrics = useMemo(() => mapMetrics(textSize, coarse), [textSize, coarse])
  const previous = useRef<RunMapLayout | undefined>(undefined)
  const layout = useMemo(() => {
    const next = layoutRunMap(cards, { viewerID, folded, metrics }, previous.current)
    previous.current = next
    return next
  }, [cards, viewerID, folded, metrics])
  return <MapViewport key={props.scope} layout={layout} cards={cards} {...props} />
}

function MapViewport({ layout, cards, scope, agentNames, actions, empty }: MapProps & { layout: RunMapLayout; cards: MapCard[] }) {
  const setSavedViewport = useStore((s) => s.setBoardMapViewport)
  const setFolded = useStore((s) => s.setBoardMapFolded)
  const [viewport, setViewport] = useState<BoardMapViewport>(() =>
    normalizeBoardMapViewport(useStore.getState().boardMapViewports[scope]) ?? { x: 0, y: 0, zoom: 1 },
  )
  const camera = useRef(viewport)
  const initialized = useRef(Boolean(normalizeBoardMapViewport(useStore.getState().boardMapViewports[scope])))
  const canvas = useRef<HTMLDivElement>(null)
  const world = useRef<HTMLDivElement>(null)
  const saveFrame = useRef<number | null>(null)
  const dirty = useRef(false)
  const pointers = useRef(new Map<number, { x: number; y: number; startX: number; startY: number; pan: boolean }>())
  const suppressClick = useRef(false)
  const revealing = useRef<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const instructionsId = useId()

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

  const follow = useCallback((dx: number, dy: number) => {
    const current = camera.current
    commit({ ...current, x: current.x - dx * current.zoom, y: current.y - dy * current.zoom })
  }, [commit])
  const { ghosts, fadeGhost, fold } = useMapMotion(world, layout, setFolded, follow)

  const fit = useCallback((first = false) => {
    const element = canvas.current
    const newest = layout.nodes[0]
    if (!element?.clientWidth || !element.clientHeight || !newest) return
    const zoom = Math.max(minBoardMapZoom, Math.min(1,
      (element.clientWidth - 24) / layout.width,
      (element.clientHeight - 24) / layout.height,
    ))
    if (first && zoom < legibleZoom) {
      commit({
        x: Math.min(0, element.clientWidth - 12 - newest.x - newest.width),
        y: Math.min(0, element.clientHeight - 12 - newest.y - newest.height),
        zoom: 1,
      })
      return
    }
    commit({
      x: (element.clientWidth - layout.width * zoom) / 2,
      y: (element.clientHeight - layout.height * zoom) / 2,
      zoom,
    })
  }, [commit, layout.width, layout.height, layout.nodes])

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
    fit(true)
    const observer = new ResizeObserver(() => { if (!initialized.current) fit(true) })
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

  const show = useCallback((key: string) => {
    const node = layout.nodes.find((entry) => entry.key === key)
    const element = canvas.current
    if (!node || !element) return false
    const zoom = camera.current.zoom < legibleZoom ? 1 : camera.current.zoom
    commit({
      x: element.clientWidth / 2 - (node.x + node.width / 2) * zoom,
      y: element.clientHeight / 2 - (node.y + node.height / 2) * zoom,
      zoom,
    })
    const card = [...element.querySelectorAll<HTMLElement>('[data-map-node]')].find((el) => el.dataset.glideKey === `n:${key}`)
    card?.querySelector<HTMLElement>('[data-card-open]')?.focus({ preventScroll: true })
    return true
  }, [commit, layout.nodes])

  // A run in a folded swarm is shown once the unfold has repacked it in.
  useLayoutEffect(() => {
    if (revealing.current && show(revealing.current)) revealing.current = null
  }, [show])

  // A worker always goes through the unfold, which also cancels a fold still fading out.
  const reveal = useCallback((key: string) => {
    const unit = layout.units.find((entry) => (entry.kind === 'swarm' || entry.kind === 'family') && (entry.hidden.some((card) => runKey(card) === key)
      || entry.nodes.some((node) => node.key === key && node.role === 'worker')))
    if (!unit) {
      show(key)
      return
    }
    revealing.current = key
    fold(unit.key, false)
  }, [fold, layout.units, show])

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
    if (event.pointerType === 'mouse' && event.buttons === 0) {
      pointerEnd(event)
      if (!pointers.current.size) suppressClick.current = false
      return
    }
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

  const labelScale = Math.min(8, Math.max(1, 0.9 / viewport.zoom))

  return (
    <section aria-label="Workspace run map" className="flex min-h-0 min-w-0 flex-1 flex-col">
      <MapHeader
        cards={cards}
        actions={actions}
        onReveal={reveal}
        camera={
          <div role="group" aria-label="Map camera" className="flex items-center gap-1">
            <Button variant="ghost" size="icon-sm" label="Zoom out" disabled={viewport.zoom <= minBoardMapZoom} onClick={() => zoomAt(1 / 1.2)}><Minus /></Button>
            <output aria-label="Map zoom" className="min-w-10 text-center text-ui-sm text-muted tabular-nums">{Math.round(viewport.zoom * 100)}%</output>
            <Button variant="ghost" size="icon-sm" label="Zoom in" disabled={viewport.zoom >= maxBoardMapZoom} onClick={() => zoomAt(1.2)}><Plus /></Button>
            <Button variant="ghost" size="sm" onClick={() => fit()} aria-label="Fit map to view">Fit</Button>
          </div>
        }
      />
      <p id={instructionsId} className="sr-only">
        Drag the canvas or use the arrow keys to pan. Scroll to pan; Control or Command plus scroll zooms around the pointer.
        On touch screens, drag to pan or pinch to zoom. Plus and minus zoom; Home or zero fits all runs. Tab reaches each run and its controls.
        Runs read newest first; a swarm's workers sit under its integrator and child runs under the run that started them, whose fold button hides or shows them.
        The needs you and failed counts open lists that bring a run into view. Needs you includes runs from every workspace, even with Mine selected.
      </p>
      <div
        ref={canvas}
        data-run-map-canvas
        role="region"
        aria-label="Map canvas"
        aria-describedby={instructionsId}
        tabIndex={0}
        className={cn(focusRing, 'relative min-h-0 min-w-0 flex-1 touch-none overflow-hidden bg-chrome outline-offset-[-2px]', dragging ? 'cursor-grabbing select-none' : 'cursor-grab')}
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
        <div
          ref={world}
          data-run-map-world
          className="absolute top-0 left-0 origin-top-left"
          style={{
            width: layout.width,
            height: layout.height,
            transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.zoom})`,
            '--map-label-scale': labelScale,
          } as CSSProperties}
        >
          <MapWorld layout={layout} agentNames={agentNames} ghosts={ghosts} onFold={fold} fadeGhost={fadeGhost} />
        </div>
      </div>
    </section>
  )
}

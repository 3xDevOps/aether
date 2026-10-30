import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { Popover, PopoverAnchor } from '@/components/ui/popover'
import { clockTime, elapsed } from '@/lib/format'
import { useKeybindings } from '@/lib/keybindings'
import { playEntrance, play } from '@/lib/motion'
import { prefersReducedMotion } from '@/lib/hooks'
import { stateWords } from '@/lib/status'
import { cn, focusRingInset } from '@/lib/utils'
import { ReplyComposer, cardAction } from '@/routes/board/card-action'
import { ticks, xOf } from '@/routes/board/lanes/axis'
import { EdgeHits, EdgeStrokes, drawEdges, edgeShape, type EdgeAnchor } from '@/routes/board/lanes/connectors'
import { laneEdges, type EdgesInput, type LaneEdge } from '@/routes/board/lanes/edges'
import { EdgePopover, FamilyPopover, LaneLinks, LanePopoverContent, RunPopover } from '@/routes/board/lanes/lane-popover'
import { LaneRow, trackGutter, type LaneGeometry } from '@/routes/board/lanes/lane-row'
import { laneHeight, layoutLanes, type Lane, type LanesModel, type RunLane } from '@/routes/board/lanes/model'
import { agentStateWord, openAgent } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'

/** Past this many lanes only those near the viewport render. */
export const windowAbove = 60
const axisHeight = 28
const overscan = 400
/** Half the "now" chip and a little air: tick labels this close to it would sit under it. */
const nowClear = 48
const onlyOverlaps = new Set(['message'] as const)

type Pop =
  | { kind: 'lane'; key: string; run?: string; x: number; y: number; h: number; at?: number; reply?: boolean }
  | { kind: 'edge'; edge: LaneEdge; x: number; y: number }

function treeLabel(lane: Lane, now: number): string {
  switch (lane.kind) {
    case 'section':
      return [`${lane.label}, ${lane.count} ${lane.count === 1 ? 'run' : 'runs'}`, lane.before > 0 && `${lane.before} ended before this window`].filter(Boolean).join(', ')
    case 'summary':
      return lane.label
    case 'family':
      return `Run ${lane.title}, ${lane.detail}`
    case 'run': {
      const { card } = lane
      const words = stateWords(card, undefined, now)
      return [lane.title, words.label, words.detail, `started ${clockTime(card.run.created_at, now)}`, elapsed(card.run.created_at, lane.end), card.owner?.display_name]
        .filter(Boolean).join(', ')
    }
    case 'agent':
      return `${lane.label}, ${agentStateWord(lane.state)}`
    case 'note':
      return lane.text
  }
}

function expandedOf(lane: Lane): boolean | undefined {
  if (lane.kind === 'section' || lane.kind === 'family') return lane.open
  if (lane.kind === 'summary') return false
  if (lane.kind === 'run') return lane.agentsOpen
  return undefined
}

function LaneKeys({ onOpen, onReply }: { onOpen: () => void; onReply?: () => void }) {
  useKeybindings('card', { 'card-open': onOpen, ...(onReply && { 'card-reply': onReply }) })
  return null
}

export interface LanesViewProps {
  model: LanesModel
  edges: LaneEdge[]
  edgeInput: Omit<EdgesInput, 'focus' | 'hidden'>
  overlapsShown: boolean
  g: LaneGeometry
  now: number
  agentNames: Record<string, string>
  widen?: string
  onWiden: () => void
  onToggle: (key: string, open: boolean) => void
  onAgents: (runID: string, open: boolean) => void
  onRetryAgents: (runID: string) => void
  canShowAgents: (lane: RunLane) => boolean
  onPan: (ms: number) => void
  onZoom: (factor: number, anchor: number) => void
  onNow: () => void
}

export function LanesView(props: LanesViewProps) {
  const { model, g, now } = props
  const scroller = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)
  const [viewport, setViewport] = useState({ top: 0, height: 0 })
  const [activeKey, setActiveKey] = useState<string | null>(null)
  const [hovered, setHovered] = useState<string | null>(null)
  const [treeFocused, setTreeFocused] = useState(false)
  const [pop, setPop] = useState<Pop | null>(null)
  const popRef = useRef(pop)
  popRef.current = pop
  const hintID = useId()
  const navigate = useStore((s) => s.navigate)

  const layout = useMemo(() => layoutLanes(model.lanes, g.metrics), [model.lanes, g.metrics])
  const byKey = useMemo(() => new Map(model.lanes.map((lane) => [lane.key, lane])), [model.lanes])
  const active = activeKey && byKey.has(activeKey) ? activeKey : model.lanes[0]?.key
  const activeIndex = model.lanes.findIndex((lane) => lane.key === active)

  const tree = useMemo(() => {
    const size = new Map<string | undefined, number>()
    const out = new Map<string, { level: number; posinset: number; setsize: number; expanded?: boolean; section: string }>()
    for (const lane of model.lanes) {
      const posinset = (size.get(lane.parentKey) ?? 0) + 1
      size.set(lane.parentKey, posinset)
      const parent = lane.parentKey ? out.get(lane.parentKey) : undefined
      out.set(lane.key, { level: lane.level, posinset, setsize: 0, expanded: expandedOf(lane), section: parent?.section ?? lane.key })
    }
    for (const lane of model.lanes) out.get(lane.key)!.setsize = size.get(lane.parentKey)!
    return out
  }, [model.lanes])

  const anchors = useMemo(() => {
    const out = new Map<string, EdgeAnchor>()
    for (const lane of model.lanes) {
      const height = laneHeight(lane, g.metrics)
      out.set(lane.key, { top: layout.top.get(lane.key)!, height, half: lane.kind === 'run' ? g.metrics.bar / 2 : 8 })
    }
    return out
  }, [model.lanes, layout, g.metrics])

  const popLane = pop?.kind === 'lane' ? byKey.get(pop.key) : undefined
  const focusKey = hovered ?? popLane?.key ?? (treeFocused ? active : undefined)
  const focusLane = focusKey ? byKey.get(focusKey) : undefined
  const focusRun = focusLane?.kind === 'run' ? focusLane.card.run.id : undefined
  const overlaps = useMemo(
    () => (props.overlapsShown && focusRun ? laneEdges({ ...props.edgeInput, focus: focusRun, hidden: onlyOverlaps }) : []),
    [props.overlapsShown, props.edgeInput, focusRun],
  )
  const edges = useMemo(() => [...props.edges, ...overlaps], [props.edges, overlaps])

  const windowed = model.lanes.length > windowAbove
  const shown = windowed
    ? model.lanes.filter((lane, i) => {
        const top = layout.top.get(lane.key)!
        return i === activeIndex || (top + laneHeight(lane, g.metrics) >= viewport.top - axisHeight - overscan && top <= viewport.top + viewport.height + overscan)
      })
    : model.lanes
  const edgeView = windowed ? viewport : null
  const shapes = useMemo(() => {
    const shownEdges = edgeView
      ? edges.filter((edge) => {
          const a = anchors.get(edge.from.lane)?.top ?? 0
          const b = anchors.get(edge.to.lane)?.top ?? 0
          return Math.max(a, b) >= edgeView.top - overscan && Math.min(a, b) <= edgeView.top + edgeView.height + overscan
        })
      : edges
    return drawEdges(shownEdges, anchors, g, focusKey)
  }, [edgeView, edges, anchors, g, focusKey])

  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    let frame = 0
    const read = () => {
      frame = 0
      setViewport((v) => (v.top === el.scrollTop && v.height === el.clientHeight ? v : { top: el.scrollTop, height: el.clientHeight }))
    }
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(read)
    }
    read()
    el.addEventListener('scroll', schedule, { passive: true })
    const observer = new ResizeObserver(schedule)
    observer.observe(el)
    return () => {
      cancelAnimationFrame(frame)
      el.removeEventListener('scroll', schedule)
      observer.disconnect()
    }
  }, [])

  // Pan and zoom: Shift-wheel or a sideways wheel pans, Ctrl or Cmd zooms around the pointer; vertical wheel scrolls lanes.
  const latest = useRef(props)
  latest.current = props
  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const wheel = (event: WheelEvent) => {
      const { g, onPan, onZoom } = latest.current
      const span = g.window.end - g.window.start
      const unit = event.deltaMode === 1 ? 16 : 1
      const rect = el.getBoundingClientRect()
      const x = event.clientX - rect.left - g.labelWidth
      if (event.ctrlKey || event.metaKey) {
        event.preventDefault()
        onZoom(Math.exp(event.deltaY * unit * 0.002), g.window.start + (Math.min(g.width, Math.max(0, x)) / g.width) * span)
        return
      }
      const sideways = event.shiftKey && !event.deltaX ? event.deltaY : Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : 0
      if (!sideways) return
      event.preventDefault()
      onPan((sideways * unit * span) / g.width)
    }
    el.addEventListener('wheel', wheel, { passive: false })
    return () => el.removeEventListener('wheel', wheel)
  }, [])

  const drag = useRef<{ x: number; id: number; moved: boolean } | null>(null)
  const pointerDown = (event: React.PointerEvent) => {
    if (event.button !== 0 || !(event.target as Element).closest('[data-lane-track]')) return
    drag.current = { x: event.clientX, id: event.pointerId, moved: false }
  }
  // Hover comes from the pointer's y, not row events: link hit paths above the rows would otherwise end a row's hover.
  const hoverAt = (event: React.PointerEvent<HTMLDivElement>) => {
    const y = event.clientY - event.currentTarget.getBoundingClientRect().top
    if (y < 0 || y >= layout.height || model.lanes.length === 0) return setHovered(null)
    let lo = 0
    let hi = model.lanes.length - 1
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1
      if (layout.top.get(model.lanes[mid].key)! <= y) lo = mid
      else hi = mid - 1
    }
    setHovered(model.lanes[lo].key)
  }
  const pointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    hoverAt(event)
    const d = drag.current
    if (!d || d.id !== event.pointerId) return
    const dx = event.clientX - d.x
    if (!d.moved && Math.abs(dx) < 6) return
    if (!d.moved) event.currentTarget.setPointerCapture(event.pointerId)
    d.moved = true
    d.x = event.clientX
    props.onPan((-dx * (g.window.end - g.window.start)) / g.width)
  }
  const pointerUp = () => {
    if (drag.current?.moved) setTimeout(() => { drag.current = null })
    else drag.current = null
  }

  // Lanes glide only when they change section; lanes new since the first render fade in.
  const before = useRef<{ top: Map<string, number>; section: Map<string, string> } | null>(null)
  useLayoutEffect(() => {
    const previous = before.current
    before.current = { top: layout.top, section: new Map([...tree].map(([key, t]) => [key, t.section])) }
    if (!previous || windowed || prefersReducedMotion() || !content.current) return
    let entering = 0
    for (const el of content.current.querySelectorAll<HTMLElement>('[data-glide-key]')) {
      const key = el.dataset.glideKey!
      const was = previous.top.get(key)
      if (was === undefined) {
        playEntrance(el, { motion: { duration: '--duration-250', easing: '--ease-enter' }, index: entering++ })
        continue
      }
      const moved = was - layout.top.get(key)!
      if (moved && previous.section.get(key) !== tree.get(key)?.section) {
        play(el, [{ translate: `0 ${moved}px` }, { translate: '0 0' }], { duration: '--duration-220', easing: '--ease-enter' }, { composite: 'add' })
      }
    }
  }, [layout, tree, windowed])

  const focusLaneEl = useCallback((key: string) => {
    const top = layout.top.get(key)
    const el = scroller.current
    if (top !== undefined && el) {
      const lane = byKey.get(key)!
      const bottom = top + laneHeight(lane, g.metrics)
      if (top < el.scrollTop) el.scrollTop = top
      else if (bottom > el.scrollTop + el.clientHeight - axisHeight) el.scrollTop = bottom - el.clientHeight + axisHeight
    }
    requestAnimationFrame(() => [...(content.current?.querySelectorAll<HTMLElement>('[role=treeitem]') ?? [])].find((el) => el.dataset.key === key)?.focus())
  }, [layout, byKey, g.metrics])

  const moveTo = (index: number) => {
    const lane = model.lanes[Math.max(0, Math.min(model.lanes.length - 1, index))]
    if (!lane) return
    setActiveKey(lane.key)
    focusLaneEl(lane.key)
  }

  const openLane = (lane: Lane, x?: number, reply = false) => {
    const top = layout.top.get(lane.key)!
    const height = laneHeight(lane, g.metrics)
    const bar = lane.kind === 'run' ? g.metrics.bar : height
    const end = lane.kind === 'run' ? Math.min(g.width, Math.max(0, xOf(lane.end, g.window, g.width))) : g.width / 2
    const at = x === undefined ? undefined : g.window.start + (x / g.width) * (g.window.end - g.window.start)
    setPop({ kind: 'lane', key: lane.key, run: lane.kind === 'run' ? lane.card.run.id : undefined, x: x ?? end, y: top + (height - bar) / 2, h: bar, at, reply })
  }

  const openEdge = (edge: LaneEdge) => {
    const { mid } = edgeShape(edge, anchors.get(edge.from.lane)!, anchors.get(edge.to.lane)!, g)
    setPop({ kind: 'edge', edge, x: mid.x, y: mid.y })
  }

  const toggle = (lane: Lane, open: boolean) => {
    if (lane.kind === 'section' || lane.kind === 'family') props.onToggle(lane.key, open)
    else if (lane.kind === 'summary') props.onToggle(lane.parentKey!, true)
    else if (lane.kind === 'run' && (lane.agentsOpen !== undefined || props.canShowAgents(lane))) props.onAgents(lane.card.run.id, open)
  }

  const activate = (lane: Lane, x?: number, chevron = false) => {
    setActiveKey(lane.key)
    switch (lane.kind) {
      case 'section':
      case 'summary':
        toggle(lane, lane.kind === 'summary' || !lane.open)
        break
      case 'family':
        if (chevron || x === undefined) toggle(lane, !lane.open)
        else openLane(lane, x)
        break
      case 'run':
        if (chevron) toggle(lane, !lane.agentsOpen)
        else openLane(lane, x)
        break
      case 'agent':
        openAgent(lane.runID, lane.agentID)
        break
      case 'note':
        if (lane.retry) props.onRetryAgents(lane.runID)
    }
  }

  const onPick = useCallback((lane: Lane, x: number | undefined, chevron: boolean) => {
    if (drag.current?.moved) return
    latestActivate.current(lane, x, chevron)
  }, [])
  const latestActivate = useRef(activate)
  latestActivate.current = activate
  const onRetry = useCallback((runID: string) => latest.current.onRetryAgents(runID), [])
  const onWiden = useCallback(() => latest.current.onWiden(), [])
  const pickEdge = useCallback((edge: LaneEdge, x: number, y: number) => {
    if (!drag.current?.moved) setPop({ kind: 'edge', edge, x, y })
  }, [])
  // Swapping the lane popover for the reply composer unmounts its content too; only a real close returns focus.
  const returnTo = (key: string | undefined) => () => {
    if (!popRef.current && key) focusLaneEl(key)
  }

  // A live update can remove the selected lane; its popover closes and focus goes to the lane that draws its run now.
  const gone = pop?.kind === 'lane' && !popLane ? pop : undefined
  useEffect(() => {
    if (!gone) return
    setPop(null)
    const key = (gone.run && model.drawn.get(gone.run)) || active
    if (!key) return
    setActiveKey(key)
    focusLaneEl(key)
  }, [gone])

  const keyDown = (event: React.KeyboardEvent) => {
    const target = event.target as HTMLElement
    if (target.getAttribute('role') !== 'treeitem') return
    const lane = byKey.get(target.dataset.key ?? '')
    if (!lane) return
    const index = model.lanes.indexOf(lane)
    const expanded = expandedOf(lane)
    switch (event.key) {
      case 'ArrowDown': moveTo(index + 1); break
      case 'ArrowUp': moveTo(index - 1); break
      case 'Home': moveTo(0); break
      case 'End': moveTo(model.lanes.length - 1); break
      case 'ArrowRight':
        if (expanded === false || (lane.kind === 'run' && expanded === undefined && props.canShowAgents(lane))) toggle(lane, true)
        else if (expanded) moveTo(index + 1)
        break
      case 'ArrowLeft':
        if (expanded && lane.kind !== 'summary') toggle(lane, false)
        else if (lane.parentKey) moveTo(model.lanes.findIndex((item) => item.key === lane.parentKey))
        break
      case 'Enter':
      case ' ':
        activate(lane)
        break
      default:
        return
    }
    event.preventDefault()
  }

  const axisKeyDown = (event: React.KeyboardEvent) => {
    const span = g.window.end - g.window.start
    switch (event.key) {
      case 'ArrowLeft': props.onPan(-span / 10); break
      case 'ArrowRight': props.onPan(span / 10); break
      case '+': case '=': props.onZoom(1 / 1.5, g.window.end - span / 2); break
      case '-': case '_': props.onZoom(1.5, g.window.end - span / 2); break
      case '0': case 'Home': props.onNow(); break
      default: return
    }
    event.preventDefault()
  }

  const nowX = xOf(now, g.window, g.width)
  const nowShown = nowX >= 0 && nowX <= g.width
  const hoveredLane = hovered ? byKey.get(hovered) : undefined
  const highlights = [
    hoveredLane && hoveredLane.kind !== 'section' && hoveredLane.key !== popLane?.key && { key: hoveredLane.key, className: 'bg-hover' },
    popLane && { key: popLane.key, className: 'bg-selection' },
  ].filter((item): item is { key: string; className: string } => Boolean(item))
  const tickMarks = ticks(g.window, now)
  const activeLane = active ? byKey.get(active) : undefined
  const popRun = popLane?.kind === 'run' ? popLane : undefined
  const totalWidth = g.labelWidth + g.width + trackGutter

  return (
    <div
      ref={scroller}
      role="region"
      aria-label="Run lanes"
      className="relative min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto bg-canvas"
    >
      <div className="sticky top-0 z-20 flex border-b border-seam bg-canvas" style={{ height: axisHeight, width: totalWidth }}>
        <div className="shrink-0" style={{ width: g.labelWidth }} />
        <div
          tabIndex={0}
          role="group"
          aria-label="Timeline window"
          aria-describedby={hintID}
          onKeyDown={axisKeyDown}
          className={cn(focusRingInset, 'relative shrink-0 outline-none')}
          style={{ width: g.width + trackGutter }}
        >
          <span id={hintID} className="sr-only">Left and right arrows move through time, plus and minus zoom, and zero returns to now.</span>
          {tickMarks.map((tick) => {
            const x = xOf(tick.t, g.window, g.width)
            if (nowShown && Math.abs(x - nowX) < nowClear) return null
            return (
              <span key={tick.t} aria-hidden className="absolute top-1/2 -translate-x-1/2 -translate-y-1/2 text-ui-xs whitespace-nowrap text-muted tabular-nums" style={{ left: x }}>
                {tick.label}
              </span>
            )
          })}
          {nowShown && (
            <span aria-hidden className="absolute top-1/2 -translate-x-1/2 -translate-y-1/2 rounded-control border border-seam bg-raised px-1.5 text-ui-xs" style={{ left: nowX }}>now</span>
          )}
        </div>
      </div>
      <div
        ref={content}
        className="relative touch-pan-y"
        style={{ height: layout.height, width: totalWidth }}
        onPointerDownCapture={pointerDown}
        onPointerMove={pointerMove}
        onPointerLeave={() => setHovered(null)}
        onPointerUp={pointerUp}
        onPointerCancel={pointerUp}
      >
        {highlights.map((item) => (
          <div key={item.key} aria-hidden className={cn('pointer-events-none absolute inset-x-0', item.className)} style={{ top: layout.top.get(item.key), height: laneHeight(byKey.get(item.key)!, g.metrics) }} />
        ))}
        <div aria-hidden className="pointer-events-none absolute inset-y-0" style={{ left: g.labelWidth, width: g.width }}>
          {tickMarks.map((tick) => (
            <span key={tick.t} className="absolute inset-y-0 border-l border-dashed border-seam" style={{ left: xOf(tick.t, g.window, g.width) }} />
          ))}
          {nowShown && <span className="absolute inset-y-0 w-px bg-icon-faint" style={{ left: nowX }} />}
        </div>
        <EdgeStrokes shapes={shapes} g={g} height={layout.height} />
        <div
          role="tree"
          aria-label="Run lanes"
          onKeyDown={keyDown}
          onFocus={(event) => {
            setTreeFocused(true)
            const key = (event.target as HTMLElement).dataset.key
            if (key) setActiveKey(key)
          }}
          onBlur={(event) => {
            if (!(event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget))) setTreeFocused(false)
          }}
        >
          {shown.map((lane) => (
            <LaneRow
              key={lane.key}
              lane={lane}
              top={layout.top.get(lane.key)!}
              height={laneHeight(lane, g.metrics)}
              g={g}
              now={now}
              tree={tree.get(lane.key)!}
              name={treeLabel(lane, now)}
              active={lane.key === active}
              selected={lane.key === popLane?.key}
              agentName={lane.kind === 'run' ? props.agentNames[lane.card.run.harness] : undefined}
              widen={lane.kind === 'section' ? props.widen : undefined}
              onPick={onPick}
              onWiden={onWiden}
              onRetry={onRetry}
            />
          ))}
        </div>
        <EdgeHits shapes={shapes} g={g} height={layout.height} onPick={pickEdge} />
        <Popover open={pop !== null} onOpenChange={(open) => { if (!open) setPop(null) }}>
          {pop && (
            <PopoverAnchor asChild>
              <span aria-hidden className="pointer-events-none absolute w-px" style={{ left: g.labelWidth + pop.x, top: pop.y, height: pop.kind === 'lane' ? pop.h : 0 }} />
            </PopoverAnchor>
          )}
          {pop?.kind === 'lane' && popRun && pop.reply ? (
            <ReplyComposer card={popRun.card} title={popRun.title} onDone={() => setPop(null)} returnFocus={returnTo(popRun.key)} />
          ) : pop?.kind === 'lane' && popLane ? (
            <LanePopoverContent label={treeLabel(popLane, now)} returnFocus={returnTo(popLane.key)}>
              {popLane.kind === 'run' && (
                <RunPopover
                  lane={popLane}
                  at={pop.at}
                  agentName={props.agentNames[popLane.card.run.harness]}
                  now={now}
                  onReply={() => setPop({ ...pop, reply: true })}
                  onAgents={popLane.agentsOpen !== undefined || props.canShowAgents(popLane) ? (open) => { props.onAgents(popLane.card.run.id, open); setPop(null) } : undefined}
                />
              )}
              {popLane.kind === 'family' && <FamilyPopover lane={popLane} />}
              <LaneLinks edges={edges.filter((edge) => edge.from.lane === popLane.key || edge.to.lane === popLane.key)} now={now} onPick={openEdge} />
            </LanePopoverContent>
          ) : pop?.kind === 'edge' ? (
            <LanePopoverContent label="Link" returnFocus={returnTo(active)}>
              <EdgePopover edge={pop.edge} now={now} />
            </LanePopoverContent>
          ) : null}
        </Popover>
      </div>
      {treeFocused && activeLane?.kind === 'run' && (
        <LaneKeys
          onOpen={() => navigate('run', { runId: activeLane.card.run.id })}
          onReply={cardAction(activeLane.card)?.kind === 'reply' ? () => openLane(activeLane, undefined, true) : undefined}
        />
      )}
    </div>
  )
}

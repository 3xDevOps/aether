import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { Popover, PopoverAnchor } from '@/components/ui/popover'
import { clockTime, elapsed } from '@/lib/format'
import { useKeybindings } from '@/lib/keybindings'
import { playEntrance, play } from '@/lib/motion'
import { prefersReducedMotion } from '@/lib/hooks'
import { runLabel, stateWords } from '@/lib/status'
import { cn, focusRingInset } from '@/lib/utils'
import { ReplyComposer, cardAction } from '@/routes/board/card-action'
import { type Tick, ticks, xOf } from '@/routes/board/lanes/axis'
import { EdgeHits, EdgeStrokes, Hairlines, drawEdges, edgeShape, type EdgeAnchor } from '@/routes/board/lanes/connectors'
import { laneEdges, type EdgesInput, type LaneEdge } from '@/routes/board/lanes/edges'
import { EdgePopover, FamilyPopover, LaneConversations, LaneLinks, LanePopoverContent, RunPopover } from '@/routes/board/lanes/lane-popover'
import { LaneRow, trackGutter, type LaneGeometry } from '@/routes/board/lanes/lane-row'
import { LiveMail } from '@/routes/board/lanes/live-mail'
import { bundleWords, conversationTag, conversationWords, conversationsByLane, mailMarks, type Conversation } from '@/routes/board/lanes/mail'
import { MailMarks, OffscreenMail } from '@/routes/board/lanes/mail-marks'
import { laneHeight, layoutLanes, type Lane, type LanesModel, type RunLane } from '@/routes/board/lanes/model'
import { agentStateWord, openAgent } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'

/** Past this many lanes only those near the viewport render. */
const windowAbove = 60
const axisHeight = 28
const overscan = 400
/** Half the "now" chip and a little air: tick labels this close to it would sit under it. */
const nowClear = 48
const onlyOverlaps = new Set(['message'] as const)

type Pop =
  | { kind: 'lane'; key: string; run?: string; x: number; y: number; h: number; at?: number; reply?: boolean }
  | { kind: 'edge'; edges: LaneEdge[]; x: number; y: number; focus?: string }

/** How long the pointer may rest, or move without closing in, while heading for a pinned button before the lane under it takes the hover. */
const aimMs = 150
/** The longest the lanes crossed on the way to a pinned button wait. */
const aimMaxMs = 1500

function side(a: { x: number; y: number }, b: { x: number; y: number }, p: { x: number; y: number }): number {
  return (b.x - a.x) * (p.y - a.y) - (b.y - a.y) * (p.x - a.x)
}

function inTriangle(p: { x: number; y: number }, a: { x: number; y: number }, b: { x: number; y: number }, c: { x: number; y: number }): boolean {
  const d1 = side(a, b, p)
  const d2 = side(b, c, p)
  const d3 = side(c, a, p)
  return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0))
}

/** An estimate of one axis character at text-ui-xs; tabular digits keep it close. */
const tickCharWidth = 7

function spaced(marks: Tick[], g: LaneGeometry): Tick[] {
  let right = -Infinity
  return marks.filter((tick) => {
    const x = xOf(tick.t, g.window, g.width)
    const half = (tick.label.length * tickCharWidth) / 2
    if (x - half < right + 8) return false
    right = x + half
    return true
  })
}

function treeLabel(lane: Lane, now: number): string {
  switch (lane.kind) {
    case 'section':
      return [`${lane.label}, ${lane.count} ${lane.count === 1 ? 'session' : 'sessions'}`, lane.before > 0 && `${lane.before} ended before this window`].filter(Boolean).join(', ')
    case 'summary':
      return lane.label
    case 'family':
      return `Session ${lane.title}, ${lane.detail}`
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

interface LanesViewProps {
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
  // An open popover keeps the lane it opened from in focus, wherever the pointer goes on its way to it.
  const focusKey = popLane?.key ?? (pop?.kind === 'edge' && pop.focus && byKey.has(pop.focus) ? pop.focus : undefined) ?? hovered ?? (treeFocused ? active : undefined)
  const focusRef = useRef(focusKey)
  focusRef.current = focusKey
  const focusLane = focusKey ? byKey.get(focusKey) : undefined
  const focusRun = focusLane?.kind === 'run' ? focusLane.card.run.id : undefined
  const overlaps = useMemo(
    () => (props.overlapsShown && focusRun ? laneEdges({ ...props.edgeInput, focus: focusRun, hidden: onlyOverlaps }) : []),
    [props.overlapsShown, props.edgeInput, focusRun],
  )
  const edges = useMemo(() => [...props.edges, ...overlaps], [props.edges, overlaps])
  const talks = useMemo(() => conversationsByLane(props.edges), [props.edges])
  const nameOf = useCallback((c: { lane: string; run: string }) => {
    const lane = byKey.get(c.lane)
    if (lane?.kind === 'run') return lane.title
    const run = useStore.getState().runs[c.run]
    return run ? runLabel(run) : c.run
  }, [byKey])
  const talkWords = useMemo(() => new Map([...talks].map(([lane, list]) => [lane, conversationWords(list, nameOf)])), [talks, nameOf])

  const windowed = model.lanes.length > windowAbove
  const shown = windowed
    ? model.lanes.filter((lane, i) => {
        const top = layout.top.get(lane.key)!
        return i === activeIndex || (top + laneHeight(lane, g.metrics) >= viewport.top - axisHeight - overscan && top <= viewport.top + viewport.height + overscan)
      })
    : model.lanes
  const edgeView = windowed ? viewport : null
  const picked = useMemo(() => new Set(pop?.kind === 'edge' ? pop.edges.map((edge) => edge.key) : []), [pop])
  const linked = useCallback(
    (edge: LaneEdge) => edge.kind !== 'message' || (Boolean(edge.from.lane && edge.to.lane) && (picked.has(edge.key) || (focusKey !== undefined && (edge.from.lane === focusKey || edge.to.lane === focusKey)))),
    [picked, focusKey],
  )
  const inView = useCallback((edge: LaneEdge) => {
    if (!edgeView) return true
    const a = anchors.get(edge.from.lane)?.top ?? 0
    const b = anchors.get(edge.to.lane)?.top ?? 0
    return Math.max(a, b) >= edgeView.top - overscan && Math.min(a, b) <= edgeView.top + edgeView.height + overscan
  }, [edgeView, anchors])
  const shapes = useMemo(() => drawEdges(edges.filter((edge) => inView(edge) && linked(edge)), anchors, g, focusKey), [edges, inView, anchors, g, focusKey, linked])
  const hairlines = useMemo(() => props.edges.filter(inView), [props.edges, inView])
  const order = useMemo(() => new Map(model.lanes.map((lane, i) => [lane.key, i])), [model.lanes])
  const shownKeys = windowed ? shown.map((lane) => lane.key).join('\n') : ''
  const marks = useMemo(() => {
    const all = mailMarks(props.edges, order, (t) => xOf(t, g.window, g.width))
    if (!shownKeys) return all
    const keep = new Set(shownKeys.split('\n'))
    return all.filter((mark) => keep.has(mark.lane))
  }, [props.edges, order, g.window, g.width, shownKeys])
  const edgeTitle = useCallback((edge: LaneEdge) => (edge.messages ? bundleWords(edge, nameOf, clockTime(new Date(edge.from.t).toISOString(), now)) : ''), [nameOf, now])
  const focusTalks = useMemo(() => (focusKey ? talks.get(focusKey) ?? [] : []), [focusKey, talks])
  const chips = useMemo(() => {
    const out = new Map<string, string>()
    if (!focusKey || focusTalks.length === 0) return out
    out.set(focusKey, `↔ ${focusTalks.length}`)
    const byLane = new Map<string, { sent: number; received: number }>()
    for (const c of focusTalks) {
      if (!c.lane) continue
      const sum = byLane.get(c.lane) ?? { sent: 0, received: 0 }
      byLane.set(c.lane, { sent: sum.sent + c.sent, received: sum.received + c.received })
    }
    for (const [lane, sum] of byLane) out.set(lane, conversationTag(sum))
    return out
  }, [focusKey, focusTalks])

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
  const laneAt = (y: number): string | null => {
    if (y < 0 || y >= layout.height || model.lanes.length === 0) return null
    let lo = 0
    let hi = model.lanes.length - 1
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1
      if (layout.top.get(model.lanes[mid].key)! <= y) lo = mid
      else hi = mid - 1
    }
    return model.lanes[lo].key
  }
  const latestLaneAt = useRef(laneAt)
  latestLaneAt.current = laneAt
  const pointer = useRef<{ x: number; y: number } | null>(null)
  /** Where the pointer last was on the hovered lane. */
  const left = useRef<{ x: number; y: number } | null>(null)
  /** Travel towards a pinned button: when it began and the closest the pointer has come. */
  const aim = useRef<{ since: number; gap: number } | null>(null)
  const settle = useRef<number | undefined>(undefined)
  const onControl = useRef(false)
  useEffect(() => () => clearTimeout(settle.current), [])
  const hover = (key: string | null) => {
    clearTimeout(settle.current)
    aim.current = null
    left.current = pointer.current
    setHovered(key)
  }
  const hoverAt = (event: React.PointerEvent<HTMLDivElement>) => {
    const at = { x: event.clientX, y: event.clientY }
    pointer.current = at
    const next = laneAt(at.y - event.currentTarget.getBoundingClientRect().top)
    // On its way to the hovered lane's pinned button the pointer crosses other lanes. They wait while it keeps closing in, for at most aimMaxMs.
    const from = left.current
    const buttons = !popRef.current && hovered && next !== hovered && from ? content.current?.querySelectorAll('[data-mail-pinned]') ?? [] : []
    let heading = false
    let gap = Infinity
    for (const el of buttons) {
      const r = el.getBoundingClientRect()
      const [tl, tr, bl, br] = [{ x: r.left, y: r.top }, { x: r.right, y: r.top }, { x: r.left, y: r.bottom }, { x: r.right, y: r.bottom }]
      // The two triangles from where the pointer left the lane to the button's diagonals cover the cone towards it.
      if (!inTriangle(at, from!, tl, br) && !inTriangle(at, from!, tr, bl)) continue
      heading = true
      gap = Math.min(gap, Math.hypot(Math.max(r.left - at.x, 0, at.x - r.right), Math.max(r.top - at.y, 0, at.y - r.bottom)))
    }
    if (heading) {
      aim.current ??= { since: Date.now(), gap: Infinity }
      if (Date.now() - aim.current.since < aimMaxMs) {
        if (gap < aim.current.gap) {
          aim.current.gap = gap
          clearTimeout(settle.current)
          settle.current = window.setTimeout(() => {
            const el = content.current
            if (el && pointer.current) hover(latestLaneAt.current(pointer.current.y - el.getBoundingClientRect().top))
          }, aimMs)
        }
        return
      }
    }
    hover(next)
  }
  const pointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const target = event.target as Element
    pointer.current = { x: event.clientX, y: event.clientY }
    onControl.current = Boolean(target.closest('[data-mail-control]'))
    if (onControl.current) {
      clearTimeout(settle.current)
      aim.current = null
    }
    // React bubbles the portalled popover's events here: the pointer is on the popover, not on the lane under it.
    else if (!event.currentTarget.contains(target)) {
      pointer.current = null
      hover(null)
    }
    else hoverAt(event)
    const d = drag.current
    if (!d || d.id !== event.pointerId) return
    const dx = event.clientX - d.x
    if (!d.moved && Math.abs(dx) < 6) return
    if (!d.moved) {
      event.currentTarget.setPointerCapture(event.pointerId)
      setPop(null)
    }
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
    const el = scroller.current
    // Lanes are placed absolutely, so the browser cannot anchor the scroll: the lane at the top of the view stays put when lanes above it come or go.
    // At the bottom of shorter content the browser has already clamped scrollTop; the offset before the change is the one to correct.
    const clamped = el !== null && el.scrollTop < viewport.top && el.scrollTop >= el.scrollHeight - el.clientHeight - 1
    const scroll = clamped ? viewport.top : el?.scrollTop ?? 0
    let moved = 0
    if (previous && el && scroll > 0) {
      // The lane at the top of the view, as a browser would anchor; if it left, the first lane that started in view.
      let top: [string, number] | undefined
      let below: [string, number] | undefined
      for (const [key, y] of previous.top) {
        if (y <= scroll && (!top || y > top[1])) top = [key, y]
        if (y > scroll && layout.top.has(key) && (!below || y < below[1])) below = [key, y]
      }
      const anchor = top && layout.top.has(top[0]) ? top : below
      moved = anchor ? layout.top.get(anchor[0])! - anchor[1] : 0
      if (moved || clamped) el.scrollTop = scroll + moved
    }
    // A pointer at rest hovers the lane now under it once the lanes move; on a mark, link or pinned button it keeps the lane that drew it.
    const shifted = hovered !== null && (moved !== 0 || previous?.top.get(hovered) !== layout.top.get(hovered))
    // A scroll correction can move a pinned button before it re-renders, so a press still on a control keeps its lane while that lane exists.
    const control = (onControl.current && hovered !== null && layout.top.has(hovered)) || (pointer.current && document.elementFromPoint(pointer.current.x, pointer.current.y)?.closest('[data-mail-control]'))
    if (shifted && pointer.current && content.current && !aim.current && !control) {
      const key = latestLaneAt.current(pointer.current.y - content.current.getBoundingClientRect().top)
      if (key !== hovered) hover(key)
    }
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
    // Mail with a session outside the view has one drawn end; its facts open there.
    const a = anchors.get(edge.from.lane) ?? anchors.get(edge.to.lane)!
    const { mid } = edgeShape(edge, a, anchors.get(edge.to.lane) ?? a, g)
    setPop({ kind: 'edge', edges: [edge], x: mid.x, y: mid.y, focus: focusRef.current })
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
    const again = popRef.current?.kind === 'lane' && popRef.current.key === lane.key && !chevron && (lane.kind === 'run' || x !== undefined)
    setPop(null)
    if (!again) latestActivate.current(lane, x, chevron)
  }, [])
  // A click on a lane, mark, link or pinned button replaces or closes the open popover itself; Radix dismissing it after that click would close a new one.
  const keep = useCallback((target: Element) => !drag.current?.moved && Boolean(content.current?.contains(target) && target.closest('[role=treeitem], [data-mail-control]') && !target.closest('[role=treeitem] button')), [])
  const latestActivate = useRef(activate)
  latestActivate.current = activate
  const onRetry = useCallback((runID: string) => latest.current.onRetryAgents(runID), [])
  const onWiden = useCallback(() => latest.current.onWiden(), [])
  const pickEdges = useCallback((edges: LaneEdge[], x: number, y: number, lane?: string) => {
    // The focused lane stays when the mail is its own; mail on another lane focuses the lane that drew it.
    const focus = focusRef.current
    if (!drag.current?.moved) setPop({ kind: 'edge', edges, x, y, focus: edges.some((edge) => edge.from.lane === focus || edge.to.lane === focus) ? focus : lane ?? (edges[0].from.lane || edges[0].to.lane) })
  }, [])
  // Swapping the lane popover for the reply composer unmounts its content too; only a real close returns focus.
  // Moving to a lane from a popover's message list closes the popover there, not on the lane it opened from.
  const jumped = useRef<string | null>(null)
  const returnTo = (key: string | undefined) => () => {
    const target = jumped.current ?? key
    jumped.current = null
    // A drag closes the card without pulling the view back to its lane.
    if (!popRef.current && target && !drag.current?.moved) focusLaneEl(target)
  }

  // The focused lane, kept so focus survives a live update that removes it: the browser drops it on the page.
  const focused = useRef<{ key: string; index: number; run?: string } | null>(null)
  const lost = focused.current && !byKey.has(focused.current.key) ? focused.current : null
  // Lanes coming and going above the focused lane move it; its neighbour is found from where it is now.
  useEffect(() => {
    const now = focused.current
    if (now && byKey.has(now.key)) now.index = model.lanes.findIndex((lane) => lane.key === now.key)
  }, [model.lanes, byKey])
  useEffect(() => {
    if (!lost || popRef.current || (document.activeElement && document.activeElement !== document.body)) return
    focused.current = null
    const key = (lost.run && model.drawn.get(lost.run)) || model.lanes[Math.min(lost.index, model.lanes.length - 1)]?.key
    if (!key) return
    setActiveKey(key)
    focusLaneEl(key)
  }, [lost])

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
  const activeLane = active ? byKey.get(active) : undefined
  const visibleBottom = viewport.top + viewport.height - axisHeight
  const offscreen = (c: Conversation, above: boolean) => {
    const top = c.lane ? layout.top.get(c.lane) : undefined
    if (top === undefined) return false
    return above ? top + laneHeight(byKey.get(c.lane)!, g.metrics) <= viewport.top : top >= visibleBottom
  }
  // Before the first measure nothing counts as scrolled away.
  const view = viewport.height > 0 ? { top: viewport.top, bottom: visibleBottom } : { top: -Infinity, bottom: Infinity }
  const focusAbove = focusTalks.filter((c) => offscreen(c, true))
  const focusBelow = focusTalks.filter((c) => offscreen(c, false))
  const focusOutside = focusTalks.filter((c) => !c.lane)
  const jump = (key: string) => {
    if (popRef.current) {
      jumped.current = key
      setPop(null)
    }
    setHovered(null)
    setActiveKey(key)
    focusLaneEl(key)
  }
  const highlights = [
    hoveredLane && hoveredLane.kind !== 'section' && hoveredLane.key !== popLane?.key && { key: hoveredLane.key, kind: 'hover' },
    popLane && { key: popLane.key, kind: 'selection' },
    !hoveredLane && !popLane && treeFocused && activeLane && activeLane.kind !== 'section' && { key: activeLane.key, kind: 'hover' },
  ].filter((item): item is { key: string; kind: 'hover' | 'selection' } => Boolean(item))
  // A lane's popover follows its lane and the moment it was opened at, as live updates move them.
  const anchor = pop?.kind === 'lane' && popLane
    ? { x: pop.at === undefined ? pop.x : Math.min(g.width, Math.max(0, xOf(pop.at, g.window, g.width))), y: layout.top.get(popLane.key)! + (laneHeight(popLane, g.metrics) - pop.h) / 2 }
    : { x: pop?.x ?? 0, y: pop?.y ?? 0 }
  const tickMarks = spaced(ticks(g.window, now), g)
  const popRun = popLane?.kind === 'run' ? popLane : undefined
  const totalWidth = g.labelWidth + g.width + trackGutter

  return (
    <div
      ref={scroller}
      role="region"
      aria-label="Session lanes"
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
        onPointerLeave={() => {
          pointer.current = null
          hover(null)
        }}
        onPointerUp={pointerUp}
        onPointerCancel={pointerUp}
      >
        {highlights.map((item) => (
          <div key={item.key} aria-hidden data-lane-highlight={item.kind} className={cn('pointer-events-none absolute inset-x-0', item.kind === 'hover' ? 'bg-hover' : 'bg-selection')} style={{ top: layout.top.get(item.key), height: laneHeight(byKey.get(item.key)!, g.metrics) }} />
        ))}
        <div aria-hidden className="pointer-events-none absolute inset-y-0" style={{ left: g.labelWidth, width: g.width }}>
          {tickMarks.map((tick) => (
            <span key={tick.t} className="absolute inset-y-0 border-l border-dashed border-seam" style={{ left: xOf(tick.t, g.window, g.width) }} />
          ))}
          {nowShown && <span className="absolute inset-y-0 w-px bg-icon-faint" style={{ left: nowX }} />}
        </div>
        <Hairlines edges={hairlines} anchors={anchors} g={g} height={layout.height} receded={focusKey !== undefined} />
        <EdgeStrokes shapes={shapes} g={g} height={layout.height} />
        <div
          role="tree"
          aria-label="Session lanes"
          onKeyDown={keyDown}
          onFocus={(event) => {
            setTreeFocused(true)
            const key = (event.target as HTMLElement).dataset.key
            if (!key) return
            setActiveKey(key)
            const lane = byKey.get(key)
            focused.current = { key, index: model.lanes.findIndex((item) => item.key === key), run: lane?.kind === 'run' ? lane.card.run.id : lane?.kind === 'family' ? lane.parentID : undefined }
          }}
          onBlur={(event) => {
            if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) return
            setTreeFocused(false)
            if ((event.target as Node).isConnected) focused.current = null
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
              name={[treeLabel(lane, now), talkWords.get(lane.key)].filter(Boolean).join(', ')}
              active={lane.key === active}
              selected={lane.key === popLane?.key}
              agentName={lane.kind === 'run' ? props.agentNames[lane.card.run.harness] : undefined}
              widen={lane.kind === 'section' ? props.widen : undefined}
              mail={chips.get(lane.key)}
              onPick={onPick}
              onWiden={onWiden}
              onRetry={onRetry}
            />
          ))}
        </div>
        <EdgeHits shapes={shapes} g={g} height={layout.height} title={edgeTitle} onPick={pickEdges} />
        <MailMarks marks={marks} anchors={anchors} g={g} height={layout.height} focus={focusKey} linked={linked} title={edgeTitle} onPick={pickEdges} />
        {focusTalks.length > 0 && viewport.height > 0 && (
          <OffscreenMail
            above={focusAbove}
            below={focusBelow}
            outside={focusOutside}
            g={g}
            top={viewport.top}
            bottom={visibleBottom}
            nameOf={nameOf}
            onJump={jump}
          />
        )}
        <LiveMail
          mail={props.edgeInput.mail}
          drawn={model.drawn}
          anchors={anchors}
          g={g}
          view={view}
          now={now}
          reserved={{ top: Number(focusAbove.length > 0) + Number(focusOutside.length > 0), bottom: Number(focusBelow.length > 0) }}
          nameOf={nameOf}
          onJump={jump}
        />
        <Popover open={pop !== null} onOpenChange={(open) => { if (!open) setPop(null) }}>
          {pop && (
            <PopoverAnchor asChild>
              <span aria-hidden className="pointer-events-none absolute w-px" style={{ left: g.labelWidth + anchor.x, top: anchor.y, height: pop.kind === 'lane' ? pop.h : 0 }} />
            </PopoverAnchor>
          )}
          {pop?.kind === 'lane' && popRun && pop.reply ? (
            <ReplyComposer card={popRun.card} title={popRun.title} onDone={() => setPop(null)} returnFocus={returnTo(popRun.key)} />
          ) : pop?.kind === 'lane' && popLane ? (
            <LanePopoverContent label={treeLabel(popLane, now)} returnFocus={returnTo(popLane.key)} keep={keep}>
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
              <LaneConversations
                conversations={talks.get(popLane.key) ?? []}
                lane={popLane.key}
                nameOf={nameOf}
                now={now}
                onPick={openEdge}
                onJump={(key) => {
                  jumped.current = key
                  setActiveKey(key)
                  setPop(null)
                }}
              />
              <LaneLinks edges={overlaps.filter((edge) => edge.from.lane === popLane.key || edge.to.lane === popLane.key)} now={now} onPick={openEdge} />
            </LanePopoverContent>
          ) : pop?.kind === 'edge' ? (
            <LanePopoverContent label="Link" returnFocus={returnTo(active)} keep={keep}>
              {pop.edges.map((edge, i) => (
                <div key={edge.key} className={cn(i > 0 && 'mt-3 border-t border-seam pt-2')}>
                  <EdgePopover edge={edge} now={now} />
                </div>
              ))}
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

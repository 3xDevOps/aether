import { Fragment, useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { ArrowDown, ArrowUp } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { useReducedMotion } from '@/lib/hooks'
import { cn } from '@/lib/utils'
import { xOf } from '@/routes/board/lanes/axis'
import type { EdgeAnchor } from '@/routes/board/lanes/connectors'
import type { Mail } from '@/routes/board/lanes/edges'
import type { LaneGeometry } from '@/routes/board/lanes/lane-row'
import { mailControl } from '@/routes/board/lanes/mail-marks'
import { useStore } from '@/store'

/** How long a live arrow shows, its fade included; under 5s, so it needs no pause control (WCAG 2.2.2). */
export const liveMailMs = 4500
/** Arrows on screen at once; a newer pair replaces the oldest. */
export const liveMailCap = 3
const slotPx = 8
const nowGap = 14
const chipRoom = 160
const stubPx = 22
const announceDelay = 1500
/** Older mail is a reconnect replaying the log, not mail arriving now; the margin covers clock skew. */
const replayMs = 60_000

export interface LiveEnd {
  /** Empty when the session has no lane in this view. */
  lane: string
  run: string
}

/** One sender-to-recipient lane pair that received mail while the view was open. */
export interface LiveArrow {
  pair: string
  from: LiveEnd
  to: LiveEnd
  /** The newest send time. */
  t: number
  count: number
  /** Offset left of the send time, so arrows at the same moment never sit on one line. */
  slot: number
  /** When the newest message arrived, by this browser's clock. */
  at: number
}

export interface Arrival {
  from: LiveEnd
  to: LiveEnd
  t: number
  at: number
}

/** Mail on a live pair restarts its arrow and adds to its count; past the cap, the oldest arrow goes. */
export function addLiveMail(live: LiveArrow[], arrivals: Arrival[]): LiveArrow[] {
  let out = live
  for (const a of arrivals) {
    const pair = `${a.from.lane || a.from.run}>${a.to.lane || a.to.run}`
    const same = out.find((arrow) => arrow.pair === pair)
    if (same) {
      out = [...out.filter((arrow) => arrow !== same), { ...same, t: Math.max(same.t, a.t), count: same.count + 1, at: a.at }]
      continue
    }
    const kept = out.slice(Math.max(0, out.length - liveMailCap + 1))
    const used = new Set(kept.map((arrow) => arrow.slot))
    let slot = 0
    while (used.has(slot)) slot++
    out = [...kept, { pair, from: a.from, to: a.to, t: a.t, count: 1, slot, at: a.at }]
  }
  return out
}

function laneOf(drawn: Map<string, string>, run: string): LiveEnd {
  return { lane: drawn.get(run) ?? '', run }
}

/**
 * Arrows for mail that arrives while the view is open, and one polite line
 * per burst. Nothing in held history at mount, or read from older pages,
 * ever draws one.
 */
export function useLiveMail({ arrivals, mail, drawn, shown, nameOf }: {
  arrivals: string[]
  mail: Mail
  drawn: Map<string, string>
  shown: boolean
  nameOf: (end: LiveEnd) => string
}): { live: LiveArrow[]; announcement: string } {
  const [live, setLive] = useState<LiveArrow[]>([])
  const [announcement, setAnnouncement] = useState('')
  const seen = useRef<Set<string> | null>(null)
  seen.current ??= new Set(arrivals)
  const queue = useRef<string[]>([])
  const announceTimer = useRef<number | undefined>(undefined)
  const latest = useRef({ mail, drawn, shown, nameOf })
  latest.current = { mail, drawn, shown, nameOf }

  useEffect(() => {
    const fresh = arrivals.filter((id) => !seen.current!.has(id))
    seen.current = new Set(arrivals)
    if (fresh.length === 0) return
    const { mail, drawn, shown, nameOf } = latest.current
    // A hidden tab throttles timers, so arrows would pile up and play together on return.
    if (!shown || document.hidden) return
    const wanted = new Set(fresh)
    const at = Date.now()
    const found: Arrival[] = []
    for (const { t, payload } of mail.sent) {
      if (!wanted.has(payload.message_id) || t < at - replayMs) continue
      const from = laneOf(drawn, payload.from_run_id)
      const to = laneOf(drawn, payload.to_run_id)
      if ((!from.lane && !to.lane) || from.lane === to.lane) continue
      found.push({ from, to, t, at })
    }
    if (found.length === 0) return
    setLive((current) => addLiveMail(current, found))
    queue.current.push(...found.map((a) => `${nameOf(a.from)} to ${nameOf(a.to)}`))
    if (announceTimer.current !== undefined) return
    // Cleared first, so a burst worded like the last one is still read.
    setAnnouncement('')
    announceTimer.current = window.setTimeout(() => {
      announceTimer.current = undefined
      const pairs = [...new Set(queue.current)]
      const n = queue.current.length
      queue.current = []
      setAnnouncement(n === 1 ? `New message from ${pairs[0]}` : `${n} new messages: ${pairs.slice(0, 3).join(', ')}${pairs.length > 3 ? `, and ${pairs.length - 3} more` : ''}`)
    }, announceDelay)
  }, [arrivals])

  useEffect(() => () => clearTimeout(announceTimer.current), [])

  useEffect(() => {
    if (live.length === 0) return
    const next = Math.min(...live.map((arrow) => arrow.at)) + liveMailMs - Date.now()
    const timer = window.setTimeout(() => setLive((current) => current.filter((arrow) => arrow.at + liveMailMs > Date.now())), Math.max(0, next))
    return () => clearTimeout(timer)
  }, [live])

  useEffect(() => {
    if (shown) return
    setLive([])
    setAnnouncement('')
    clearTimeout(announceTimer.current)
    announceTimer.current = undefined
    queue.current = []
  }, [shown])

  return { live, announcement }
}

/** Where each arrow sits; `offscreen` names the end past the scrolled rows, if any. */
export interface LiveShape {
  arrow: LiveArrow
  x: number
  y1: number
  y2: number
  /** Mail with a session outside this view draws a stub from the drawn end, labelled with the other session. */
  stub?: 'out' | 'in'
  offscreen?: { end: 'from' | 'to'; edge: 'top' | 'bottom' }
}

/** Ends resolve through the current `drawn`, so an arrow follows a family that folds or opens while it shows. */
export function liveShapes(live: LiveArrow[], drawn: Map<string, string>, anchors: Map<string, EdgeAnchor>, g: LaneGeometry, view: { top: number; bottom: number }, now: number): LiveShape[] {
  const following = g.window.end >= now
  return live.flatMap((live): LiveShape[] => {
    const arrow = { ...live, from: laneOf(drawn, live.from.run), to: laneOf(drawn, live.to.run) }
    // Panned into the past, mail sent now is past the window's end, as its marks are.
    if (arrow.t < g.window.start || (arrow.t > g.window.end && !following)) return []
    const a = anchors.get(arrow.from.lane)
    const b = anchors.get(arrow.to.lane)
    if ((!a && !b) || arrow.from.lane === arrow.to.lane) return []
    // Clear of live bars' end caps and the now line, which mail sent now would otherwise sit on.
    const x = Math.max(6, Math.min(g.width - nowGap, xOf(arrow.t, g.window, g.width)) - arrow.slot * slotPx)
    const mid = (e: EdgeAnchor) => e.top + e.height / 2
    if (a && b) {
      const dir = Math.sign(mid(b) - mid(a)) || 1
      const y1 = mid(a) + dir * (a.half + 2)
      const y2 = mid(b) - dir * (b.half + 2)
      const out = (y: number): 'top' | 'bottom' | undefined => (y < view.top ? 'top' : y > view.bottom ? 'bottom' : undefined)
      const toEdge = out(y2)
      const fromEdge = out(y1)
      const offscreen = toEdge ? { end: 'to' as const, edge: toEdge } : fromEdge ? { end: 'from' as const, edge: fromEdge } : undefined
      return [{ arrow, x, y1, y2, offscreen }]
    }
    // Over the top edge, as the marks of outside mail are, unless the lane is the first row.
    const e = (a ?? b)!
    const up = e.top >= stubPx
    const edge = up ? mid(e) - e.half - 2 : mid(e) + e.half + 2
    const far = edge + (up ? -stubPx : stubPx)
    const away: 'top' | 'bottom' | undefined = edge < view.top ? 'top' : edge > view.bottom ? 'bottom' : undefined
    const offscreen = away && { end: a ? 'from' as const : 'to' as const, edge: away }
    return [a ? { arrow, x, y1: edge, y2: far, stub: 'out', offscreen } : { arrow, x, y1: far, y2: edge, stub: 'in', offscreen }]
  })
}

/** A button that mounts after its arrow starts at the arrow's age, fixed at mount: a delay changed mid-run would move the fade. */
function LiveChip({ at, className, style, onClick, children }: {
  at: number
  className: string
  style: React.CSSProperties
  onClick: () => void
  children: React.ReactNode
}) {
  const [delay] = useState(() => at - Date.now())
  return (
    <Button
      {...mailControl}
      data-mail-live-chip
      variant="secondary"
      size="sm"
      tabIndex={-1}
      className={cn('pointer-events-auto absolute z-10 h-6 max-w-[min(280px,60%)] gap-1 border-series-5 px-2 text-ui-xs', className)}
      style={{ ...style, animationDelay: `${delay}ms` }}
      onClick={onClick}
    >
      {children}
    </Button>
  )
}

/**
 * Each arrow is its own HTML box, so its fade, the pulse that travels it and
 * the ring at its head run on the compositor: Chromium repaints an
 * animation on an SVG child on the main thread.
 */
export function LiveMail({ mail, drawn, anchors, g, view, now, reserved, nameOf, onJump }: {
  mail: Mail
  drawn: Map<string, string>
  anchors: Map<string, EdgeAnchor>
  g: LaneGeometry
  view: { top: number; bottom: number }
  now: number
  /** Rows the focused lane's pinned buttons already take at each edge. */
  reserved: { top: number; bottom: number }
  nameOf: (end: LiveEnd) => string
  onJump: (lane: string) => void
}) {
  // Live state lives here, not in the lanes view, so an arrow coming or going re-renders only this layer.
  const arrivals = useStore((s) => s.laneArrivals)
  const shown = useStore((s) => !s.laneHidden.includes('message'))
  const { live, announcement } = useLiveMail({ arrivals, mail, drawn, shown, nameOf })
  const still = useReducedMotion()
  const pinned = { top: reserved.top, bottom: reserved.bottom }
  return (
    <>
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">{announcement}</span>
      {liveShapes(live, drawn, anchors, g, view, now).map(({ arrow, x, y1, y2, stub, offscreen }) => {
        const dir = Math.sign(y2 - y1) || 1
        const top = Math.min(y1, y2) - 6
        const height = Math.abs(y2 - y1) + 12
        const from = y1 - top
        const to = y2 - top
        const key = `${arrow.pair}@${arrow.at}`
        const fade = !still && 'motion-safe:animate-mail-live'
        const other = stub === 'out' ? arrow.to : stub === 'in' ? arrow.from : undefined
        let chip = null
        if (offscreen) {
          const row = pinned[offscreen.edge]++
          const end = offscreen.end === 'to' ? arrow.to : arrow.from
          const Icon = offscreen.edge === 'top' ? ArrowUp : ArrowDown
          // Too close to the window's start for the button to fit on the arrow's left.
          const right = x < chipRoom
          chip = (
            <LiveChip
              key={`${key}:chip`}
              at={arrow.at}
              className={cn(fade, !right && '-translate-x-full')}
              style={{ left: g.labelWidth + x + (right ? 10 : -10), top: offscreen.edge === 'top' ? view.top + 4 + row * 28 : view.bottom - 28 - row * 28 }}
              onClick={() => onJump(end.lane)}
            >
              <Icon aria-hidden className="size-3 shrink-0 text-series-5" />
              <span className="min-w-0 truncate">{offscreen.end === 'to' ? `to ${nameOf(end)}` : `from ${nameOf(end)}`}</span>
              {arrow.count > 1 && <span className="shrink-0 text-muted tabular-nums">×{arrow.count}</span>}
            </LiveChip>
          )
        }
        const flash = anchors.get(arrow.to.lane)
        return (
          <Fragment key={arrow.pair}>
          {flash && (
            <div
              key={`${key}:flash`}
              aria-hidden
              data-mail-live-flash
              className={cn(fade, 'pointer-events-none absolute bg-series-5/10')}
              style={{ left: g.labelWidth, width: g.width, top: flash.top, height: flash.height }}
            />
          )}
          <div
            key={key}
            aria-hidden
            data-mail-live={arrow.pair}
            className={cn(fade, 'pointer-events-none absolute text-series-5')}
            style={{ left: g.labelWidth + x - 8, top, width: 16, height }}
          >
            <svg className="absolute inset-0 overflow-visible" width={16} height={height}>
              <path d={`M8 ${from}V${to - dir * 5}`} stroke="var(--canvas)" strokeWidth={4} strokeLinecap="round" />
              <path d={`M8 ${from}V${to - dir * 5}`} stroke="currentColor" strokeWidth={2} strokeDasharray="4 3" strokeLinecap="round" />
              {stub !== 'in' && <circle cx={8} cy={from} r={2.5} fill="currentColor" stroke="var(--canvas)" strokeWidth={1} />}
              <polygon points={`8,${to} 4.5,${to - dir * 6} 11.5,${to - dir * 6}`} fill="currentColor" stroke="var(--canvas)" strokeWidth={1} strokeLinejoin="round" />
            </svg>
            {!still && (
              <>
                <span
                  className="absolute left-1/2 size-2 -translate-x-1/2 -translate-y-1/2 rounded-full motion-safe:animate-mail-travel bg-current"
                  style={{ top: from, '--mail-travel': `${to - from}px` } as React.CSSProperties}
                />
                <span
                  className="absolute left-1/2 size-3 -translate-x-1/2 -translate-y-1/2 rounded-full motion-safe:animate-mail-ping border-2 border-current"
                  style={{ top: to - dir * 3 }}
                />
              </>
            )}
            {(arrow.count > 1 || other) && (
              <span
                className="absolute flex h-4 items-center gap-0.5 rounded-chip border border-series-5 bg-raised px-1 text-ui-xs whitespace-nowrap text-text tabular-nums"
                style={{ right: 12, top: stub ? (stub === 'out' ? to : from) - 8 : (from + to) / 2 - 8 }}
              >
                {other && <span className="max-w-40 truncate text-muted">{stub === 'out' ? 'to' : 'from'} {nameOf(other)}</span>}
                {arrow.count > 1 && `×${arrow.count}`}
              </span>
            )}
          </div>
          {chip}
          </Fragment>
        )
      })}
    </>
  )
}

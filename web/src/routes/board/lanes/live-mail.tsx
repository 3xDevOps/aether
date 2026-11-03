import { useEffect, useRef, useState } from 'react'
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

/** How long a live arrow shows, its fade included; under 5s, so it needs no pause control (WCAG 2.2.2). */
export const liveMailMs = 4500
/** Arrows on screen at once; a newer pair replaces the oldest. */
export const liveMailCap = 3
const slotPx = 8
const stubPx = 22
const announceDelay = 1500

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
    if (fresh.length === 0) return
    for (const id of fresh) seen.current!.add(id)
    const { mail, drawn, shown, nameOf } = latest.current
    // A hidden tab throttles timers, so arrows would pile up and play together on return.
    if (!shown || document.hidden) return
    const wanted = new Set(fresh)
    const at = Date.now()
    const found: Arrival[] = []
    for (const { t, payload } of mail.sent) {
      if (!wanted.has(payload.message_id)) continue
      const from = laneOf(drawn, payload.from_run_id)
      const to = laneOf(drawn, payload.to_run_id)
      if ((!from.lane && !to.lane) || from.lane === to.lane) continue
      found.push({ from, to, t, at })
    }
    if (found.length === 0) return
    setLive((current) => addLiveMail(current, found))
    queue.current.push(...found.map((a) => `${nameOf(a.from)} to ${nameOf(a.to)}`))
    if (announceTimer.current !== undefined) return
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
    if (!shown) setLive([])
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

export function liveShapes(live: LiveArrow[], anchors: Map<string, EdgeAnchor>, g: LaneGeometry, view: { top: number; bottom: number }): LiveShape[] {
  return live.flatMap((arrow): LiveShape[] => {
    const a = anchors.get(arrow.from.lane)
    const b = anchors.get(arrow.to.lane)
    if (!a && !b) return []
    // Inside the bar's rounded ends, like the marks it leaves behind.
    const x = Math.max(6, Math.min(g.width - 6, xOf(arrow.t, g.window, g.width)) - arrow.slot * slotPx)
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
    return [a ? { arrow, x, y1: edge, y2: far, stub: 'out' as const } : { arrow, x, y1: far, y2: edge, stub: 'in' as const }]
  })
}

function head(x: number, y: number, dir: number): string {
  return `${x},${y} ${x - 3.5},${y - dir * 6} ${x + 3.5},${y - dir * 6}`
}

/**
 * Each arrow is its own HTML box, so its fade, the pulse that travels it and
 * the ring at its head run on the compositor: Chromium repaints an
 * animation on an SVG child on the main thread.
 */
export function LiveMail({ shapes, g, view, reserved, nameOf, onJump }: {
  shapes: LiveShape[]
  g: LaneGeometry
  view: { top: number; bottom: number }
  /** Rows the focused lane's pinned buttons already take at each edge. */
  reserved: { top: number; bottom: number }
  nameOf: (end: LiveEnd) => string
  onJump: (lane: string) => void
}) {
  const still = useReducedMotion()
  const pinned = { top: reserved.top, bottom: reserved.bottom }
  return (
    <>
      {shapes.map(({ arrow, x, y1, y2, stub, offscreen }) => {
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
          chip = (
            <Button
              {...mailControl}
              key={`${key}:chip`}
              data-mail-live-chip
              variant="secondary"
              size="sm"
              tabIndex={-1}
              className={cn(fade, 'pointer-events-auto absolute z-10 h-6 max-w-[min(280px,60%)] -translate-x-full gap-1 border-series-5 px-2 text-ui-xs')}
              style={{ left: g.labelWidth + x - 10, top: offscreen.edge === 'top' ? view.top + 4 + row * 28 : view.bottom - 28 - row * 28 }}
              onClick={() => onJump(end.lane)}
            >
              <Icon aria-hidden className="size-3 shrink-0 text-series-5" />
              <span className="min-w-0 truncate">{offscreen.end === 'to' ? `to ${nameOf(end)}` : `from ${nameOf(end)}`}</span>
              {arrow.count > 1 && <span className="shrink-0 text-muted tabular-nums">×{arrow.count}</span>}
            </Button>
          )
        }
        return [
          <div
            key={key}
            aria-hidden
            data-mail-live={arrow.pair}
            className={cn(fade, 'pointer-events-none absolute text-series-5')}
            style={{ left: g.labelWidth + x - 8, top, width: 16, height }}
          >
            <svg className="absolute inset-0 overflow-visible" width={16} height={height}>
              <path d={`M8 ${from}V${to - dir * 5}`} stroke="var(--canvas)" strokeWidth={4} strokeLinecap="round" />
              <path d={`M8 ${from}V${to - dir * 5}`} stroke="currentColor" strokeWidth={1.5} strokeDasharray="4 3" strokeLinecap="round" />
              {stub !== 'in' && <circle cx={8} cy={from} r={2.5} fill="currentColor" stroke="var(--canvas)" strokeWidth={1} />}
              <polygon points={head(8, to, dir)} fill="currentColor" stroke="var(--canvas)" strokeWidth={1} strokeLinejoin="round" />
            </svg>
            {!still && (
              <>
                <span
                  className="absolute left-1/2 size-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full motion-safe:animate-mail-travel bg-current"
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
          </div>,
          chip,
        ]
      })}
    </>
  )
}

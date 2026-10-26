import { spanEnd, type Interval, type Span } from '@/store/trace'

const second = 1000
const minute = 60 * second
const hour = 60 * minute
const day = 24 * hour

/** An idle stretch drawn as a fixed-width break; `unknown` is history that is not loaded, `open` the idle stretch up to now. */
export interface Gap extends Interval {
  unknown?: boolean
  open?: boolean
}

export interface Axis {
  start: number
  end: number
  gaps: Gap[]
  /** Axis time one break takes. */
  breakMs: number
  /** Axis time of the whole run, with headroom while it is live. */
  length: number
}

const minIdle = minute
const idleShare = 0.05
/** Breaks are a fiftieth of the domain, about 16px across a laptop-width track. */
const breakShare = 1 / 50
const liveHeadroom = 0.85

function activity(root: Span, now: number): Interval[] {
  const spans = root.children.flatMap((child) => (child.kind === 'workers' ? child.children : [child]))
  const intervals = spans.map((span) => ({ from: span.start, to: spanEnd(span, now) })).sort((a, b) => a.from - b.from)
  const union: Interval[] = []
  for (const interval of intervals) {
    const last = union.at(-1)
    if (last && interval.from <= last.to) last.to = Math.max(last.to, interval.to)
    else union.push({ ...interval })
  }
  return union
}

/**
 * Stretches with nothing running that last longer than a minute and 5% of the active time.
 * With `partial` the stretch before the first held item is history that is not loaded.
 */
export function idleGaps(root: Span, now: number, partial: boolean): Gap[] {
  const live = root.end === null
  const end = spanEnd(root, now)
  const union = activity(root, now)
  const active = union.reduce((sum, interval) => sum + interval.to - interval.from, 0)
  const threshold = Math.max(minIdle, idleShare * active)
  const holes: Gap[] = []
  let at = root.start
  for (const interval of union) {
    if (interval.from > at) holes.push({ from: at, to: interval.from, unknown: partial && at === root.start })
    at = Math.max(at, interval.to)
  }
  if (end > at) holes.push({ from: at, to: end, open: live || undefined })
  return holes.filter((hole) => hole.unknown || hole.to - hole.from > threshold)
}

const ladder = [
  ...[1, 2, 5, 10, 15, 20, 30].map((n) => n * second),
  ...[1, 2, 5, 10, 15, 20, 30].map((n) => n * minute),
  ...[1, 2, 3, 6, 12].map((n) => n * hour),
  ...[1, 2, 5, 7, 14, 30].map((n) => n * day),
]

function niceCeil(ms: number): number {
  return ladder.find((step) => step >= ms) ?? Math.ceil(ms / (30 * day)) * 30 * day
}

/** A live length rounded up through the ladder with headroom, so it steps rather than grows each second. */
export function withHeadroom(ms: number): number {
  return niceCeil(ms / liveHeadroom)
}

export function buildAxis(start: number, end: number, gaps: Gap[], live: boolean): Axis {
  const active = Math.max(second, end - start - gaps.reduce((sum, gap) => sum + gap.to - gap.from, 0))
  const domain = live ? withHeadroom(active) : active
  const breakMs = domain * breakShare
  return { start, end, gaps, breakMs, length: domain + gaps.length * breakMs }
}

/** A real time's place on the axis, in axis milliseconds from the run's start. */
export function axisAt(axis: Axis, t: number): number {
  let a = t - axis.start
  for (const gap of axis.gaps) {
    if (t <= gap.from) break
    if (gap.open) {
      a -= t - gap.from - axis.breakMs
      break
    }
    const length = gap.to - gap.from
    if (t >= gap.to) {
      a -= length - axis.breakMs
      continue
    }
    a -= (t - gap.from) * (1 - axis.breakMs / length)
    break
  }
  return Math.max(0, a)
}

/** Elapsed time down to the second, trailing zero units dropped: "0s", "1m 30s", "2h", "2h 0m 10s". */
export function tickLabel(ms: number): string {
  const parts: string[] = []
  for (const [unit, size] of [['d', day], ['h', hour], ['m', minute], ['s', second]] as const) {
    const n = Math.floor(ms / size)
    ms -= n * size
    if (n > 0 || parts.length > 0) parts.push(`${n}${unit}`)
  }
  while (parts.length > 1 && parts.at(-1)!.startsWith('0')) parts.pop()
  return parts.length ? parts.join(' ') : '0s'
}

/** The largest unit only: "2h", "45m". */
export function roughDuration(ms: number): string {
  return tickLabel(ms).split(' ')[0]!
}

export interface AxisMark {
  /** Axis time. */
  a: number
  label: string
  kind: 'tick' | 'break' | 'unknown'
}

const tickSpacing = 72
const labelRoom = 52

/** The label of a break, after it: an open one reads its length as of the last axis change. */
export function gapLabel(gap: Gap): string {
  return gap.unknown ? 'Not loaded' : `${roughDuration(gap.to - gap.from)} idle`
}

/** Tick labels in elapsed run time inside each active stretch in view, and a label after each break, kept apart. */
export function axisMarks(axis: Axis, from: number, to: number, pxPerMs: number): AxisMark[] {
  const step = ladder.find((candidate) => candidate * pxPerMs >= tickSpacing) ?? ladder.at(-1)!
  const low = from - labelRoom / pxPerMs
  const marks: AxisMark[] = []
  const pieces: Interval[] = []
  let at = axis.start
  for (const gap of axis.gaps) {
    pieces.push({ from: at, to: gap.from })
    at = gap.to
  }
  // Nothing runs after an open break: the time beyond it is still to come.
  if (!axis.gaps.at(-1)?.open) pieces.push({ from: at, to: axis.end + (axis.length - axisAt(axis, axis.end)) })
  pieces.forEach((piece, i) => {
    const gap = axis.gaps[i - 1]
    if (gap) marks.push({ a: axisAt(axis, gap.from) + axis.breakMs, label: gapLabel(gap), kind: gap.unknown ? 'unknown' : 'break' })
    // Inside a piece axis time runs with real time, so only the stretch in view is walked.
    const pa = axisAt(axis, piece.from)
    const first = Math.max(piece.from, piece.from + low - pa)
    const last = Math.min(piece.to, piece.from + to - pa)
    for (let k = Math.ceil((first - axis.start) / step); axis.start + k * step <= last; k++) {
      marks.push({ a: axisAt(axis, axis.start + k * step), label: tickLabel(k * step), kind: 'tick' })
    }
  })
  const lastGap = axis.gaps.at(-1)
  if (lastGap?.open) marks.push({ a: axisAt(axis, lastGap.from) + axis.breakMs, label: gapLabel(lastGap), kind: 'break' })
  const kept: AxisMark[] = []
  for (const mark of marks) {
    if (mark.a < low || mark.a > to) continue
    const previous = kept.at(-1)
    if (previous && (mark.a - previous.a) * pxPerMs < labelRoom) {
      if (mark.kind === 'tick') continue
      kept.pop()
    }
    kept.push(mark)
  }
  return kept
}

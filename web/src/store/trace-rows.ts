import { startsOpen, surfacedBelow, type Span } from '@/store/trace'

export type OpenState = Readonly<Record<string, boolean>>

export interface TraceRow {
  /** Unique among rows: a surfaced row carries its folded ancestor's id. */
  key: string
  span: Span
  level: number
  posinset: number
  setsize: number
  /** Undefined for a leaf. */
  expanded?: boolean
  /** A failed or waiting span shown under its folded ancestor. */
  surfaced?: boolean
  parentKey?: string
}

export function isOpen(span: Span, open: OpenState, last: boolean): boolean {
  return open[span.id] ?? startsOpen(span, last)
}

const below = new WeakMap<Span, { level: number; open: OpenState; last: boolean; rows: TraceRow[] }>()

function rowsBelow(span: Span, level: number, open: OpenState, last: boolean): TraceRow[] {
  const held = below.get(span)
  if (held && held.level === level && held.open === open && held.last === last) return held.rows
  const rows: TraceRow[] = []
  if (isOpen(span, open, last)) {
    // The Workers group follows a run's turns, so its last child is not always its last turn.
    const lastAt = span.kind === 'run' ? span.children.map((child) => child.kind).lastIndexOf('turn') : span.children.length - 1
    span.children.forEach((child, i) => {
      const lastChild = i === lastAt
      rows.push(rowOf(child, level + 1, span.id, i + 1, span.children.length, open, lastChild))
      if (child.children.length) rows.push(...rowsBelow(child, level + 1, open, lastChild))
    })
  } else {
    const surfaced = surfacedBelow(span)
    surfaced.forEach((child, i) => rows.push({
      key: `${span.id}>${child.id}`,
      span: child,
      level: level + 1,
      posinset: i + 1,
      setsize: surfaced.length,
      surfaced: true,
      parentKey: span.id,
    }))
  }
  below.set(span, { level, open, last, rows })
  return rows
}

function rowOf(span: Span, level: number, parentKey: string | undefined, posinset: number, setsize: number, open: OpenState, last: boolean): TraceRow {
  return {
    key: span.id,
    span,
    level,
    posinset,
    setsize,
    expanded: span.children.length ? isOpen(span, open, last) : undefined,
    parentKey,
  }
}

/** The visible rows, depth first. Rows under a closed turn or settled subagent are reused while its span, depth and the fold state hold. */
export function flattenTrace(root: Span, open: OpenState): TraceRow[] {
  return [rowOf(root, 1, undefined, 1, 1, open, true), ...rowsBelow(root, 1, open, true)]
}

/** tool, overlap and dense: one, two, or three or more tool calls at once. */
export type SegmentTone = 'model' | 'tool' | 'overlap' | 'dense' | 'failed' | 'wait'

export interface BarSegment {
  from: number
  /** Null while it runs. */
  to: number | null
  tone: SegmentTone
}

const depthTone: SegmentTone[] = ['tool', 'tool', 'overlap', 'dense']

/** Bottom to top. */
const layers: SegmentTone[] = ['model', 'tool', 'overlap', 'dense', 'failed', 'wait']

function descendants(span: Span, into: Span[] = []): Span[] {
  for (const child of span.children) {
    into.push(child)
    descendants(child, into)
  }
  return into
}

const segmentCache = new WeakMap<Span, BarSegment[]>()

/** A parent bar's insides: its own model time, then the tool density, failures and waits below it. */
export function barSegments(span: Span): BarSegment[] {
  const held = segmentCache.get(span)
  if (held) return held
  const below = descendants(span)
  const out: BarSegment[] = (span.model ?? []).map((interval) => ({ ...interval, tone: 'model' }))
  const points: [number, number][] = []
  for (const leaf of below) {
    if ((leaf.kind !== 'tool' && leaf.kind !== 'worker') || leaf.status === 'failed') continue
    if (leaf.end === leaf.start) out.push({ from: leaf.start, to: leaf.start, tone: 'tool' })
    points.push([leaf.start, 1])
    if (leaf.end !== null) points.push([leaf.end, -1])
  }
  points.sort((a, b) => a[0] - b[0] || a[1] - b[1])
  let depth = 0
  let at = 0
  for (const [t, step] of points) {
    if (depth > 0 && t > at) out.push({ from: at, to: t, tone: depthTone[Math.min(depth, 3)]! })
    depth += step
    at = t
  }
  if (depth > 0) out.push({ from: at, to: null, tone: depthTone[Math.min(depth, 3)]! })
  for (const child of below) {
    if (child.status === 'failed') out.push({ from: child.start, to: child.end, tone: 'failed' })
    else if (child.kind === 'wait') out.push({ from: child.start, to: child.end, tone: 'wait' })
  }
  out.sort((a, b) => layers.indexOf(a.tone) - layers.indexOf(b.tone) || a.from - b.from)
  const merged: BarSegment[] = []
  for (const segment of out) {
    const last = merged.at(-1)
    if (last && last.tone === segment.tone && last.to !== null && segment.from <= last.to) {
      last.to = segment.to === null ? null : Math.max(last.to, segment.to)
    } else merged.push({ ...segment })
  }
  segmentCache.set(span, merged)
  return merged
}

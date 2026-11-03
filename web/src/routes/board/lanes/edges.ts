import type { CoordMessageAckedPayload, CoordMessagePayload, Event, OverlapPeer, RunMessageKind } from '@/lib/types'
import type { TimeWindow } from '@/routes/board/lanes/axis'
import type { LaneEdgeKind } from '@/store/lanes'

/** One message in a bundle: a folded lane can bundle mail from or to several sessions. */
export interface MailItem {
  from: string
  to: string
  acked: boolean
}

export interface EdgeEnd {
  lane: string
  run: string
  t: number
}

export interface LaneEdge {
  key: string
  kind: LaneEdgeKind
  from: EdgeEnd
  to: EdgeEnd
  /** Bundled mail: every message id, its sending runs, whether all were acknowledged, and the kind when they share one. */
  messages?: { ids: string[]; senders: string[]; acked: boolean; kind?: RunMessageKind; correlation?: string; items: MailItem[] }
  files?: string[]
  /** Mail with a session that has no lane in this view: that run's ID; its end's lane is empty. */
  outside?: string
}

export interface EdgesInput {
  drawn: Map<string, string>
  mail: Mail
  window: TimeWindow
  now: number
  /** Mail closer than this in time, between the same two lanes, is one edge. */
  bucket: number
  /** The run whose overlaps are shown. */
  focus?: string
  overlaps: Record<string, OverlapPeer[]>
  hidden: ReadonlySet<LaneEdgeKind>
}

export interface Mail {
  sent: { t: number; payload: CoordMessagePayload }[]
  acked: Set<string>
}

/** Agent mail from held history, parsed once per history change rather than on every edge pass. */
export function mailOf(events: Event[]): Mail {
  const sent: Mail['sent'] = []
  const acked = new Set<string>()
  for (const event of events) {
    if (event.type === 'coord.message') sent.push({ t: Date.parse(event.time), payload: event.payload as CoordMessagePayload })
    else if (event.type === 'coord.message.acked') acked.add((event.payload as CoordMessageAckedPayload).message_id)
  }
  return { sent, acked }
}

/** Every relationship between drawn lanes; an edge whose ends share one lane is dropped, and mail with an undrawn session keeps its drawn end. */
export function laneEdges(input: EdgesInput): LaneEdge[] {
  const { drawn, hidden } = input
  const out: LaneEdge[] = []
  const end = (run: string, t: number): EdgeEnd | undefined => {
    const lane = drawn.get(run)
    return lane ? { lane, run, t } : undefined
  }
  const add = (edge: Omit<LaneEdge, 'from' | 'to'>, from: EdgeEnd | undefined, to: EdgeEnd | undefined) => {
    if (hidden.has(edge.kind) || !from || !to || from.lane === to.lane) return
    if (Math.max(from.t, to.t) < input.window.start || Math.min(from.t, to.t) > input.window.end) return
    out.push({ ...edge, from, to })
  }

  if (!hidden.has('message')) {
    const { acked } = input.mail
    const bundles = new Map<string, LaneEdge>()
    for (const { t, payload: p } of input.mail.sent) {
      const drawnFrom = end(p.from_run_id, t)
      const drawnTo = end(p.to_run_id, t)
      if ((!drawnFrom && !drawnTo) || drawnFrom?.lane === drawnTo?.lane || t < input.window.start || t > input.window.end) continue
      const outside = !drawnFrom ? p.from_run_id : !drawnTo ? p.to_run_id : undefined
      const from = drawnFrom ?? { lane: '', run: p.from_run_id, t }
      const to = drawnTo ?? { lane: '', run: p.to_run_id, t }
      const key = `message:${from.lane || from.run}>${to.lane || to.run}@${Math.floor(t / input.bucket)}`
      const bundle = bundles.get(key)
      const item = { from: p.from_run_id, to: p.to_run_id, acked: acked.has(p.message_id) }
      if (!bundle) {
        bundles.set(key, { key, kind: 'message', from, to, outside, messages: { ids: [p.message_id], senders: [p.from_run_id], acked: item.acked, kind: p.kind, correlation: p.correlation_id, items: [item] } })
        continue
      }
      const m = bundle.messages!
      m.ids.push(p.message_id)
      m.items.push(item)
      if (!m.senders.includes(p.from_run_id)) m.senders.push(p.from_run_id)
      m.acked &&= acked.has(p.message_id)
      if (m.kind !== p.kind) m.kind = undefined
      if (m.correlation !== p.correlation_id) m.correlation = undefined
    }
    out.push(...bundles.values())
  }

  if (input.focus) {
    for (const peer of input.overlaps[input.focus] ?? []) {
      add({ key: `overlap:${input.focus}:${peer.run_id}`, kind: 'overlap', files: peer.files }, end(input.focus, input.now), end(peer.run_id, input.now))
    }
  }
  return out
}

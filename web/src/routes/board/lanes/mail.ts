import type { LaneEdge } from '@/routes/board/lanes/edges'

export const mergePx = 8

export interface MailMark {
  key: string
  lane: string
  x: number
  /** The bar edge facing the other lane; mail with a session outside the view uses the top. */
  side: 'top' | 'bottom'
  /** Every merged bundle was sent from this lane. */
  sent: boolean
  count: number
  edges: LaneEdge[]
}

export function mailMarks(edges: LaneEdge[], order: Map<string, number>, toX: (t: number) => number): MailMark[] {
  const ends: { lane: string; x: number; side: MailMark['side']; sent: boolean; edge: LaneEdge }[] = []
  for (const edge of edges) {
    if (edge.kind !== 'message' || !edge.messages) continue
    const down = !edge.outside && (order.get(edge.from.lane) ?? 0) < (order.get(edge.to.lane) ?? 0)
    const x = toX(edge.from.t)
    if (edge.from.lane) ends.push({ lane: edge.from.lane, x, side: down ? 'bottom' : 'top', sent: true, edge })
    if (edge.to.lane) ends.push({ lane: edge.to.lane, x, side: down || edge.outside ? 'top' : 'bottom', sent: false, edge })
  }
  ends.sort((a, b) => a.lane.localeCompare(b.lane) || a.side.localeCompare(b.side) || a.x - b.x)
  const out: MailMark[] = []
  for (const end of ends) {
    const last = out.at(-1)
    if (last && last.lane === end.lane && last.side === end.side && end.x - last.x < mergePx) {
      last.edges.push(end.edge)
      last.sent &&= end.sent
      last.count += end.edge.messages!.ids.length
      continue
    }
    out.push({ key: `${end.edge.key}:${end.sent ? 'sent' : 'received'}`, lane: end.lane, x: end.x, side: end.side, sent: end.sent, count: end.edge.messages!.ids.length, edges: [end.edge] })
  }
  return out
}

/** One lane's mail with one other session; a folded lane can hold several, so this is per session, not per lane. */
export interface Conversation {
  /** The other session's lane; empty when it has none in this view. */
  lane: string
  run: string
  sent: number
  received: number
  unacked: number
  edges: LaneEdge[]
}

export function conversationsByLane(edges: LaneEdge[]): Map<string, Conversation[]> {
  const byLane = new Map<string, Map<string, Conversation>>()
  const add = (lane: string, otherLane: string, otherRun: string, sent: boolean, acked: boolean, edge: LaneEdge) => {
    if (!lane) return
    const mine = byLane.get(lane) ?? new Map<string, Conversation>()
    byLane.set(lane, mine)
    const c = mine.get(otherRun) ?? { lane: otherLane, run: otherRun, sent: 0, received: 0, unacked: 0, edges: [] }
    mine.set(otherRun, c)
    if (sent) c.sent++
    else c.received++
    if (!acked) c.unacked++
    if (c.edges.at(-1) !== edge) c.edges.push(edge)
  }
  for (const edge of edges) {
    if (edge.kind !== 'message' || !edge.messages) continue
    for (const item of edge.messages.items) {
      add(edge.from.lane, edge.to.lane, item.to, true, item.acked, edge)
      add(edge.to.lane, edge.from.lane, item.from, false, item.acked, edge)
    }
  }
  return new Map([...byLane].map(([lane, list]) => [lane, [...list.values()].sort((a, b) => b.sent + b.received - (a.sent + a.received))]))
}

type NameOf = (session: { lane: string; run: string }) => string

export function conversationWords(conversations: Conversation[], nameOf: NameOf): string {
  if (conversations.length === 0) return ''
  const parts = conversations.map((c) => [
    c.sent > 0 && `sent ${c.sent} to ${nameOf(c)}`,
    c.received > 0 && `received ${c.received} from ${nameOf(c)}`,
  ].filter(Boolean).join(' and '))
  const unacked = conversations.reduce((n, c) => n + c.unacked, 0)
  return [`messages: ${parts.join(', ')}`, unacked > 0 && `${unacked} not acknowledged`].filter(Boolean).join(', ')
}

/** Seen from the focused lane, as in its popover. */
export function conversationTag(c: Pick<Conversation, 'sent' | 'received'>): string {
  return [c.sent > 0 && `→${c.sent}`, c.received > 0 && `←${c.received}`].filter(Boolean).join(' ')
}

export function bundleWords(edge: LaneEdge, nameOf: NameOf, time: string): string {
  const { items } = edge.messages!
  const one = (run: string, lane: string) => `${nameOf({ lane, run })}${lane ? '' : ' (outside this view)'}`
  const senders = [...new Set(items.map((item) => item.from))].map((run) => one(run, edge.from.lane)).join(', ')
  const recipients = [...new Set(items.map((item) => item.to))].map((run) => one(run, edge.to.lane)).join(', ')
  const unacked = items.filter((item) => !item.acked).length
  return [
    `${items.length === 1 ? 'Message' : `${items.length} messages`} from ${senders} to ${recipients}`,
    time,
    unacked > 0 && `${unacked} not acknowledged`,
  ].filter(Boolean).join(' · ')
}

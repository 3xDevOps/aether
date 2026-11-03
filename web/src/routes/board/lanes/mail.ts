import type { LaneEdge } from '@/routes/board/lanes/edges'

/** Marks closer than this, on one edge of one lane, draw as one mark with a count. */
export const mergePx = 8

/** Agent mail on one lane at one moment: a dot where it was sent, a ring where it arrived. */
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

/** Each mail bundle as a mark on each drawn end, merged per lane edge within `mergePx`. */
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

/** One lane's mail with one other session in the window. */
export interface Conversation {
  /** The other lane; empty for a session outside the view. */
  lane: string
  run: string
  sent: number
  received: number
  unacked: number
  edges: LaneEdge[]
}

/** Every lane's conversations, busiest first, in one pass over the mail. */
export function conversationsByLane(edges: LaneEdge[]): Map<string, Conversation[]> {
  const byLane = new Map<string, Map<string, Conversation>>()
  const add = (lane: string, other: LaneEdge['from'], sent: boolean, edge: LaneEdge) => {
    if (!lane) return
    const mine = byLane.get(lane) ?? new Map<string, Conversation>()
    byLane.set(lane, mine)
    const key = other.lane || `outside:${other.run}`
    const c = mine.get(key) ?? { lane: other.lane, run: other.run, sent: 0, received: 0, unacked: 0, edges: [] }
    mine.set(key, c)
    const n = edge.messages!.ids.length
    if (sent) c.sent += n
    else c.received += n
    if (!edge.messages!.acked) c.unacked += n
    c.edges.push(edge)
  }
  for (const edge of edges) {
    if (edge.kind !== 'message' || !edge.messages) continue
    add(edge.from.lane, edge.to, true, edge)
    add(edge.to.lane, edge.from, false, edge)
  }
  return new Map([...byLane].map(([lane, list]) => [lane, [...list.values()].sort((a, b) => b.sent + b.received - (a.sent + a.received))]))
}

/** "messages: sent 3 to Reviewer and received 1 from Reviewer, 1 not acknowledged", for a lane's accessible name. */
export function conversationWords(conversations: Conversation[], nameOf: (c: Pick<Conversation, 'lane' | 'run'>) => string): string {
  if (conversations.length === 0) return ''
  const parts = conversations.map((c) => [
    c.sent > 0 && `sent ${c.sent} to ${nameOf(c)}`,
    c.received > 0 && `received ${c.received} from ${nameOf(c)}`,
  ].filter(Boolean).join(' and '))
  const unacked = conversations.reduce((n, c) => n + c.unacked, 0)
  return [`messages: ${parts.join(', ')}`, unacked > 0 && `${unacked} not acknowledged`].filter(Boolean).join(', ')
}

/** Counts seen from the focused lane, as in its popover: → sent to that lane, ← received from it. */
export function conversationTag(c: Pick<Conversation, 'sent' | 'received'>): string {
  return [c.sent > 0 && `→${c.sent}`, c.received > 0 && `←${c.received}`].filter(Boolean).join(' ')
}

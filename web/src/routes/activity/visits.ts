import type { Event } from '@/lib/types'

export interface Visit {
  kind: 'visit'
  id: string
  runID: string
  memberID: string
  time: string
  open: boolean
}

export type FeedItem = { kind: 'event'; event: Event } | Visit

/** Opening a run takes its control and leaving releases it, so each take and
 * the release that ends it fold into one visit. Oldest first in and out. */
export function foldVisits(events: readonly Event[]): FeedItem[] {
  const items: FeedItem[] = []
  const open = new Map<string, Visit>()
  for (const event of events) {
    if (event.type !== 'run.controller') {
      items.push({ kind: 'event', event })
      continue
    }
    const memberID = (event.payload as { member_id?: string } | null)?.member_id ?? ''
    const current = open.get(event.run_id)
    if (!memberID) {
      if (current) {
        current.open = false
        open.delete(event.run_id)
      } else items.push({ kind: 'event', event })
      continue
    }
    if (current?.memberID === memberID) continue
    if (current) current.open = false
    const visit: Visit = { kind: 'visit', id: event.id, runID: event.run_id, memberID, time: event.time, open: true }
    open.set(event.run_id, visit)
    items.push(visit)
  }
  return items
}

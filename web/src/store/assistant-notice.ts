import { toast } from 'sonner'
import type { AssistantNoticePayload, Event } from '@/lib/types'
import type { RootStore } from '@/store'

/**
 * The subscribe ack carries no sequence head, so nothing marks an event as
 * replayed. An event older than this on the server's clock is a catch-up.
 */
export const liveWindowMs = 30_000

const serverOffsets = new WeakMap<RootStore, number>()

/** Server time minus client time, read when server.info arrives, so a skewed client clock does not decide what is live. */
export function noteServerTime(store: RootStore, serverTime: string): void {
  const offset = Date.parse(serverTime) - Date.now()
  if (!Number.isNaN(offset)) serverOffsets.set(store, offset)
}

/** Server time minus client time: add it to Date.now() for the clock that stamps every event and session item. */
export function serverOffset(store: RootStore): number {
  return serverOffsets.get(store) ?? 0
}

/** Only the member the notice names reacts. Replayed notices count toward the badge but do not toast. */
export function applyAssistantNotice(store: RootStore, ev: Event): void {
  const p = ev.payload as AssistantNoticePayload
  const state = store.getState()
  if (state.info?.member.id !== p.member_id) return
  state.noteAssistantNotice()
  if (Date.now() + serverOffset(store) - Date.parse(ev.time) > liveWindowMs) return
  const show = p.kind === 'finished' ? toast : toast.warning
  show(p.title, {
    description: p.body || undefined,
    action: { label: 'Open run', onClick: () => store.getState().navigate('run', { runId: p.run_id }) },
  })
}

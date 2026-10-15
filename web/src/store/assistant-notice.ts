import { toast } from 'sonner'
import type { AssistantNoticePayload, Event } from '@/lib/types'
import type { RootStore } from '@/store'

/** The stream marks no event as replayed, so an event older than this is a catch-up after a reconnect. */
const liveWindowMs = 30_000

/** Only the member the notice names reacts. Replayed notices count toward the badge but do not toast. */
export function applyAssistantNotice(store: RootStore, ev: Event): void {
  const p = ev.payload as AssistantNoticePayload
  const state = store.getState()
  if (state.info?.member.id !== p.member_id) return
  state.noteAssistantNotice()
  if (Date.now() - Date.parse(ev.time) > liveWindowMs) return
  const show = p.kind === 'finished' ? toast : toast.warning
  show(p.title, {
    description: p.body || undefined,
    action: { label: 'Open run', onClick: () => store.getState().navigate('run', { runId: p.run_id }) },
  })
}

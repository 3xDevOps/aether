import { toast } from 'sonner'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { awaitingReview } from '@/lib/needs-you'
import type { RootStore } from '@/store'
import { capability } from '@/store/hooks'

/** Marks the open run seen: `finish_unopened` for any member, `outcome_unseen`
 * for its owner. Only the server's answer clears a flag, never this tab's ack.
 * A refusal is reported, not retried, until the member opens the run again. */
export function watchOutcomeSeen(
  store: RootStore,
  client: Api,
  doc: Document = document,
): () => void {
  const inFlight = new Set<string>()
  let tried: string | null = null

  const check = () => {
    const s = store.getState()
    const runID = s.route.params.runId
    const run = runID ? s.runs[runID] : undefined
    const owned = run?.member_id === s.info?.member.id
    if (!run || !(run.finish_unopened || (owned && awaitingReview(run)))) {
      tried = null
      return
    }
    if (tried === run.id || inFlight.has(run.id) || doc.hidden) return
    if (!capability(s.capabilities).hasMethod('run.seen')) return
    tried = run.id
    inFlight.add(run.id)
    const asked = run.stateChangedAt
    client.runSeen(run.id).then(
      (seen) => {
        inFlight.delete(run.id)
        // The answer is not ordered against the event stream. One from before
        // the run's latest status change says nothing about the flags that
        // change set, so it is dropped and the run asked about again.
        if (store.getState().runs[run.id]?.stateChangedAt !== asked) {
          if (tried === run.id) tried = null
          check()
          return
        }
        if (!seen.outcome_unseen) store.getState().applyOutcomeSeen(seen.id)
        if (!seen.finish_unopened) store.getState().applyFinishOpened(seen.id)
      },
      (err: unknown) => {
        inFlight.delete(run.id)
        toast.error(`Could not mark the run seen: ${errorSentence(err)}`)
      },
    )
  }

  const stopStore = store.subscribe(check)
  doc.addEventListener('visibilitychange', check)
  check()
  return () => {
    stopStore()
    doc.removeEventListener('visibilitychange', check)
  }
}

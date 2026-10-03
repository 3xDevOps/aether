import { toast } from 'sonner'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { awaitingReview } from '@/lib/status'
import type { RootStore } from '@/store'
import { capability } from '@/store/hooks'

/**
 * Calls `run.seen` when the run's owner reveals a run awaiting review, or is
 * already viewing it in a visible tab when the outcome arrives. Only the
 * server's answer clears the flag, never this tab's ack. One call per
 * reveal: a refusal is reported, not retried, until the owner opens the run
 * again.
 */
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
    if (!run || !awaitingReview(run)) {
      tried = null
      return
    }
    if (tried === run.id || inFlight.has(run.id) || doc.hidden) return
    if (run.member_id !== s.info?.member.id) return
    if (!capability(s.capabilities).hasMethod('run.seen')) return
    tried = run.id
    inFlight.add(run.id)
    client
      .runSeen(run.id)
      .then((seen) => {
        if (!seen.outcome_unseen) store.getState().applyOutcomeSeen(seen.id)
      })
      .catch((err: unknown) => {
        toast.error(`Could not mark the run seen: ${message(err)}`)
      })
      .finally(() => inFlight.delete(run.id))
  }

  const stopStore = store.subscribe(check)
  doc.addEventListener('visibilitychange', check)
  check()
  return () => {
    stopStore()
    doc.removeEventListener('visibilitychange', check)
  }
}

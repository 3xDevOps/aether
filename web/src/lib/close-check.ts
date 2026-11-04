import { api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { CloseOutcome, CloseWarningKind, Run, RunCloseCheck } from '@/lib/types'

/** How long a close waits on its advisory check before asking anyway. */
export const closeCheckDeadlineMs = 15_000

/** A failed or late read is the check's error, so a close never proceeds on a guess and never waits forever. */
export function readCloseCheck(runID: string, outcome?: CloseOutcome): Promise<RunCloseCheck> {
  const failed = (message: string): RunCloseCheck => ({ run_id: runID, warnings: [], error: { code: 0, message } })
  let timer: ReturnType<typeof setTimeout> | undefined
  const late = new Promise<RunCloseCheck>((resolve) => {
    timer = setTimeout(() => resolve(failed(`the server did not answer within ${closeCheckDeadlineMs / 1000} seconds`)), closeCheckDeadlineMs)
  })
  const read = api.runCloseCheck([runID], outcome).then(
    (runs) => runs.find((check) => check.run_id === runID) ?? failed('the server did not answer for this session'),
    (err: unknown) => failed(errorSentence(err)),
  )
  return Promise.race([read, late]).finally(() => clearTimeout(timer))
}

/** A merged close asserts the work is merged, as the server's check does. */
export function closeWarned(check: RunCloseCheck | undefined, outcome?: CloseOutcome): boolean {
  if (!check) return false
  return Boolean(check.error) || check.warnings.some((warning) => !(outcome === 'merged' && warning.kind === 'unmerged_work'))
}

/** What a close was asked against; a close is dropped when this changed before it runs, such as after Not yet. */
export function closeState(run: Pick<Run, 'status_seq' | 'status' | 'review' | 'state'> | undefined): string {
  return run ? `${run.status_seq}|${run.status}|${run.review ?? ''}|${run.state ?? ''}` : ''
}

export const closeBusyText = 'Another close is waiting for your answer, so this session was not closed'

// Mirrors archiveHolding in internal/sshd/close_check.go: what keeps a completed session from being archived.
const archiveHolding = new Set<CloseWarningKind>(['subsessions', 'pull_request', 'awaited', 'cleanup_pending'])

/** Why each completed session cannot be archived yet, keyed by run id; a failed read holds every session it covers. */
export async function readArchiveHolds(runIDs: string[]): Promise<Map<string, string[]>> {
  const held = new Map<string, string[]>()
  const batches = Array.from({ length: Math.ceil(runIDs.length / 50) }, (_, i) => runIDs.slice(i * 50, i * 50 + 50))
  await Promise.all(batches.map(async (ids) => {
    try {
      for (const check of await api.runCloseCheck(ids, 'completed')) {
        const reasons = check.error ? [check.error.message] : check.warnings.filter((w) => archiveHolding.has(w.kind)).map((w) => w.detail)
        if (reasons.length > 0) held.set(check.run_id, reasons)
      }
    } catch (err) {
      for (const id of ids) held.set(id, [errorSentence(err)])
    }
  }))
  return held
}

export const closeChangedText = 'The session changed while its close was being checked, so it was not closed'

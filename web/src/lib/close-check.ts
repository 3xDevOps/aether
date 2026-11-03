import { api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { CloseOutcome, Run, RunCloseCheck } from '@/lib/types'

/** A failed read is the check's error, so a close never proceeds on a guess. */
export function readCloseCheck(runID: string, outcome?: CloseOutcome): Promise<RunCloseCheck> {
  return api.runCloseCheck([runID], outcome).then(
    (runs) => runs.find((check) => check.run_id === runID) ?? { run_id: runID, warnings: [], error: { code: 0, message: 'the server did not answer for this session' } },
    (err: unknown) => ({ run_id: runID, warnings: [], error: { code: 0, message: errorSentence(err) } }),
  )
}

/** A merged close asserts the work is merged, as the server's check does. */
export function closeWarned(check: RunCloseCheck | undefined, outcome?: CloseOutcome): boolean {
  if (!check) return false
  return Boolean(check.error) || check.warnings.some((warning) => !(outcome === 'merged' && warning.kind === 'unmerged_work'))
}

/** What a close was asked against; a close is dropped when this changed before it runs, such as after Not yet. */
export function closeState(run: Pick<Run, 'status' | 'review' | 'state'> | undefined): string {
  return run ? `${run.status}|${run.review ?? ''}|${run.state ?? ''}` : ''
}

export const closeChangedText = 'The session changed while its close was being checked, so it was not closed'

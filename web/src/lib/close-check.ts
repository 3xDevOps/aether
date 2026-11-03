import { api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { RunCloseCheck } from '@/lib/types'

/** A failed read is the check's error, so a close never proceeds on a guess. */
export function readCloseCheck(runID: string): Promise<RunCloseCheck> {
  return api.runCloseCheck([runID]).then(
    (runs) => runs.find((check) => check.run_id === runID) ?? { run_id: runID, warnings: [], error: { code: 0, message: 'the server did not answer for this session' } },
    (err: unknown) => ({ run_id: runID, warnings: [], error: { code: 0, message: errorSentence(err) } }),
  )
}

/** A close the owner should confirm: something depends on the run, or nobody could tell. */
export function closeWarned(check: RunCloseCheck | undefined): boolean {
  return Boolean(check && (check.warnings.length > 0 || check.error))
}

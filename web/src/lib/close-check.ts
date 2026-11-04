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

/** Every session's check, keyed by run id, 50 to a call; a failed call is the error of every session it covers. */
export async function readCloseChecks(runIDs: string[], outcome?: CloseOutcome): Promise<Map<string, RunCloseCheck>> {
  const checks = new Map<string, RunCloseCheck>()
  const batches = Array.from({ length: Math.ceil(runIDs.length / 50) }, (_, i) => runIDs.slice(i * 50, i * 50 + 50))
  await Promise.all(batches.map(async (ids) => {
    try {
      for (const check of await api.runCloseCheck(ids, outcome)) checks.set(check.run_id, check)
    } catch (err) {
      for (const id of ids) checks.set(id, { run_id: id, warnings: [], error: { code: 0, message: errorSentence(err) } })
    }
  }))
  return checks
}

/** What keeps each completed session from being archived, keyed by run id; a failed read holds every session it covers. */
export async function readArchiveHolds(runIDs: string[]): Promise<Map<string, RunCloseCheck>> {
  const held = new Map<string, RunCloseCheck>()
  for (const check of (await readCloseChecks(runIDs, 'completed')).values()) {
    const warnings = check.warnings.filter((w) => archiveHolding.has(w.kind))
    if (check.error || warnings.some((w) => !w.subsession)) held.set(check.run_id, { ...check, warnings })
  }
  return held
}

/** The close an idle session gets: merged when a pull request of its own repository merged, else abandoned. */
export function idleOutcome(check: RunCloseCheck): 'merged' | 'abandoned' {
  return check.merged_pull_request ? 'merged' : 'abandoned'
}

export const closeChangedText = 'The session changed while its close was being checked, so it was not closed'

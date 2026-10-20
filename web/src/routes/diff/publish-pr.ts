import { useEffect } from 'react'
import { create } from 'zustand'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Run, RunGitExpected, RunPRCheck, RunPRFeedbackResult, RunPRTarget, RunPullRequest } from '@/lib/types'
import { completeTarget, defaultPRTarget, defaultPushTarget } from '@/routes/diff/publish-target'
import { useStore } from '@/store'

/** The server stores no pull request: every answer here is read from GitHub through the run's own `gh`. */
export interface TrackedPR {
  /** The target a member published to in the Publish dialog, when the checkout's own default would not find it. */
  chosen?: RunPRTarget
  /** What the last answer was asked for; asked again until the checkout moves. */
  asked?: { expected: RunGitExpected; target: RunPRTarget }
  feedback?: RunPRFeedbackResult
  error?: string
}

/** An answer that reaches this many entries of a kind may have left some out. */
export const feedbackLimit = 100

const none: TrackedPR = {}
const usePullRequests = create<Record<string, TrackedPR>>(() => ({}))
const revisions = new Map<string, number>()

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) usePullRequests.setState({}, true)
})

async function read(run: Run, client: Api): Promise<Partial<TrackedPR>> {
  const { chosen, asked } = usePullRequests.getState()[run.id] ?? none
  const ask = async (next: NonNullable<TrackedPR['asked']>) => client.runPRFeedback({ run_id: run.id, ...next, limit: feedbackLimit })
  // A failed read that found no pull request says nothing about the one on screen; a partial one carries its own error.
  const answer = (next: TrackedPR['asked'], feedback: RunPRFeedbackResult): Partial<TrackedPR> =>
    feedback.error && !feedback.pull_request ? { asked: next, error: feedback.error } : { asked: next, feedback, error: undefined }
  if (asked) {
    const feedback = await ask(asked)
    // A moved checkout may be on another branch, which is another pull request.
    if (!feedback.actual) return answer(asked, feedback)
  }
  const workspace = useStore.getState().workspaces[run.workspace_id]
  const [status, source] = await Promise.all([
    client.runGitStatus({ run_id: run.id }),
    // Without the source, the pull request defaults to the repository pushed to.
    client.workspaceMirrorStatus(run.workspace_id).then((mirror) => mirror.source_url, () => undefined),
  ])
  if (status.error) throw new Error(status.error)
  const target = chosen ?? defaultPRTarget(defaultPushTarget(status, workspace?.origin), run.base_branch || workspace?.base_branch || '', source)
  if (!status.branch || !status.head || !completeTarget(target)) return { asked: undefined, feedback: undefined, error: undefined }
  const next = { expected: { branch: status.branch, head: status.head }, target }
  return answer(next, await ask(next))
}

export async function refreshPullRequest(run: Run, client: Api = api): Promise<void> {
  const revision = (revisions.get(run.id) ?? 0) + 1
  revisions.set(run.id, revision)
  const identity = useStore.getState().identityKey
  const patch = await read(run, client).catch((cause): Partial<TrackedPR> => ({ error: message(cause) }))
  if (revisions.get(run.id) !== revision || useStore.getState().identityKey !== identity) return
  usePullRequests.setState((all) => ({ [run.id]: { ...all[run.id], ...patch } }))
}

export function trackPullRequest(run: Run, target: RunPRTarget, client: Api = api): Promise<void> {
  usePullRequests.setState((all) => ({ [run.id]: { ...all[run.id], chosen: target, asked: undefined } }))
  return refreshPullRequest(run, client)
}

const pollMs = 60_000

/** One poll per run however many surfaces watch it; a `run.pr` or integration event reads at once. */
const polls = new Map<string, { run: Run; watchers: number; timer?: number; stopped: boolean }>()

useStore.subscribe((state, previous) => {
  if (state.prSeq === previous.prSeq && state.integrationSeq === previous.integrationSeq) return
  for (const [runID, poll] of polls) {
    const mission = poll.run.mission_id
    if (state.prSeq[runID] !== previous.prSeq[runID] || (mission && state.integrationSeq[mission] !== previous.integrationSeq[mission])) {
      void refreshPullRequest(poll.run)
    }
  }
})

function watchPullRequest(run: Run): () => void {
  let poll = polls.get(run.id)
  if (!poll) {
    const started = { run, watchers: 0, timer: undefined as number | undefined, stopped: false }
    const tick = async () => {
      if (document.visibilityState === 'visible') await refreshPullRequest(run)
      if (!started.stopped) started.timer = window.setTimeout(() => void tick(), pollMs)
    }
    polls.set(run.id, started)
    void tick()
    poll = started
  }
  poll.watchers++
  const watching = poll
  return () => {
    if (--watching.watchers > 0) return
    watching.stopped = true
    clearTimeout(watching.timer)
    polls.delete(run.id)
  }
}

export function usePullRequest(run: Run, watch: boolean): TrackedPR {
  const tracked = usePullRequests((all) => all[run.id]) ?? none
  // The run's identity and base never change under one id.
  useEffect(() => (watch ? watchPullRequest(run) : undefined), [run.id, watch])
  return tracked
}

export type CheckState = 'failing' | 'running' | 'passing'

const passed = new Set(['success', 'neutral', 'skipped'])

/** Check runs report lowercase states, commit statuses the upper-case ones the server maps them to. */
export function checkState(check: RunPRCheck): CheckState {
  if (check.status.toLowerCase() !== 'completed') return 'running'
  return passed.has((check.conclusion ?? '').toLowerCase()) ? 'passing' : 'failing'
}

export function countChecks(checks: RunPRCheck[]): Record<CheckState, number> {
  const counts = { failing: 0, running: 0, passing: 0 }
  for (const check of checks) counts[checkState(check)]++
  return counts
}

export function stateLabel(pr: RunPullRequest): string {
  return `${pr.draft ? 'Draft · ' : ''}${pr.state.charAt(0).toUpperCase()}${pr.state.slice(1)}`
}

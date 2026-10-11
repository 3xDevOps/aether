import { useEffect } from 'react'
import { create } from 'zustand'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Run, RunGitExpected, RunPRCheck, RunPRFeedbackResult, RunPRTarget, RunPullRequest } from '@/lib/types'
import { completeTarget, defaultPRTarget, defaultPushTarget } from '@/routes/diff/publish-target'
import { useStore } from '@/store'

/** What the dashboard knows of a run's pull request. The server keeps none of
 * it: every answer is read from GitHub through the run's own `gh`. */
export interface TrackedPR {
  /** The target a member published to in the Publish dialog, when the checkout's own default would not find it. */
  chosen?: RunPRTarget
  /** What the last answer was asked for; asked again until the checkout moves. */
  asked?: { expected: RunGitExpected; target: RunPRTarget }
  feedback?: RunPRFeedbackResult
  error?: string
}

/** How many entries of each kind one answer may carry; an answer that reaches it may have left some out. */
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

/** Reads the run's pull request, its checks and its comments again. */
export async function refreshPullRequest(run: Run, client: Api = api): Promise<void> {
  const revision = (revisions.get(run.id) ?? 0) + 1
  revisions.set(run.id, revision)
  const identity = useStore.getState().identityKey
  const patch = await read(run, client).catch((cause): Partial<TrackedPR> => ({ error: message(cause) }))
  if (revisions.get(run.id) !== revision || useStore.getState().identityKey !== identity) return
  usePullRequests.setState((all) => ({ [run.id]: { ...all[run.id], ...patch } }))
}

/** Follows the pull request at `target` from now on, then reads it. */
export function trackPullRequest(run: Run, target: RunPRTarget, client: Api = api): Promise<void> {
  usePullRequests.setState((all) => ({ [run.id]: { ...all[run.id], chosen: target, asked: undefined } }))
  return refreshPullRequest(run, client)
}

const pollMs = 60_000

/** The run's pull request, read again every minute while `watch` holds and the page is visible. */
export function usePullRequest(run: Run, watch: boolean): TrackedPR {
  const tracked = usePullRequests((all) => all[run.id]) ?? none
  useEffect(() => {
    if (!watch) return
    let cancelled = false
    let timer: number | undefined
    const poll = async () => {
      if (document.visibilityState === 'visible') await refreshPullRequest(run)
      if (!cancelled) timer = window.setTimeout(() => void poll(), pollMs)
    }
    void poll()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
    // The run's identity and base never change under one id.
  }, [run.id, watch])
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

/** GitHub's `open`, `closed` or `merged` as a label, marking a draft. */
export function stateLabel(pr: RunPullRequest): string {
  return `${pr.draft ? 'Draft · ' : ''}${pr.state.charAt(0).toUpperCase()}${pr.state.slice(1)}`
}

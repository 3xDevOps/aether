import { useEffect } from 'react'
import { create } from 'zustand'
import { useShallow } from 'zustand/react/shallow'
import type { Tone } from '@/components/ui/status-dot'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Run, RunGitExpected, RunPRCheck, RunPRFeedbackResult, RunPRTarget, RunPullRequest } from '@/lib/types'
import { completeTarget, defaultPRTarget, defaultPushTarget } from '@/routes/diff/publish-target'
import { useStore } from '@/store'
import { coalesce } from '@/store/coalesce'

/** A run seen through one of its repos. A repo other than the primary carries its own workspace, branch and base. */
export type ScopedRun = Run & { repo?: string }

const prKey = (run: ScopedRun) => (run.repo ? `${run.id}:${run.repo}` : run.id)

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

async function read(run: ScopedRun, client: Api): Promise<Partial<TrackedPR>> {
  const { chosen, asked } = usePullRequests.getState()[prKey(run)] ?? none
  const ask = async (next: NonNullable<TrackedPR['asked']>) => client.runPRFeedback({ run_id: run.id, ...repoParam(run), ...next, limit: feedbackLimit })
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
    client.runGitStatus({ run_id: run.id, ...repoParam(run) }),
    // Without the source, the pull request defaults to the repository pushed to.
    client.workspaceMirrorStatus(run.workspace_id).then((mirror) => mirror.source_url, () => undefined),
  ])
  if (status.error) throw new Error(status.error)
  const target = chosen ?? defaultPRTarget(defaultPushTarget(status, workspace?.origin), run.base_branch || workspace?.base_branch || '', source)
  if (!status.branch || !status.head || !completeTarget(target)) return { asked: undefined, feedback: undefined, error: undefined }
  const next = { expected: { branch: status.branch, head: status.head }, target }
  return answer(next, await ask(next))
}

export async function refreshPullRequest(run: ScopedRun, client: Api = api): Promise<void> {
  const key = prKey(run)
  const revision = (revisions.get(key) ?? 0) + 1
  revisions.set(key, revision)
  const identity = useStore.getState().identityKey
  const patch = await read(run, client).catch((cause): Partial<TrackedPR> => ({ error: message(cause) }))
  if (revisions.get(key) !== revision || useStore.getState().identityKey !== identity) return
  usePullRequests.setState((all) => ({ [key]: { ...all[key], ...patch } }))
}

export function trackPullRequest(run: ScopedRun, target: RunPRTarget, client: Api = api): Promise<void> {
  const key = prKey(run)
  usePullRequests.setState((all) => ({ [key]: { ...all[key], chosen: target, asked: undefined } }))
  return refreshPullRequest(run, client)
}

/** The `repo` param of a call about one of the run's non-primary checkouts. */
export const repoParam = (run: ScopedRun): { repo?: string } => (run.repo ? { repo: run.repo } : {})

const pollMs = 60_000

/** One poll per run however many surfaces watch it; a `run.pr`, `git.branch` or integration event reads at once. */
const polls = new Map<string, { run: ScopedRun; watchers: number; timer?: number; stopped: boolean }>()
const pollListeners = new Set<(runID: string) => void>()

/** Hears each 60s poll of a watched run, for reads that ride on it. */
export function onPullRequestPoll(listener: (runID: string) => void): () => void {
  pollListeners.add(listener)
  return () => pollListeners.delete(listener)
}

useStore.subscribe((state, previous) => {
  if (state.prSeq === previous.prSeq && state.gitSeq === previous.gitSeq && state.integrationSeq === previous.integrationSeq) return
  for (const [key, poll] of polls) {
    const runID = poll.run.id
    const mission = poll.run.mission_id
    if (state.prSeq[runID] !== previous.prSeq[runID] || state.gitSeq[runID] !== previous.gitSeq[runID] || (mission && state.integrationSeq[mission] !== previous.integrationSeq[mission])) {
      coalesce(polls, key, () => refreshPullRequest(poll.run))
    }
  }
})

function watchPullRequest(run: ScopedRun): () => void {
  const key = prKey(run)
  let poll = polls.get(key)
  if (!poll) {
    const started = { run, watchers: 0, timer: undefined as number | undefined, stopped: false }
    const tick = async () => {
      if (document.visibilityState === 'visible') {
        for (const listener of pollListeners) listener(run.id)
        await refreshPullRequest(run)
      }
      if (!started.stopped) started.timer = window.setTimeout(() => void tick(), pollMs)
    }
    polls.set(key, started)
    void tick()
    poll = started
  }
  poll.watchers++
  const watching = poll
  return () => {
    if (--watching.watchers > 0) return
    watching.stopped = true
    clearTimeout(watching.timer)
    polls.delete(key)
  }
}

export function usePullRequest(run: ScopedRun, watch: boolean): TrackedPR {
  const key = prKey(run)
  const tracked = usePullRequests((all) => all[key]) ?? none
  // The run's identity and base never change under one id.
  useEffect(() => (watch ? watchPullRequest(run) : undefined), [key, watch])
  return tracked
}

/** Every repo of a multi-repo run in one subscription; `runs` is as long as the run has repos. */
export function usePullRequestList(runs: ScopedRun[], watch: boolean): TrackedPR[] {
  const keys = runs.map(prKey)
  const tracked = usePullRequests(useShallow((all) => keys.map((key) => all[key] ?? none)))
  const joined = keys.join()
  useEffect(() => {
    if (!watch) return
    const stops = runs.map(watchPullRequest)
    return () => stops.forEach((stop) => stop())
  }, [joined, watch])
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

/** `short` is what a narrow header keeps of `text`. An answer that failed part-way or stopped at its
 * limit may be missing checks, so it can report a failure it saw but never that all of them passed. */
export function summarizePR(pr: RunPullRequest, feedback: RunPRFeedbackResult): { tone: Tone; text: string; short?: string } {
  if (pr.state === 'merged') return { tone: 'done', text: 'Merged' }
  if (pr.state === 'closed') return { tone: 'neutral', text: 'Closed' }
  const checks = feedback.checks ?? []
  const counts = countChecks(checks)
  if (counts.failing) return { tone: 'failed', text: `${counts.failing} failing`, short: String(counts.failing) }
  if (feedback.error || (feedback.truncated && checks.length >= feedbackLimit)) return { tone: 'neutral', text: 'Checks incomplete' }
  if (counts.running) return { tone: 'working', text: 'Checks running' }
  if (counts.passing) return { tone: 'done', text: 'Checks passed' }
  return { tone: 'neutral', text: 'No checks' }
}

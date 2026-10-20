import { useEffect } from 'react'
import { create } from 'zustand'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { RunGitLogResult } from '@/lib/types'
import { onPullRequestPoll } from '@/routes/diff/publish-pr'
import { useStore } from '@/store'
import { coalesce } from '@/store/coalesce'

export interface TrackedLog {
  log?: RunGitLogResult
  /** The last read's failure, kept beside the log it could not replace. */
  error?: string
  reading: boolean
}

const none: TrackedLog = { reading: false }
const useGitLogs = create<Record<string, TrackedLog>>(() => ({}))
const owner = {}

/** Runs some card shows, with how many cards show each. */
const watched = new Map<string, number>()

// A fetch moves the base without a git.branch event, so the log also rides the pull request's poll.
onPullRequestPoll((runID) => {
  if (watched.has(runID)) refreshGitLog(runID)
})

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useGitLogs.setState({}, true)
  if (state.gitSeq === previous.gitSeq && state.integrationSeq === previous.integrationSeq) return
  for (const runID of watched.keys()) {
    const mission = state.runs[runID]?.mission_id
    if (state.gitSeq[runID] !== previous.gitSeq[runID] || (mission && state.integrationSeq[mission] !== previous.integrationSeq[mission])) {
      refreshGitLog(runID)
    }
  }
})

export const logLimit = 50

export function refreshGitLog(runID: string, client: Api = api): void {
  useGitLogs.setState((all) => ({ [runID]: { ...all[runID], reading: true } }))
  coalesce(owner, runID, async () => {
    const identity = useStore.getState().identityKey
    const patch = await client.runGitLog({ run_id: runID, limit: logLimit }).then(
      (log): Partial<TrackedLog> => (log.error && !log.commits && !log.head ? { error: log.error } : { log, error: undefined }),
      (cause): Partial<TrackedLog> => ({ error: message(cause) }),
    )
    if (useStore.getState().identityKey !== identity) return
    useGitLogs.setState((all) => ({ [runID]: { ...all[runID], ...patch, reading: false } }))
  })
}

/** Read once when the first card shows the run, then on each branch or integration event and each pull request poll. */
export function useGitLog(runID: string, watch: boolean): TrackedLog {
  const tracked = useGitLogs((all) => all[runID]) ?? none
  useEffect(() => {
    if (!watch) return
    const count = watched.get(runID) ?? 0
    watched.set(runID, count + 1)
    if (count === 0) refreshGitLog(runID)
    return () => {
      const left = (watched.get(runID) ?? 1) - 1
      if (left > 0) watched.set(runID, left)
      else watched.delete(runID)
    }
  }, [runID, watch])
  return tracked
}

/** Commits the run branch has that its base does not, once read. */
export function useAhead(runID: string): number {
  return useGitLogs((all) => all[runID]?.log?.ahead ?? 0)
}

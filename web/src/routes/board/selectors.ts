// Board shape: run cards dealt into Orca's four buckets. Pure over a narrow
// input so the component can memoize on exactly what it reads.

import { useMemo } from 'react'
import { runState, type PresentationState } from '@/lib/status'
import type { Member, Session } from '@/lib/types'
import { useStore } from '@/store'
import { isUnseen, type Ack } from '@/store/board'
import { usePendingApprovalRuns } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

export type Bucket = 'needs-you' | 'working' | 'done'

export interface BoardCard {
  run: RunRecord
  state: PresentationState
  owner?: Member
  session?: Session
  unseen: boolean
  paused: boolean
}

export interface BoardColumn {
  key: Bucket
  label: string
  cards: BoardCard[]
}

/** A session with nothing running: the Idle column, hidden by default. */
export interface IdleSession {
  session: Session
  runs: number
  changedAt: string
}

export interface BoardInput {
  sessions: Record<string, Session>
  runs: Record<string, RunRecord>
  members: Record<string, Member>
  acked: Record<string, Ack>
  pausedRuns: Record<string, boolean>
  /** Runs holding a pending approval, pre-derived so its identity is stable. */
  pending: Set<string>
}

export const bucketLabel: Record<Bucket | 'idle', string> = {
  'needs-you': 'Needs You',
  working: 'Working',
  done: 'Done',
  idle: 'Idle',
}

/**
 * Lifecycle to bucket. `needs-attention` covers both stalls and clean exits
 * waiting on run.close - the reason string on the card tells them apart.
 */
export function bucketOf(state: PresentationState): Bucket {
  switch (state) {
    case 'needs-attention':
      return 'needs-you'
    case 'working':
    case 'waiting':
      return 'working'
    default:
      return 'done'
  }
}

export interface BoardData {
  columns: BoardColumn[]
  idle: IdleSession[]
}

const at = (iso: string) => Date.parse(iso)

export function board(s: BoardInput): BoardData {
  const columns: Record<Bucket, BoardCard[]> = {
    'needs-you': [],
    working: [],
    done: [],
  }
  const activeSessions = new Set<string>()
  const runCounts = new Map<string, number>()
  const sessionChanged = new Map<string, string>()

  for (const run of Object.values(s.runs)) {
    const state = runState(run.status, s.pending.has(run.id))
    const bucket = bucketOf(state)
    columns[bucket].push({
      run,
      state,
      owner: s.members[run.member_id],
      session: s.sessions[run.session_id],
      unseen: isUnseen(s.acked, run),
      paused: state === 'working' && s.pausedRuns[run.id] === true,
    })
    if (bucket !== 'done') activeSessions.add(run.session_id)
    runCounts.set(run.session_id, (runCounts.get(run.session_id) ?? 0) + 1)
    const seen = sessionChanged.get(run.session_id)
    if (!seen || at(run.stateChangedAt) > at(seen)) {
      sessionChanged.set(run.session_id, run.stateChangedAt)
    }
  }

  const newestFirst = (a: BoardCard, b: BoardCard) =>
    at(b.run.stateChangedAt) - at(a.run.stateChangedAt)

  const idle = Object.values(s.sessions)
    .filter((session) => !activeSessions.has(session.id))
    .map((session) => ({
      session,
      runs: runCounts.get(session.id) ?? 0,
      changedAt: sessionChanged.get(session.id) ?? session.created_at,
    }))
    .sort((a, b) => at(b.changedAt) - at(a.changedAt))

  return {
    columns: (Object.keys(columns) as Bucket[]).map((key) => ({
      key,
      label: bucketLabel[key],
      cards: columns[key].sort(newestFirst),
    })),
    idle,
  }
}

export function useBoard(): BoardData {
  const sessions = useStore((s) => s.sessions)
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const acked = useStore((s) => s.acked)
  const pausedRuns = useStore((s) => s.pausedRuns)
  const pending = usePendingApprovalRuns()
  return useMemo(
    () => board({ sessions, runs, members, acked, pausedRuns, pending }),
    [sessions, runs, members, acked, pausedRuns, pending],
  )
}

import { useEffect, useMemo, useRef, useState } from 'react'
import { loadMission, swarmHandle } from '@/components/messages/handles'
import { api } from '@/lib/api'
import { useSecondClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { runLabel } from '@/lib/status'
import type { MissionAttemptState } from '@/lib/types'
import { taskTitle } from '@/routes/missions/tree'
import { useStore } from '@/store'
import { serverOffset } from '@/store/assistant-notice'
import type { RunRecord } from '@/store/runs'
import { loadOlderItems } from '@/store/session-stream'
import { noAgents } from '@/store/subagents'
import { costTotals, deriveTrace, traceStats, type SpanStatus, type WorkerSource } from '@/store/trace'
import { axisAt, buildAxis, gapLabel, idleGaps, type Axis, type Gap } from '@/store/trace-axis'
import { readTraceLog } from '@/store/trace-log'
import { missionSeqOf } from '@/store/workflow-signals'
import { descendantRuns } from '@/store/selectors'

const liveStatuses = new Set(['queued', 'provisioning', 'running', 'needs-attention'])

/** Older history pages the view reads on its own before it leaves a Load earlier button. */
export const autoPages = 5

const attemptStatus: Record<MissionAttemptState, SpanStatus> = {
  reserved: 'running',
  launching: 'running',
  running: 'running',
  unknown: 'stopped',
  submitted: 'done',
  completed: 'done',
  failed: 'failed',
  cancelled: 'stopped',
  superseded: 'stopped',
  abandoned: 'stopped',
}

function childStatus(status: RunRecord['status']): SpanStatus {
  if (liveStatuses.has(status)) return 'running'
  return status === 'failed' || status === 'interrupted' ? 'failed' : status === 'abandoned' ? 'stopped' : 'done'
}

function useWorkers(run: RunRecord): { workers: WorkerSource[] | undefined; error: string | null; retry: () => void } {
  const missionID = run.mission_role === 'integrator' ? run.mission_id : undefined
  const detail = useStore((s) => (missionID ? s.missionDetails[missionID] : undefined))
  const changed = useStore(missionSeqOf(missionID ?? ''))
  const [error, setError] = useState<string | null>(null)
  const [tries, setTries] = useState(0)
  useEffect(() => {
    if (!missionID) return
    let live = true
    loadMission(missionID).then(() => live && setError(null), (err: unknown) => live && setError(message(err)))
    return () => {
      live = false
    }
  }, [missionID, changed, tries])
  const childKey = useStore((s) => JSON.stringify(descendantRuns(s.runs, [run.id]).filter((child) => !child.detached).map((child) => [
    child.id, runLabel(child), child.started_at ?? child.created_at, child.finished_at, child.status,
  ])))
  const children = useMemo(() => (JSON.parse(childKey) as [string, string, string, string | null, RunRecord['status']][]).map(([runID, label, start, finished, status]): WorkerSource => ({
    runID,
    label,
    start: Date.parse(start),
    end: finished ? Date.parse(finished) : null,
    status: childStatus(status),
  })), [childKey])
  const attempts = useMemo(() => detail?.attempts.flatMap((attempt) => {
    if (attempt.run_id === run.id) return []
    const handle = swarmHandle(detail, attempt.run_id)
    const task = detail.tasks.find((item) => item.id === attempt.task_id)
    const from = attempt.started_at ?? attempt.reserved_at
    return [{
      runID: attempt.run_id,
      label: [handle?.name ?? `Worker ${attempt.number}`, task && taskTitle(task)].filter(Boolean).join(' · '),
      start: Date.parse(from),
      end: attempt.finished_at ? Date.parse(attempt.finished_at) : null,
      status: attemptStatus[attempt.state],
    }]
  }), [detail, run.id])
  const workers = useMemo(() => (attempts || children.length > 0 ? [...(attempts ?? []), ...children] : undefined), [attempts, children])
  return { workers, error, retry: () => setTries((n) => n + 1) }
}

function sameGap(a: Gap, b: Gap): boolean {
  return a.from === b.from && a.unknown === b.unknown && a.open === b.open && (a.open ? gapLabel(a) === gapLabel(b) : a.to === b.to)
}

function sameShape(a: Axis, b: Axis): boolean {
  return a.start === b.start && a.breakMs === b.breakMs && a.length === b.length && a.gaps.length === b.gaps.length &&
    a.gaps.every((gap, i) => sameGap(gap, b.gaps[i]!))
}

/** Keeps the last axis while its start, breaks and length hold, so rows keep their geometry as the clock ticks; an open break changes it only when its label does. */
function useStableAxis(next: Axis): Axis {
  const held = useRef(next)
  if (!sameShape(held.current, next)) held.current = next
  return held.current
}

export function useTraceData(run: RunRecord) {
  const session = useStore((s) => s.acpSessions[run.id])
  const log = useStore((s) => s.traceLogs[run.id])
  const connected = useStore((s) => s.connection === 'live')
  const live = liveStatuses.has(run.status)
  const tick = useSecondClock(live)
  const now = tick + serverOffset(useStore)
  const { workers, error: workersError, retry: retryWorkers } = useWorkers(run)

  useEffect(() => {
    if (connected) void readTraceLog(useStore, api, run)
  }, [connected, run.id, run.workspace_id])

  const generation = session?.historyGeneration
  const paged = useStore((s) => s.traceViews[run.id]?.paged)
  const pages = paged && paged.generation === generation ? paged.count : 0
  useEffect(() => {
    if (!session?.more || session.olderLoading || session.olderError || pages >= autoPages) return
    useStore.getState().setTraceView(run.id, { paged: { generation, count: pages + 1 } })
    void loadOlderItems(useStore, api, run.id)
  }, [session?.more, session?.olderLoading, session?.olderError, generation, pages, run.id])

  const events = log?.events
  const held = Boolean(log?.read) && !log?.error
  const statusEvents = useMemo(() => (held ? events!.filter((event) => event.type === 'run.status') : null), [held, events])
  const cost = useMemo(() => costTotals(events?.filter((event) => event.type === 'run.cost') ?? []), [events])
  const start = Date.parse(run.started_at ?? run.created_at)
  const end = run.finished_at && !live ? Date.parse(run.finished_at) : null
  const turns = session?.turns
  const agents = session?.agents ?? noAgents
  const statusError = log?.error
  const root = useMemo(() => deriveTrace({
    start,
    end,
    runStatus: run.status,
    runState: run.state,
    live,
    turns: turns ?? [],
    agents,
    statusEvents,
    statusError,
    workers,
  }), [start, end, run.status, run.state, live, turns, agents, statusEvents, statusError, workers])
  const stats = useMemo(() => traceStats(root), [root])
  const partial = Boolean(session?.more || session?.truncatedBefore)
  const gaps = idleGaps(root, now, partial)
  const axis = useStableAxis(buildAxis(root.start, root.end ?? Math.max(now, root.start), gaps, live))
  return {
    root,
    stats,
    cost,
    log,
    session,
    live,
    now,
    nowA: axisAt(axis, Math.max(now, root.start)),
    axis,
    paged: pages >= autoPages,
    workersError,
    retryWorkers,
  }
}

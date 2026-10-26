import type { CoordMessageAckedPayload, CoordMessagePayload, Event, MissionSubmission, OverlapPeer, RunMessageKind } from '@/lib/types'
import type { TimeWindow } from '@/routes/board/lanes/axis'
import type { BoardCard } from '@/routes/board/selectors'
import type { LaneEdgeKind } from '@/store/lanes'
import type { MissionDetail } from '@/store/missions'

export interface EdgeEnd {
  lane: string
  run: string
  t: number
}

export interface LaneEdge {
  key: string
  kind: LaneEdgeKind
  from: EdgeEnd
  to: EdgeEnd
  /** Orchestration across owners draws dashed, as on Map. */
  crossOwner?: boolean
  submission?: MissionSubmission
  /** Bundled mail: every message id, its sending runs, whether all were acknowledged, and the kind when they share one. */
  messages?: { ids: string[]; senders: string[]; acked: boolean; kind?: RunMessageKind; correlation?: string }
  files?: string[]
}

export interface EdgesInput {
  cards: Map<string, BoardCard>
  drawn: Map<string, string>
  details: Record<string, MissionDetail>
  mail: Mail
  window: TimeWindow
  now: number
  /** Mail closer than this in time, between the same two lanes, is one edge. */
  bucket: number
  /** The run whose overlaps are shown. */
  focus?: string
  overlaps: Record<string, OverlapPeer[]>
  hidden: ReadonlySet<LaneEdgeKind>
}

const at = (iso: string | null | undefined) => (iso ? Date.parse(iso) : undefined)

export interface Mail {
  sent: { t: number; payload: CoordMessagePayload }[]
  acked: Set<string>
}

/** Agent mail from held history, parsed once per history change rather than on every edge pass. */
export function mailOf(events: Event[]): Mail {
  const sent: Mail['sent'] = []
  const acked = new Set<string>()
  for (const event of events) {
    if (event.type === 'coord.message') sent.push({ t: Date.parse(event.time), payload: event.payload as CoordMessagePayload })
    else if (event.type === 'coord.message.acked') acked.add((event.payload as CoordMessageAckedPayload).message_id)
  }
  return { sent, acked }
}

function latestAttemptRun(detail: MissionDetail, taskID: string, cards: Map<string, BoardCard>): BoardCard | undefined {
  const attempts = detail.attempts.filter((a) => a.task_id === taskID && cards.has(a.run_id)).sort((a, b) => b.number - a.number)
  const accepted = detail.submissions.find((s) => s.task_id === taskID && s.state === 'accepted')
  const pick = attempts.find((a) => a.id === accepted?.attempt_id) ?? attempts[0]
  return pick && cards.get(pick.run_id)
}

/** Every relationship between drawn lanes; an edge whose ends share one lane is dropped. */
export function laneEdges(input: EdgesInput): LaneEdge[] {
  const { cards, drawn, details, hidden } = input
  const out: LaneEdge[] = []
  const end = (run: string, t: number): EdgeEnd | undefined => {
    const lane = drawn.get(run)
    return lane ? { lane, run, t } : undefined
  }
  const add = (edge: Omit<LaneEdge, 'from' | 'to'>, from: EdgeEnd | undefined, to: EdgeEnd | undefined) => {
    if (hidden.has(edge.kind) || !from || !to || from.lane === to.lane) return
    if (Math.max(from.t, to.t) < input.window.start || Math.min(from.t, to.t) > input.window.end) return
    out.push({ ...edge, from, to })
  }

  for (const card of cards.values()) {
    const { run } = card
    if (run.mission_role !== 'worker' || !run.mission_id || !run.integrator_run_id) continue
    const integrator = cards.get(run.integrator_run_id)?.run
    if (integrator?.mission_role !== 'integrator' || integrator.mission_id !== run.mission_id || integrator.workspace_id !== run.workspace_id) continue
    const reserved = at(details[run.mission_id]?.attempts.find((a) => a.run_id === run.id)?.reserved_at)
    const launched = Date.parse(run.created_at)
    add(
      { key: `orchestration:${run.id}`, kind: 'orchestration', crossOwner: integrator.member_id !== run.member_id },
      end(integrator.id, Math.min(reserved ?? launched, launched)),
      end(run.id, launched),
    )
  }

  for (const detail of Object.values(details)) {
    for (const task of detail.tasks) {
      const target = latestAttemptRun(detail, task.id, cards)
      for (const dependency of task.dependencies ?? []) {
        const source = latestAttemptRun(detail, dependency.depends_on_task_id, cards)
        const done = at(source?.run.finished_at)
        if (!source || !target || done === undefined) continue
        const attempt = detail.attempts.find((a) => a.run_id === target.run.id)
        add(
          { key: `dependency:${dependency.depends_on_task_id}:${task.id}`, kind: 'dependency' },
          end(source.run.id, done),
          end(target.run.id, at(attempt?.started_at) ?? at(target.run.started_at) ?? Date.parse(target.run.created_at)),
        )
      }
    }
    for (const submission of detail.submissions) {
      const t = Date.parse(submission.created_at)
      const integrator = submission.decision_by_run_id || detail.mission.current_integrator_run_id
      add({ key: `submission:${submission.id}`, kind: 'submission', submission }, end(submission.proposed_by_run_id, t), end(integrator, t))
    }
  }

  if (!hidden.has('message')) {
    const { acked } = input.mail
    const bundles = new Map<string, LaneEdge>()
    for (const { t, payload: p } of input.mail.sent) {
      const from = end(p.from_run_id, t)
      const to = end(p.to_run_id, t)
      if (!from || !to || from.lane === to.lane || t < input.window.start || t > input.window.end) continue
      const key = `message:${from.lane}>${to.lane}@${Math.floor(t / input.bucket)}`
      const bundle = bundles.get(key)
      if (!bundle) {
        bundles.set(key, { key, kind: 'message', from, to, messages: { ids: [p.message_id], senders: [p.from_run_id], acked: acked.has(p.message_id), kind: p.kind, correlation: p.correlation_id } })
        continue
      }
      const m = bundle.messages!
      m.ids.push(p.message_id)
      if (!m.senders.includes(p.from_run_id)) m.senders.push(p.from_run_id)
      m.acked &&= acked.has(p.message_id)
      if (m.kind !== p.kind) m.kind = undefined
      if (m.correlation !== p.correlation_id) m.correlation = undefined
    }
    out.push(...bundles.values())
  }

  if (input.focus) {
    for (const peer of input.overlaps[input.focus] ?? []) {
      add({ key: `overlap:${input.focus}:${peer.run_id}`, kind: 'overlap', files: peer.files }, end(input.focus, input.now), end(peer.run_id, input.now))
    }
  }
  return out
}

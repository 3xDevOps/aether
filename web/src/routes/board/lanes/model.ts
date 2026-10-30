import { dequal } from 'dequal'
import type { Tone } from '@/components/ui/status-dot'
import { swarmHandle } from '@/components/messages/handles'
import type { StateContext } from '@/lib/needs-you'
import { endedBadly, groupLabel, outcomeState, runLabel, type PresentationState, type RunGroup } from '@/lib/status'
import type { Member } from '@/lib/types'
import { rootKey, runKey, runParents } from '@/routes/board/map-layout'
import type { TimeWindow } from '@/routes/board/lanes/axis'
import { runSegments, segmentTone, strongest, type Segment, type StatusChange, type Stretch } from '@/routes/board/lanes/segments'
import type { BoardCard } from '@/routes/board/selectors'
import { missionFinal, objectiveTitle, phaseWord } from '@/routes/missions/swarm'
import type { LaneGroup } from '@/store/lanes'
import type { MissionDetail } from '@/store/missions'
import { agentLabel, agentState, type AgentIndex, type AgentState } from '@/store/subagents'

interface LaneBase {
  key: string
  /** The tree depth: sections are 1. */
  level: number
  parentKey?: string
}

export interface SectionLane extends LaneBase {
  kind: 'section'
  label: string
  count: number
  open: boolean
  /** Runs in this section that ended before the window. */
  before: number
  member?: Member
}

export interface SummaryLane extends LaneBase {
  kind: 'summary'
  label: string
  strip: Stretch[]
}

export interface SwarmLane extends LaneBase {
  kind: 'swarm'
  /** Absent on the group of a run and its child runs. */
  missionID?: string
  title: string
  detail: string
  open: boolean
  /** Undefined when neither the swarm nor a listed run names its integrator; a child-run group's is its parent. */
  integratorID?: string
  strip: Stretch[]
  /** The current integrator's bar, drawn while the swarm is folded. */
  segments: Segment[]
  outcome?: PresentationState
}

export interface RunLane extends LaneBase {
  kind: 'run'
  card: BoardCard
  title: string
  handle?: string
  segments: Segment[]
  /** Set once the run ended, from its status: the viewer's state can read Ready for review instead. */
  outcome?: PresentationState
  start: number
  end: number
  /** Undefined when the run has no subagent sub-lanes to offer. */
  agentsOpen?: boolean
}

export interface AgentLane extends LaneBase {
  kind: 'agent'
  runID: string
  agentID: string
  label: string
  state: AgentState
  /** Unknown when the launch is older than the items read. */
  start?: number
  end: number
}

export interface NoteLane extends LaneBase {
  kind: 'note'
  runID: string
  text: string
  retry: boolean
}

export type Lane = SectionLane | SummaryLane | SwarmLane | RunLane | AgentLane | NoteLane

export type AgentLoad =
  | { status: 'loading' }
  | { status: 'error'; error: string }
  /** `partial` when the index came from one history page rather than the whole live session. */
  | { status: 'ready'; index: AgentIndex; partial: boolean }

export interface LanesInput {
  cards: BoardCard[]
  ctx: StateContext
  changes: Map<string, StatusChange[]>
  /** The time before which a workspace's history was not read. */
  floor: (workspaceID: string) => number
  window: TimeWindow
  now: number
  group: LaneGroup
  /** Disclosure choices by lane key; absent keys take their default. */
  open: ReadonlyMap<string, boolean>
  /** Run ids whose subagents are shown, with what is loaded for them. */
  agents: Readonly<Record<string, AgentLoad>>
  /** Enhanced runs can show subagents. */
  canShowAgents: (card: BoardCard) => boolean
  archived: boolean
}

export interface LanesModel {
  lanes: Lane[]
  /** The lane each run is drawn on: its own, or the folded swarm or section that holds it. */
  drawn: Map<string, string>
  /** Runs whose bars reach into the window. */
  inWindow: number
}

export const sectionOrder: RunGroup[] = ['needs-you', 'working', 'review', 'finished']
const groupRank = Object.fromEntries(sectionOrder.map((group, i) => [group, i])) as Record<RunGroup, number>
const swarmTasksOpen = 8
const agentDepth = 4

interface Unit {
  key: string
  missionID?: string
  root: BoardCard
  members: RunLane[]
  group: RunGroup
}

const parse = (iso: string | null | undefined) => (iso ? Date.parse(iso) : undefined)

function runLane(card: BoardCard, input: LanesInput, detail: MissionDetail | undefined): RunLane {
  const { run } = card
  const handle = run.mission_id ? swarmHandle(detail, run.id) : undefined
  const outcome = outcomeState(run.status)
  const start = Date.parse(run.created_at)
  return {
    kind: 'run',
    key: `run:${run.workspace_id}:${run.id}`,
    level: 2,
    card,
    title: handle?.task ?? runLabel(run),
    handle: handle?.name ?? (run.mission_role === 'integrator' ? 'Integrator' : run.mission_role === 'worker' ? 'Worker' : undefined),
    segments: runSegments({
      run,
      shown: card.state,
      changes: input.changes.get(run.id) ?? [],
      now: input.now,
      floor: input.floor(run.workspace_id),
    }),
    outcome,
    start,
    end: Math.max(start, parse(run.finished_at) ?? input.now),
  }
}

/** Integrators first, the current one leading; then workers in task order, retries after their first attempt. */
function memberOrder(detail: MissionDetail | undefined) {
  const taskIndex = new Map(detail?.tasks.map((task, i) => [task.id, i]))
  const attempt = new Map(detail?.attempts.map((item) => [item.run_id, item]))
  const rank = (lane: RunLane) => {
    const { run } = lane.card
    if (run.mission_role === 'integrator') return [0, run.id === detail?.mission.current_integrator_run_id ? 0 : 1, -lane.start]
    const a = attempt.get(run.id)
    return a ? [1, taskIndex.get(a.task_id) ?? Infinity, a.number] : [2, lane.start, 0]
  }
  return (a: RunLane, b: RunLane) => {
    const x = rank(a)
    const y = rank(b)
    return x[0] - y[0] || x[1] - y[1] || x[2] - y[2] || a.key.localeCompare(b.key)
  }
}

function units(input: LanesInput): Unit[] {
  const parents = runParents(input.cards)
  const byKey = new Map(input.cards.map((card) => [runKey(card), card]))
  const swarms = new Map<string, Unit>()
  const families = new Map<BoardCard, RunLane[]>()
  const out: Unit[] = []
  for (const card of input.cards) {
    const root = byKey.get(rootKey(runKey(card), parents))!
    const { mission_id: missionID, mission_role: role, workspace_id: workspace } = root.run
    const lane = runLane(card, input, missionID ? input.ctx.missionDetails[missionID] : undefined)
    if (!missionID || !role) {
      if (root === card) out.push({ key: lane.key, root: card, members: [lane], group: card.group })
      else families.set(root, [...(families.get(root) ?? []), lane])
      continue
    }
    const key = `swarm:${workspace}:${missionID}`
    const unit = swarms.get(key)
    if (unit) unit.members.push(lane)
    else swarms.set(key, { key, missionID, root: card, members: [lane], group: card.group })
  }
  for (const unit of out) {
    const children = families.get(unit.root)
    if (!children) continue
    children.sort((a, b) => a.start - b.start || a.key.localeCompare(b.key))
    unit.members.push(...children)
    unit.group = bestGroup(unit.members)
  }
  for (const unit of swarms.values()) {
    unit.members.sort(memberOrder(input.ctx.missionDetails[unit.missionID!]))
    unit.root = unit.members[0].card
    unit.group = bestGroup(unit.members)
    out.push(unit)
  }
  return out
}

const bestGroup = (members: RunLane[]) =>
  members.reduce<RunGroup>((best, lane) => (groupRank[lane.card.group] < groupRank[best] ? lane.card.group : best), 'finished')

const latest = (lanes: RunLane[], pick: (lane: RunLane) => number) => Math.max(...lanes.map(pick))

/** Needs you: oldest wait first. Working and review: newest start first. Finished: failed first, then the latest end. */
function unitOrder(a: Unit, b: Unit): number {
  if (a.group !== b.group) return groupRank[a.group] - groupRank[b.group]
  const by = (x: number, y: number) => x - y
  switch (a.group) {
    case 'needs-you': {
      const wait = (unit: Unit) => unit.members.filter((lane) => lane.card.group === 'needs-you').map((lane) => lane.card.waitingSince).sort()[0] ?? ''
      return wait(a).localeCompare(wait(b)) || a.key.localeCompare(b.key)
    }
    case 'finished':
      return Number(endedBadly(b.root.state)) - Number(endedBadly(a.root.state)) || by(latest(b.members, (l) => l.end), latest(a.members, (l) => l.end)) || a.key.localeCompare(b.key)
    default:
      return by(latest(b.members, (l) => l.start), latest(a.members, (l) => l.start)) || a.key.localeCompare(b.key)
  }
}

const reaches = (lane: RunLane, window: TimeWindow) => lane.end >= window.start && lane.start <= window.end

/** Every tone ranks, so each worker in the window shows on the strip. */
const swarmRank: Record<Tone, number> = {
  'needs-you': 8,
  failed: 7,
  interrupted: 7,
  'wait-fault': 7,
  working: 6,
  review: 5,
  paused: 4,
  waiting: 3,
  'wait-unknown': 3,
  ended: 3,
  disconnected: 3,
  neutral: 3,
  done: 2,
  closed: 1,
}
const outcomeRank: Partial<Record<Tone, number>> = { failed: 3, done: 2, closed: 1 }

/** `least` widens short stretches so a brief failure still wins its pixels on the strip. */
function tonesOf(lane: RunLane, least: number): Stretch[] {
  return lane.segments.flatMap((segment) => {
    const tone = segmentTone(segment.state, lane.outcome)
    return tone === 'starting' || tone === 'unloaded' ? [] : [{ start: segment.start, end: Math.max(segment.end, segment.start + least), tone }]
  })
}

function swarmCounts(members: RunLane[], counted: (lane: RunLane) => boolean = (lane) => lane.card.run.mission_role === 'worker'): string {
  const counts = { working: 0, needsYou: 0, done: 0, failed: 0 }
  for (const lane of members) {
    const { card } = lane
    if (!counted(lane)) continue
    if (card.group === 'needs-you') counts.needsYou++
    else if (card.group === 'working') counts.working++
    else if (endedBadly(card.state)) counts.failed++
    else counts.done++
  }
  return [
    counts.needsYou > 0 && `${counts.needsYou} needs you`,
    counts.working > 0 && `${counts.working} working`,
    counts.done > 0 && `${counts.done} done`,
    counts.failed > 0 && `${counts.failed} failed`,
  ].filter(Boolean).join(' · ') || 'No workers yet'
}

function agentLanes(run: RunLane, load: AgentLoad, now: number): Lane[] {
  const runID = run.card.run.id
  const note = (text: string, retry = false): NoteLane => ({ kind: 'note', key: `${run.key}:note`, level: run.level + 1, parentKey: run.key, runID, text, retry })
  if (load.status === 'loading') return [note('Loading agents…')]
  if (load.status === 'error') return [note(`Agents: ${load.error}`, true)]
  const { agents, roots } = load.index
  if (roots.length === 0) return [note(load.partial ? 'No subagents in the newest 1000 session items.' : 'No subagents.')]
  const out: Lane[] = []
  const count = (id: string): number => agents[id].children.reduce((n, child) => n + 1 + count(child), 0)
  const walk = (id: string, depth: number, parentKey: string) => {
    const agent = agents[id]
    if (!agent) return
    const key = `${run.key}:agent:${id}`
    const state = agentState(agent)
    out.push({
      kind: 'agent',
      key,
      level: run.level + depth,
      parentKey,
      runID,
      agentID: id,
      label: agentLabel(agent),
      state,
      start: parse(agent.startedAt),
      end: parse(agent.settledAt) ?? (state === 'running' ? now : parse(agent.turnEndedAt) ?? now),
    })
    if (depth === agentDepth && agent.children.length > 0) {
      out.push({ kind: 'note', key: `${key}:nested`, level: run.level + depth + 1, parentKey: key, runID, text: `Nested workflow · ${count(id)} agents`, retry: false })
      return
    }
    for (const child of agent.children) walk(child, depth + 1, key)
  }
  for (const root of roots) walk(root, 1, run.key)
  return out
}

function ownerKey(unit: Unit): string {
  return unit.root.run.member_id
}

/** Flattens runs into sections of lanes; see docs/dashboard-frontend.md, Lanes. */
export function buildLanes(input: LanesInput): LanesModel {
  const { window, open, ctx } = input
  const least = (window.end - window.start) / 300
  const all = units(input).sort(unitOrder)
  const sections = new Map<string, { label: string; member?: Member; units: Unit[]; before: number; finished: boolean }>()
  const sectionKeys: string[] = []
  if (input.group === 'status') {
    for (const group of sectionOrder) {
      sections.set(group, { label: group === 'finished' && input.archived ? 'Archived' : groupLabel[group], units: [], before: 0, finished: group === 'finished' })
      sectionKeys.push(group)
    }
  } else {
    const owners = [...new Set(all.map(ownerKey))]
    const name = (id: string) => ctx.members[id]?.display_name ?? id
    owners.sort((a, b) => Number(b === ctx.viewerID) - Number(a === ctx.viewerID) || name(a).localeCompare(name(b)))
    for (const id of owners) {
      sections.set(id, { label: id === ctx.viewerID ? `${name(id)} (you)` : name(id), member: ctx.members[id], units: [], before: 0, finished: false })
      sectionKeys.push(id)
    }
  }
  let inWindow = 0
  for (const unit of all) {
    const section = sections.get(input.group === 'status' ? unit.group : ownerKey(unit))!
    const shown = unit.members.filter((lane) => reaches(lane, window))
    section.before += unit.members.filter((lane) => lane.end < window.start).length
    inWindow += shown.length
    if (shown.length > 0) section.units.push({ ...unit, members: shown })
  }

  const lanes: Lane[] = []
  const drawn = new Map<string, string>()
  const withAgents = (lane: RunLane): Lane[] => {
    const load = input.agents[lane.card.run.id]
    const offer = input.canShowAgents(lane.card)
    const shown = { ...lane, agentsOpen: offer || load ? load !== undefined : undefined }
    drawn.set(lane.card.run.id, lane.key)
    return load ? [shown, ...agentLanes(shown, load, input.now)] : [shown]
  }
  for (const id of sectionKeys) {
    const section = sections.get(id)!
    if (section.units.length === 0 && section.before === 0) continue
    const key = `section:${id}`
    const count = section.units.reduce((n, unit) => n + unit.members.length, 0)
    const sectionOpen = open.get(key) ?? !section.finished
    lanes.push({ kind: 'section', key, level: 1, label: section.label, count, open: sectionOpen, before: section.before, member: section.member })
    if (!sectionOpen) {
      if (!section.finished || count === 0) continue
      const members = section.units.flatMap((unit) => unit.members)
      const failed = members.filter((lane) => endedBadly(lane.card.state)).length
      const summary = `${key}:summary`
      for (const lane of members) drawn.set(lane.card.run.id, summary)
      lanes.push({
        kind: 'summary',
        key: summary,
        level: 2,
        parentKey: key,
        label: [`${count} ${section.label.toLowerCase()}`, failed > 0 && `${failed} failed`].filter(Boolean).join(' · '),
        strip: strongest(members.map((lane) => ({ start: lane.start, end: Math.max(lane.end, lane.start + least), tone: segmentTone('running', lane.outcome) as Tone })), outcomeRank),
      })
      continue
    }
    for (const unit of section.units) {
      if (!unit.missionID) {
        const [parent, ...children] = unit.members
        if (children.length === 0 || parent.card !== unit.root) {
          for (const lane of unit.members) lanes.push(...withAgents({ ...lane, parentKey: key }))
          continue
        }
        const familyKey = `family:${unit.key}`
        const needs = children.filter((lane) => lane.card.group === 'needs-you')
        const familyOpen = open.get(familyKey) ?? ((!parent.outcome && children.length <= swarmTasksOpen) || needs.length > 0)
        lanes.push({
          kind: 'swarm',
          key: familyKey,
          level: 2,
          parentKey: key,
          title: parent.title,
          detail: [`${children.length} ${children.length === 1 ? 'child run' : 'child runs'}`, swarmCounts(children, () => true)].join(' · '),
          open: familyOpen,
          integratorID: parent.card.run.id,
          strip: strongest(children.flatMap((lane) => tonesOf(lane, least)), swarmRank),
          segments: parent.segments,
          outcome: parent.outcome,
        })
        drawn.set(parent.card.run.id, familyKey)
        for (const lane of children) drawn.set(lane.card.run.id, familyKey)
        for (const lane of familyOpen ? [parent, ...children] : needs) lanes.push(...withAgents({ ...lane, level: 3, parentKey: familyKey }))
        continue
      }
      const mission = ctx.missions[unit.missionID]
      const detail = ctx.missionDetails[unit.missionID]
      const workers = unit.members.filter((lane) => lane.card.run.mission_role === 'worker')
      const needs = unit.members.filter((lane) => lane.card.group === 'needs-you')
      const live = mission ? !missionFinal(mission) : unit.members.some((lane) => !lane.outcome)
      const tasks = detail?.tasks.length ?? workers.length
      const swarmOpen = open.get(unit.key) ?? ((live && tasks <= swarmTasksOpen) || needs.some((lane) => lane.card.run.mission_role === 'worker'))
      const integrator = unit.members.find((lane) => lane.card.run.id === mission?.current_integrator_run_id) ?? unit.members.find((lane) => lane.card.run.mission_role === 'integrator')
      lanes.push({
        kind: 'swarm',
        key: unit.key,
        level: 2,
        parentKey: key,
        missionID: unit.missionID,
        title: objectiveTitle(mission?.objective ?? runLabel(unit.root.run)),
        detail: [mission ? phaseWord[mission.phase] : 'Swarm', swarmCounts(unit.members)].join(' · '),
        open: swarmOpen,
        integratorID: mission?.current_integrator_run_id || integrator?.card.run.id,
        strip: strongest(workers.flatMap((lane) => tonesOf(lane, least)), swarmRank),
        segments: integrator?.segments ?? [],
        outcome: integrator?.outcome,
      })
      for (const lane of unit.members) drawn.set(lane.card.run.id, unit.key)
      for (const lane of swarmOpen ? unit.members : needs) lanes.push(...withAgents({ ...lane, level: 3, parentKey: unit.key }))
    }
  }
  return { lanes, drawn, inWindow }
}

/** Keeps unchanged lanes, and the whole model when nothing changed, so memoized rows and edges skip the render. */
export function reuseLanes(next: LanesModel, previous: LanesModel | null): LanesModel {
  if (!previous) return next
  const old = new Map(previous.lanes.map((lane) => [lane.key, lane]))
  let same = next.lanes.length === previous.lanes.length
  const lanes = next.lanes.map((lane, i) => {
    const was = old.get(lane.key)
    const kept = was && dequal(was, lane) ? was : lane
    if (kept !== previous.lanes[i]) same = false
    return kept
  })
  const drawn = dequal(previous.drawn, next.drawn) ? previous.drawn : next.drawn
  if (same && drawn === previous.drawn && next.inWindow === previous.inWindow) return previous
  return { lanes: same ? previous.lanes : lanes, drawn, inWindow: next.inWindow }
}

export interface LaneMetrics {
  lane: number
  bar: number
  section: number
  sub: number
}

export const fineMetrics: LaneMetrics = { lane: 36, bar: 24, section: 28, sub: 28 }
export const coarseMetrics: LaneMetrics = { lane: 44, bar: 32, section: 44, sub: 36 }

export function laneHeight(lane: Lane, metrics: LaneMetrics): number {
  if (lane.kind === 'section') return metrics.section
  if (lane.kind === 'agent' || lane.kind === 'note') return metrics.sub
  return metrics.lane
}

/** Each lane's top, from fixed heights, so edges and windowing never measure the DOM. */
export function layoutLanes(lanes: Lane[], metrics: LaneMetrics): { top: Map<string, number>; height: number } {
  const top = new Map<string, number>()
  let y = 0
  for (const lane of lanes) {
    top.set(lane.key, y)
    y += laneHeight(lane, metrics)
  }
  return { top, height: y }
}

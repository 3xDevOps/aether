import type { StateContext } from '@/lib/needs-you'
import {
  groupLabel,
  groupOf,
  presentRun,
  type PresentationState,
  type RunGroup,
} from '@/lib/status'
import type { Member } from '@/lib/types'
import type { RootState } from '@/store'
import { isArchivable, type RunRecord } from '@/store/runs'

/** An empty workspace scopes nothing, as before hydration names one. */
export interface RunsInput {
  workspace: string
  mineOnly: boolean
  ctx: StateContext
}

export interface RunRow {
  run: RunRecord
  state: PresentationState
  reason: string
  group: RunGroup
  waitingSince: string
  owner?: Member
  workspaceName?: string
}

export interface SwarmSummary {
  collapsed: boolean
  counts: { working: number; needsYou: number; done: number; failed: number }
  members: RunRow[]
}

export interface RunTree extends RunRow {
  children: RunRow[]
  swarm?: SwarmSummary
}

export interface RunGroups {
  'needs-you': RunTree[]
  working: RunTree[]
  finished: RunTree[]
}

export interface SidebarGroup {
  key: RunGroup
  label: string
  runs: RunTree[]
  count: number
}

export function stateContextOf(s: RootState, now: number): StateContext {
  return {
    viewerID: s.info?.member.id ?? null,
    viewerRole: s.info?.member.role ?? null,
    members: s.members,
    runs: s.runs,
    workspaces: s.workspaces,
    approvalsByRun: s.approvalsByRun,
    roomMessages: s.roomMessages,
    roomStatus: s.roomStatus,
    missions: s.missions,
    missionDetails: s.missionDetails,
    pausedRuns: s.pausedRuns,
    now,
  }
}

export function runRows(ctx: StateContext): RunRow[] {
  const rows: RunRow[] = []
  for (const run of Object.values(ctx.runs)) {
    // A live run can never be hidden.
    if (run.archived_at && isArchivable(run.status)) continue
    const shown = presentRun(run, ctx)
    rows.push({
      run,
      state: shown.state,
      reason: shown.reason,
      group: groupOf(shown.state),
      waitingSince: shown.needsYou?.since(run, ctx) ?? run.stateChangedAt,
      owner: ctx.members[run.member_id],
    })
  }
  return rows
}

function swarmRoot(rows: RunRow[], ctx: StateContext): RunRow | undefined {
  const integrators = rows.filter((row) => row.run.mission_role === 'integrator')
  const mission = ctx.missions[rows[0].run.mission_id ?? '']
  return (
    integrators.find((row) => row.run.id === mission?.current_integrator_run_id) ??
    integrators.sort((a, b) => b.run.created_at.localeCompare(a.run.created_at))[0]
  )
}

function swarmTree(root: RunRow, members: RunRow[]): RunTree {
  const children = members.filter((row) => row.state === 'needs-you')
  const counts = { working: 0, needsYou: 0, done: 0, failed: 0 }
  for (const row of members) {
    if (row.state === 'needs-you') counts.needsYou++
    else if (row.group === 'working') counts.working++
    else if (row.state === 'failed') counts.failed++
    else counts.done++
  }
  const waits = [root, ...children].filter((row) => row.state === 'needs-you')
  return {
    ...root,
    group: children.length > 0 ? 'needs-you' : root.group,
    waitingSince: waits.map((row) => row.waitingSince).sort()[0] ?? root.waitingSince,
    children,
    swarm: { collapsed: children.length < members.length, counts, members },
  }
}

export function runTrees(rows: RunRow[], ctx: StateContext): RunTree[] {
  const swarms = new Map<string, RunRow[]>()
  const trees: RunTree[] = []
  for (const row of rows) {
    const { mission_id: mission, mission_role: role, workspace_id: workspace } = row.run
    if (!mission || !role) {
      trees.push({ ...row, children: [] })
      continue
    }
    const key = JSON.stringify([workspace, mission])
    const swarm = swarms.get(key)
    if (swarm) swarm.push(row)
    else swarms.set(key, [row])
  }
  for (const members of swarms.values()) {
    const root = swarmRoot(members, ctx)
    if (!root) trees.push(...members.map((row) => ({ ...row, children: [] })))
    else trees.push(swarmTree(root, members.filter((row) => row !== root)))
  }
  return trees
}

const byWait = (a: RunRow, b: RunRow) =>
  a.waitingSince.localeCompare(b.waitingSince) || a.run.id.localeCompare(b.run.id)
const byChange = (a: RunRow, b: RunRow) =>
  b.run.stateChangedAt.localeCompare(a.run.stateChangedAt) || a.run.id.localeCompare(b.run.id)
const failedFirst = (a: RunRow, b: RunRow) =>
  Number(b.state === 'failed') - Number(a.state === 'failed') || byChange(a, b)
const inGroup: Record<RunGroup, (a: RunRow, b: RunRow) => number> = {
  'needs-you': byWait,
  working: byChange,
  finished: failedFirst,
}
const groupOrder: RunGroup[] = ['needs-you', 'working', 'finished']

function located(row: RunRow, s: RunsInput): RunRow {
  if (!s.workspace || row.run.workspace_id === s.workspace) return row
  const workspace = s.ctx.workspaces[row.run.workspace_id]
  return { ...row, workspaceName: workspace?.name ?? row.run.workspace_id }
}

export function runGroups(s: RunsInput): RunGroups {
  const groups: RunGroups = { 'needs-you': [], working: [], finished: [] }
  for (const tree of runTrees(runRows(s.ctx), s.ctx)) {
    if (tree.group !== 'needs-you') {
      if (s.workspace && tree.run.workspace_id !== s.workspace) continue
      if (s.mineOnly && tree.run.member_id !== s.ctx.viewerID) continue
    }
    groups[tree.group].push({
      ...(located(tree, s) as RunTree),
      children: tree.children.map((row) => located(row, s)),
    })
  }
  for (const key of groupOrder) groups[key].sort(inGroup[key])
  return groups
}

export function listedRuns(workspace: string, ctx: StateContext): RunRow[] {
  return runRows(ctx)
    .filter((row) => !workspace || row.run.workspace_id === workspace)
    .sort((a, b) => groupOrder.indexOf(a.group) - groupOrder.indexOf(b.group) || inGroup[a.group](a, b))
}

export function sidebarGroups(s: RunsInput): SidebarGroup[] {
  const groups = runGroups(s)
  return groupOrder
    .filter((key) => groups[key].length > 0)
    .map((key) => ({
      key,
      label: groupLabel[key],
      runs: groups[key],
      count:
        key === 'needs-you'
          ? groups[key].reduce(
              (n, tree) => n + Number(tree.state === 'needs-you') + tree.children.length,
              0,
            )
          : groups[key].length,
    }))
}

export function needsYouByWorkspace(ctx: StateContext): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const row of runRows(ctx)) {
    if (row.state !== 'needs-you') continue
    counts[row.run.workspace_id] = (counts[row.run.workspace_id] ?? 0) + 1
  }
  return counts
}

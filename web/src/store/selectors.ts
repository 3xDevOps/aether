import { resolveMember } from '@/lib/member-name'
import type { NeedsYouID, StateContext } from '@/lib/needs-you'
import {
  endedBadly,
  groupLabel,
  groupOf,
  presentRun,
  type PresentationState,
  type RunGroup,
} from '@/lib/status'
import { extraRepoCount, repoLabel } from '@/lib/repos'
import { rootKey, runKey, runParents } from '@/routes/board/map-layout'
import type { Member, RunStatusCause, RunWait } from '@/lib/types'
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
  next?: string
  cause?: RunStatusCause
  wait?: RunWait
  needsYou?: NeedsYouID
  unread?: number
  group: RunGroup
  /** When the row began what it shows: its wait when it needs the viewer, else its last status change. */
  since: string
  /** The Needs you sort key: `since`, or for a swarm its oldest wait. */
  waitingSince: string
  owner?: Member
  workspaceName?: string
}

/** The runs under a tree's root, at any depth. */
export interface TreeSummary {
  collapsed: boolean
  counts: { working: number; needsYou: number; done: number; failed: number }
  members: RunRow[]
}

export function childRunsText({ members }: TreeSummary): string {
  return `${members.length} ${members.length === 1 ? 'child run' : 'child runs'}`
}

/** `children` are the members that need the viewer, which a folded tree still shows. */
export interface RunTree extends RunRow {
  children: RunRow[]
  swarm?: TreeSummary
  /** Child runs of a run outside a swarm. */
  childRuns?: TreeSummary
}

export interface RunGroups {
  'needs-you': RunTree[]
  working: RunTree[]
  review: RunTree[]
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
    mergedInto: s.mergedInto,
    runs: s.runs,
    workspaces: s.workspaces,
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
    const since = shown.needsYou?.since(run, ctx) ?? run.stateChangedAt
    rows.push({
      run,
      state: shown.state,
      reason: shown.reason,
      next: shown.next,
      cause: shown.cause,
      wait: shown.wait,
      needsYou: shown.needsYou?.id,
      unread: shown.unread,
      group: groupOf(shown),
      since,
      waitingSince: since,
      owner: resolveMember(ctx, run.member_id).member,
    })
  }
  return rows
}

// Without a listed integrator the oldest worker stands in, so workers never list on their own.
function swarmRoot(rows: RunRow[], ctx: StateContext): RunRow {
  const integrators = rows.filter((row) => row.run.mission_role === 'integrator')
  const mission = ctx.missions[rows[0].run.mission_id ?? '']
  return (
    integrators.find((row) => row.run.id === mission?.current_integrator_run_id) ??
    integrators.sort((a, b) => b.run.created_at.localeCompare(a.run.created_at))[0] ??
    [...rows].sort((a, b) => a.run.created_at.localeCompare(b.run.created_at))[0]
  )
}

function memberTree(root: RunRow, members: RunRow[], kind: 'swarm' | 'childRuns'): RunTree {
  const children = members.filter((row) => row.needsYou)
  const counts = { working: 0, needsYou: 0, done: 0, failed: 0 }
  for (const row of members) {
    if (row.needsYou) counts.needsYou++
    else if (row.group === 'working') counts.working++
    else if (endedBadly(row.state)) counts.failed++
    else counts.done++
  }
  const waits = [root, ...children].filter((row) => row.needsYou)
  return {
    ...root,
    group: children.length > 0 ? 'needs-you' : root.group,
    waitingSince: waits.map((row) => row.waitingSince).sort()[0] ?? root.waitingSince,
    children,
    [kind]: { collapsed: children.length < members.length, counts, members },
  }
}

/** A swarm's runs and every run they started form one tree; other child runs join the tree of their root. */
export function runTrees(rows: RunRow[], ctx: StateContext): RunTree[] {
  const parents = runParents(rows)
  const byKey = new Map(rows.map((row) => [runKey(row), row]))
  const swarms = new Map<string, RunRow[]>()
  const families = new Map<RunRow, RunRow[]>()
  for (const row of rows) {
    const root = byKey.get(rootKey(runKey(row), parents))!
    const { mission_id: mission, mission_role: role, workspace_id: workspace } = root.run
    if (mission && role) {
      const key = JSON.stringify([workspace, mission])
      const swarm = swarms.get(key)
      if (swarm) swarm.push(row)
      else swarms.set(key, [row])
      continue
    }
    const family = families.get(root) ?? []
    families.set(root, family)
    if (root !== row) family.push(row)
  }
  const trees: RunTree[] = []
  for (const [root, members] of families) {
    trees.push(members.length > 0 ? memberTree(root, members, 'childRuns') : { ...root, children: [] })
  }
  for (const members of swarms.values()) {
    const root = swarmRoot(members.filter((row) => row.run.mission_role), ctx)
    trees.push(memberTree(root, members.filter((row) => row !== root), 'swarm'))
  }
  return trees
}

/** Every child run below `rootIDs`, nearest first; swarm workers are a swarm's own and not listed. */
export function descendantRuns(runs: Record<string, RunRecord>, rootIDs: string[]): RunRecord[] {
  const below = new Map<string, RunRecord[]>()
  for (const run of Object.values(runs)) {
    if (!run.parent_run_id || run.mission_role === 'worker') continue
    const siblings = below.get(run.parent_run_id) ?? []
    siblings.push(run)
    below.set(run.parent_run_id, siblings)
  }
  const found: RunRecord[] = []
  const seen = new Set(rootIDs)
  for (let i = 0, frontier = [...rootIDs]; i < frontier.length; i++) {
    for (const child of (below.get(frontier[i]) ?? []).sort((a, b) => a.created_at.localeCompare(b.created_at))) {
      if (seen.has(child.id)) continue
      seen.add(child.id)
      found.push(child)
      frontier.push(child.id)
    }
  }
  return found
}

export const byWait = (a: RunRow, b: RunRow) =>
  a.waitingSince.localeCompare(b.waitingSince) || a.run.id.localeCompare(b.run.id)
export const byChange = (a: RunRow, b: RunRow) =>
  b.run.stateChangedAt.localeCompare(a.run.stateChangedAt) || a.run.id.localeCompare(b.run.id)
const failedFirst = (a: RunRow, b: RunRow) =>
  Number(endedBadly(b.state)) - Number(endedBadly(a.state)) || byChange(a, b)
const inGroup: Record<RunGroup, (a: RunRow, b: RunRow) => number> = {
  'needs-you': byWait,
  working: byChange,
  review: byChange,
  finished: failedFirst,
}
const groupOrder: RunGroup[] = ['needs-you', 'working', 'review', 'finished']

function located(row: RunRow, s: RunsInput): RunRow {
  const elsewhere = s.workspace && row.run.workspace_id !== s.workspace
  if (!elsewhere && extraRepoCount(row.run) === 0) return row
  const name = s.ctx.workspaces[row.run.workspace_id]?.name ?? row.run.workspace_id
  return { ...row, workspaceName: repoLabel(name, row.run) }
}

export function runGroups(s: RunsInput): RunGroups {
  const groups: RunGroups = { 'needs-you': [], working: [], review: [], finished: [] }
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

/** `devices` counts the device cards that join the runs in Needs you. */
export function sidebarGroups(s: RunsInput, devices = 0): SidebarGroup[] {
  const groups = runGroups(s)
  return groupOrder
    .filter((key) => groups[key].length > 0 || (key === 'needs-you' && devices > 0))
    .map((key) => ({
      key,
      label: groupLabel[key],
      runs: groups[key].map((tree) => tree.swarm || tree.childRuns ? {
        ...tree,
        children: (tree.swarm ?? tree.childRuns)!.members
          .filter((row) => key === 'needs-you' || !s.mineOnly || row.run.member_id === s.ctx.viewerID)
          .sort((a, b) => a.run.created_at.localeCompare(b.run.created_at) || a.run.id.localeCompare(b.run.id))
          .map((row) => located(row, s)),
      } : tree),
      count:
        key === 'needs-you'
          ? groups[key].reduce(
              (n, tree) => n + Number(tree.needsYou !== undefined) + tree.children.length,
              devices,
            )
          : groups[key].length,
    }))
}

export function needsYouByWorkspace(ctx: StateContext): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const row of runRows(ctx)) {
    if (!row.needsYou) continue
    counts[row.run.workspace_id] = (counts[row.run.workspace_id] ?? 0) + 1
  }
  return counts
}

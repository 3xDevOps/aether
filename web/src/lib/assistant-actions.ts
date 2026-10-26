import type { AssistantActionRecord, AssistantConfirmation } from '@/lib/assistant-types'
import { routeFromQuery } from '@/lib/url-state'
import type { SessionRow, Turn } from '@/store/session-rows'
import { stepsBlock, type Step, type WorkItem } from '@/store/session-steps'
import type { Route } from '@/store/ui'

export type ActionState = 'approval' | 'running' | 'done' | 'failed' | 'declined' | 'expired'

/** How a confirmed call passed: on its card, or by auto-approval. */
export type Approval = 'approved' | 'auto-approved'

export interface ActionTarget {
  kind: 'run' | 'swarm' | 'pr' | 'workspace' | 'environment' | 'watch'
  id: string
  route?: Route
  url?: string
}

export interface AssistantAction {
  id: string
  callID: string
  tool: string
  read: boolean
  at: string
  state: ActionState
  approval?: Approval
  input: Record<string, unknown>
  result: unknown
  error?: string
  target?: ActionTarget
}

export type ActionGroup =
  | { kind: 'action'; id: string; action: AssistantAction }
  | { kind: 'reads'; id: string; actions: AssistantAction[] }

export interface AssistantView {
  rows: SessionRow[]
  /** Cards after the row whose id keys them; `''` keys the ones before every row. */
  groups: Map<string, ActionGroup[]>
  /** Every call that is not a read, oldest first. */
  actions: AssistantAction[]
}

/** The RPC method each acting tool calls: what a pending card and an action record name. */
const toolMethods: Record<string, string> = {
  aether_launch_run: 'run.launch',
  aether_message_run: 'run.inject',
  aether_stop_run: 'run.kill',
  aether_pause_run: 'run.pause',
  aether_resume_run: 'run.resume',
  aether_save_environment: 'env.save',
  aether_launch_swarm: 'mission.create',
  aether_pr_merge: 'run.pr.merge',
  aether_relaunch_run: 'run.relaunch',
  aether_answer: 'run.input.respond',
}

const readTools: Record<string, [string, string]> = {
  aether_list_runs: ['run list', 'run lists'],
  aether_get_run: ['run', 'runs'],
  aether_run_history: ['run conversation', 'run conversations'],
  aether_run_changes: ['run diff', 'run diffs'],
  aether_list_workspaces: ['workspace list', 'workspace lists'],
  aether_get_workspace: ['workspace', 'workspaces'],
  aether_list_agents: ['agent list', 'agent lists'],
  aether_usage: ['usage report', 'usage reports'],
  aether_environment_status: ['environment status', 'environment statuses'],
  aether_docs: ['guide', 'guides'],
  aether_view_context: ['view context', 'view contexts'],
  aether_memory_search: ['memory search', 'memory searches'],
  aether_needs_attention: ['attention list', 'attention lists'],
  aether_list_swarms: ['swarm list', 'swarm lists'],
  aether_get_swarm: ['swarm', 'swarms'],
  aether_pr_status: ['pull request', 'pull requests'],
  aether_overlaps: ['overlap report', 'overlap reports'],
  aether_activity_summary: ['activity summary', 'activity summaries'],
  aether_changes: ['change list', 'change lists'],
  aether_launch_options: ['launch options', 'launch options'],
  aether_list_watches: ['watch list', 'watch lists'],
  aether_get_watch: ['watch', 'watches'],
}

/** The Aether tool a call names: Claude Code's `mcp__aether__aether_get_run`, or the bare name another agent reports. */
export function aetherTool(name: string | undefined): string | undefined {
  return /(?:^|__|[./])(aether_[a-z0-9]+(?:_[a-z0-9]+)*)$/.exec(name ?? '')?.[1]
}

function isRead(tool: string): boolean {
  return tool in readTools || /^aether_(?:list|get)_/.test(tool)
}

/** A read tool's noun, singular and plural; one added since falls back to its own name. */
export function readNoun(tool: string): [string, string] {
  const known = readTools[tool]
  if (known) return known
  const words = tool.replace(/^aether_(?:list|get)_/, '').replace(/_/g, ' ')
  return [words, words]
}

export function readSummary(actions: AssistantAction[]): string {
  const counts = new Map<string, { noun: [string, string]; n: number }>()
  for (const action of actions) {
    const noun = readNoun(action.tool)
    const entry = counts.get(noun[0]) ?? { noun, n: 0 }
    entry.n++
    counts.set(noun[0], entry)
  }
  const parts = [...counts.values()].map(({ noun, n }) => `${n} ${n === 1 ? noun[0] : noun[1]}`)
  const list = parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}` : parts[0]
  return `${actions.some((action) => action.state === 'running') ? 'Reading' : 'Read'} ${list}`
}

function parsed(value: unknown): unknown {
  if (typeof value !== 'string') return value
  const text = value.trim()
  if (!text.startsWith('{') && !text.startsWith('[')) return value
  try {
    return JSON.parse(text)
  } catch {
    return value
  }
}

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : typeof value === 'number' ? String(value) : undefined
}

/** A deep link the result names: the dashboard's own query (`?run=<id>`) or an outside address such as a pull request. */
function linkTarget(link: string): ActionTarget | undefined {
  let url: URL
  try {
    url = new URL(link, window.location.href)
  } catch {
    return undefined
  }
  if (url.origin !== window.location.origin) return { kind: 'pr', id: link, url: link }
  const route = routeFromQuery(url.searchParams)
  if (route.name === 'run' && route.params.runId) return { kind: 'run', id: route.params.runId, route }
  if (route.name === 'missions' && route.params.missionId) return { kind: 'swarm', id: route.params.missionId, route }
  if (route.name === 'workspace' && route.params.workspaceId) return { kind: 'workspace', id: route.params.workspaceId, route }
  const watch = url.searchParams.get('watch')
  if (route.name === 'settings' && watch) return { kind: 'watch', id: watch, route: { ...route, params: { ...route.params, watch } } }
  return undefined
}

export function runTarget(id: string): ActionTarget {
  return { kind: 'run', id, route: { name: 'run', params: { runId: id } } }
}

export function swarmTarget(id: string): ActionTarget {
  return { kind: 'swarm', id, route: { name: 'missions', params: { missionId: id } } }
}

function target(tool: string, input: Record<string, unknown>, result: Record<string, unknown>, saved?: AssistantActionRecord): ActionTarget | undefined {
  const link = text(result.link) ?? text(record(result.launch).link)
  const linked = link ? linkTarget(link) : undefined
  if (linked) return linked
  const ids = record(result.ids)
  const run = text(saved?.result?.run_id) ?? text(record(result.run).id) ?? text(ids.run_id)
  const swarm = text(saved?.result?.mission_id) ?? text(record(result.mission).id) ?? text(record(result.swarm).id) ?? text(ids.mission_id) ?? text(ids.swarm_id)
  if (tool === 'aether_pr_merge') {
    const url = text(result.url) ?? text(result.pr_url) ?? text(record(result.pr).url)
    if (url) return { kind: 'pr', id: url, url }
  }
  if (tool === 'aether_save_environment') return { kind: 'environment', id: 'environment', route: { name: 'environment', params: {} } }
  if (swarm || (tool.endsWith('_swarm') && text(input.mission_id))) return swarmTarget(swarm ?? text(input.mission_id)!)
  const runID = run ?? text(input.run_id)
  if (runID) return runTarget(runID)
  const workspace = text(input.workspace_id)
  if (workspace) return { kind: 'workspace', id: workspace, route: { name: 'workspace', params: { workspaceId: workspace } } }
  return undefined
}

function errorText(result: unknown): string {
  if (typeof result === 'string') return result
  const body = record(result)
  return text(body.error) ?? text(body.message) ?? JSON.stringify(result)
}

function recordApproval(saved: AssistantActionRecord): Approval | undefined {
  if (saved.approval === 'card' || saved.outcome === 'approved') return 'approved'
  if (saved.approval === 'auto' || saved.outcome === 'auto_approved') return 'auto-approved'
  return undefined
}

/**
 * The state a call shows. The server's action record wins. Without one it
 * comes from the tool call: the error the server returns for a declined or
 * expired card, a pending confirmation for the same method, and the
 * approval note written before the call ran.
 */
export function actionOutcome(
  entry: Pick<WorkItem, 'status' | 'waiting'>,
  error: string | undefined,
  awaiting: boolean,
  noted: Approval | undefined,
  saved?: AssistantActionRecord,
): { state: ActionState; approval?: Approval } {
  const approval = saved ? recordApproval(saved) : noted
  switch (saved?.outcome) {
    case 'declined':
    case 'expired':
      return { state: saved.outcome }
    case 'withdrawn':
    case 'failed':
      return { state: 'failed', approval }
    case 'succeeded':
      return { state: 'done', approval }
  }
  if (entry.status === 'failed') {
    if (!saved && error?.includes('the member declined this action')) return { state: 'declined' }
    if (!saved && error?.includes('the member did not answer within')) return { state: 'expired' }
    return { state: 'failed', approval }
  }
  if (entry.status === 'running' && !approval && (entry.waiting || awaiting)) return { state: 'approval' }
  return { state: entry.status === 'running' ? 'running' : 'done', approval }
}

/** Each notice that carries an action record, by the id of the event it shows as. */
export function actionRecords(turns: Turn[] | undefined): Map<string, AssistantActionRecord> {
  const out = new Map<string, AssistantActionRecord>()
  for (const turn of turns ?? []) {
    for (const item of turn.items) if (item.kind === 'notice' && item.notice?.action) out.set(`ev:${item.seq}`, item.notice.action)
  }
  return out
}

const workKinds = new Set<Step['kind']>(['thought', 'tool', 'lookup', 'plan', 'subagents', 'answered'])

/** A step left without work around it reads as the conversation row it would have been. */
function asRow(step: Step): SessionRow[] {
  if (step.kind === 'narration') return [{ kind: 'assistant', id: step.id, at: step.at, text: step.text, streaming: step.streaming }]
  if (step.kind === 'event') return [{ kind: 'event', id: step.id, at: step.at, text: step.text, detail: step.detail, tone: step.tone }]
  return []
}

const approvalNote = /^(Approved|Auto-approved): /

interface Note {
  method: string
  approval?: Approval
  saved?: AssistantActionRecord
}

/** The record a notice carries, or the approval an older server's note names in words. */
function noteOf(note: Step | SessionRow, records: Map<string, AssistantActionRecord>): Note | undefined {
  if (note.kind !== 'event') return undefined
  const saved = records.get(note.id)
  if (saved) return { method: saved.method, saved }
  const match = approvalNote.exec(note.text)
  const method = /^(\S+) passed /.exec(note.detail ?? '')?.[1]
  if (!match || !method) return undefined
  return { method, approval: match[1] === 'Approved' ? 'approved' : 'auto-approved' }
}

interface Draft {
  action: AssistantAction
  entry: WorkItem
  method?: string
  noted?: Approval
  saved?: AssistantActionRecord
}

function sameRun(draft: Draft, saved: AssistantActionRecord | undefined): boolean {
  const asked = text(draft.action.input.run_id)
  return !asked || !saved?.target?.run_id || asked === saved.target.run_id
}

/**
 * Pulls the assistant's Aether tool calls out of the transcript's step
 * blocks into cards, consecutive reads into one line, and folds each
 * approval note or action record into the card of the call it belongs to.
 */
export function assistantView(rows: SessionRow[], confirmations: AssistantConfirmation[], records = new Map<string, AssistantActionRecord>()): AssistantView {
  const waitingMethods = new Set(confirmations.map((c) => c.method))
  const out: SessionRow[] = []
  const groups = new Map<string, ActionGroup[]>()
  const actions: AssistantAction[] = []
  const drafts: Draft[] = []
  const draft = (entry: WorkItem, tool: string): AssistantAction => {
    const action: AssistantAction = {
      id: entry.id,
      callID: entry.id.slice(entry.id.indexOf(':') + 1),
      tool,
      read: isRead(tool),
      at: entry.at,
      state: 'running',
      input: record(entry.input),
      result: parsed(entry.result),
    }
    drafts.push({ action, entry, method: toolMethods[tool] })
    return action
  }
  // A record carries no tool call id. It is written while its call runs, so
  // it follows the call's first appearance: it goes to the call that already
  // holds its action, or else the latest unclaimed call of its method and run.
  const claim = (step: Step | SessionRow): boolean => {
    const note = noteOf(step, records)
    if (!note) return false
    const owner = drafts.find((d) => note.saved && d.saved?.id === note.saved.id)
      ?? [...drafts].reverse().find((d) => d.method === note.method && !d.noted && !d.saved && sameRun(d, note.saved))
    if (!owner) return false
    if (note.saved) owner.saved = note.saved
    else owner.noted = note.approval
    return true
  }
  for (const row of rows) {
    if (row.kind !== 'steps') {
      if (!claim(row)) out.push(row)
      continue
    }
    const kept: Step[] = []
    const built: AssistantAction[] = []
    for (const step of row.steps) {
      const tool = step.kind === 'tool' ? aetherTool(step.entry.name) : undefined
      if (step.kind === 'tool' && tool) built.push(draft(step.entry, tool))
      else if (!claim(step)) kept.push(step)
    }
    if (kept.length === row.steps.length) {
      out.push(row)
      continue
    }
    const working = kept.some((step) => workKinds.has(step.kind))
    if (working) out.push({ ...stepsBlock(row.id, kept, row.start, row.current), live: row.live })
    else out.push(...kept.flatMap(asRow))
    const anchor = out.at(-1)?.id ?? ''
    const list = groups.get(anchor) ?? []
    for (const action of built) {
      const last = list.at(-1)
      if (action.read && last?.kind === 'reads') last.actions.push(action)
      else if (action.read) list.push({ kind: 'reads', id: `reads:${action.id}`, actions: [action] })
      else {
        list.push({ kind: 'action', id: `action:${action.id}`, action })
        actions.push(action)
      }
    }
    groups.set(anchor, list)
    if (!working && row.live) out.push({ kind: 'live', id: 'live', at: row.at, ...row.live })
  }
  for (const { action, entry, method, noted, saved } of drafts) {
    const error = entry.status === 'failed' ? errorText(action.result) : undefined
    Object.assign(action, actionOutcome(entry, error, method !== undefined && waitingMethods.has(method), noted, saved))
    action.error = saved?.error ?? error
    action.target = target(action.tool, action.input, record(action.result), saved)
  }
  return { rows: out, groups, actions }
}

/** Display names the transcript knows; a missing one leaves the id. */
export interface ActionNames {
  workspace: (id: string) => string | undefined
  agent: (name: string) => string | undefined
  run: (id: string) => string | undefined
}

type Verb = [imperative: string, past: string, present: string]

const runVerb = (verb: Verb, suffix = '') => ({ verb, object: (a: AssistantAction, n: ActionNames) => `run ${runName(a, n)}${suffix}` })

function runName(action: AssistantAction, names: ActionNames): string {
  const id = text(action.input.run_id) ?? (action.target?.kind === 'run' ? action.target.id : undefined)
  return id ? names.run(id) ?? id : ''
}

function launchObject(kind: string) {
  return (action: AssistantAction, names: ActionNames) => {
    const workspace = text(action.input.workspace_id)
    const agent = text(action.input.harness)
    return [kind, workspace && `on ${names.workspace(workspace) ?? workspace}`, agent && `with ${names.agent(agent) ?? agent}`].filter(Boolean).join(' ')
  }
}

function prName(action: AssistantAction, names: ActionNames): string {
  const result = record(action.result)
  const number = text(result.number) ?? text(record(result.pr).number) ?? text(action.input.pr_number)
  return number ? `PR #${number}` : `the pull request of run ${runName(action, names)}`
}

function watchObject(action: AssistantAction, names: ActionNames): string {
  const pr = text(action.input.pr) ?? text(action.input.pr_number)
  if (pr) return `PR ${/^\d+$/.test(pr) ? `#${pr}` : pr}`
  const mission = text(action.input.mission_id)
  if (mission) return `swarm ${mission}`
  return text(action.input.run_id) ? `run ${runName(action, names)}` : text(action.input.watch_id) ?? ''
}

const phrases: Record<string, { verb: Verb; object: (action: AssistantAction, names: ActionNames) => string }> = {
  aether_launch_run: { verb: ['Launch', 'Launched', 'Launching'], object: launchObject('run') },
  aether_launch_swarm: { verb: ['Launch', 'Launched', 'Launching'], object: launchObject('swarm') },
  aether_relaunch_run: runVerb(['Relaunch', 'Relaunched', 'Relaunching']),
  aether_message_run: runVerb(['Message', 'Messaged', 'Messaging']),
  aether_stop_run: runVerb(['Stop', 'Stopped', 'Stopping']),
  aether_pause_run: runVerb(['Pause', 'Paused', 'Pausing']),
  aether_resume_run: runVerb(['Resume', 'Resumed', 'Resuming']),
  aether_close_run: runVerb(['Close', 'Closed', 'Closing']),
  aether_mark_reviewed: runVerb(['Mark', 'Marked', 'Marking'], ' reviewed'),
  aether_answer: runVerb(['Answer', 'Answered', 'Answering']),
  aether_decide_approval: { verb: ['Decide', 'Decided', 'Deciding'], object: (a, n) => `an approval for run ${runName(a, n)}` },
  aether_cancel_swarm: { verb: ['Cancel', 'Cancelled', 'Cancelling'], object: (a) => `swarm ${text(a.input.mission_id) ?? a.target?.id ?? ''}`.trimEnd() },
  aether_pr_merge: { verb: ['Merge', 'Merged', 'Merging'], object: prName },
  aether_save_environment: { verb: ['Save', 'Saved', 'Saving'], object: () => 'your environment' },
  aether_watch_create: { verb: ['Watch', 'Started watching', 'Starting to watch'], object: watchObject },
  aether_watch_delete: { verb: ['Stop watching', 'Stopped watching', 'Stopping watching'], object: watchObject },
}

/** What the call did in product words: "Launched run on api with Claude Code". */
export function actionSummary(action: AssistantAction, names: ActionNames): string {
  const phrase = phrases[action.tool] ?? { verb: ['Use', 'Used', 'Using'] as Verb, object: () => action.tool }
  const [imperative, past, present] = phrase.verb
  const verb = action.state === 'done' ? past : action.state === 'running' || action.state === 'approval' ? present : imperative
  return `${verb} ${phrase.object(action, names)}`.trimEnd()
}

/** Whether the tool has its own words; any other Aether tool shows its name and inputs. */
export function isKnownAction(tool: string): boolean {
  return tool in phrases
}

/** A launch's receipt in one line: what started where, on which agent and whose account, and whether it fell back. */
export function launchLine(action: AssistantAction, names: ActionNames): string | undefined {
  const launch = record(record(action.result).launch)
  const kind = text(launch.kind)
  if (!kind) return undefined
  const workspace = text(launch.workspace_id)
  const harness = text(launch.harness)
  const account = record(launch.account)
  const owner = text(account.name)
  const requested = text(record(launch.requested_account).name) ?? text(launch.requested_account)
  return [
    kind,
    workspace && (names.workspace(workspace) ?? workspace),
    harness && [names.agent(harness) ?? harness, text(launch.model)].filter(Boolean).join(' '),
    owner && `${owner}'s account${account.shared === true || account.own === false || Object.values(account).includes('shared') ? ', shared' : ''}`,
    requested && `fell back from ${requested}`,
  ].filter(Boolean).join(' · ')
}

/** A watch's state, what it waits for and how its last notice went. */
export function watchLine(action: AssistantAction): string | undefined {
  if (!action.tool.startsWith('aether_watch_')) return undefined
  const result = record(action.result)
  const watch = record(result.watch ?? result)
  const conditions = Array.isArray(watch.conditions) ? watch.conditions.filter((c): c is string => typeof c === 'string') : []
  const deliveries = record(watch.last_notification).deliveries
  const last = Array.isArray(deliveries)
    ? deliveries.map((d) => [text(record(d).channel), text(record(d).status)?.replace(/_/g, ' ')].filter(Boolean).join(' ')).filter(Boolean)
    : []
  const line = [text(watch.state), conditions.length > 0 && `on ${conditions.join(', ').replace(/_/g, ' ')}`, last.length > 0 && `last notice: ${last.join(', ')}`]
    .filter(Boolean).join(' · ')
  return line || undefined
}

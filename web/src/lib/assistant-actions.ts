import type { AssistantActionRecord, AssistantActionTarget, AssistantConfirmation } from '@/lib/assistant-types'
import { routeFromQuery } from '@/lib/url-state'
import type { SessionToolCall } from '@/lib/session-types'
import type { SessionRow, Turn } from '@/store/session-rows'
import { asRow, stepsBlock, workKinds, type Step, type WorkItem } from '@/store/session-steps'
import type { Route } from '@/store/ui'

export type ActionState = 'approval' | 'running' | 'done' | 'unconfirmed' | 'failed' | 'declined' | 'expired' | 'withdrawn' | 'unfinished'

export type Approval = 'approved' | 'auto-approved'

export interface ActionTarget {
  kind: 'run' | 'swarm' | 'pr' | 'workspace' | 'environment' | 'watch' | 'link'
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
  /** The log holds a cut-down item; `assistant.item` reads the whole one. */
  seq?: number
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

/** The RPC methods each acting tool may call, by its input where it says which: what a pending card and an action record name. */
const toolMethods: Record<string, string | string[] | ((input: Record<string, unknown>) => string | undefined)> = {
  aether_launch_run: 'run.launch',
  aether_message_run: 'run.inject',
  aether_stop_run: (input) => (text(input.outcome) ? 'run.close' : 'run.kill'),
  aether_pause_run: 'run.pause',
  aether_resume_run: 'run.resume',
  aether_save_environment: 'env.save',
  aether_launch_swarm: 'mission.create',
  aether_pr_merge: 'run.pr.merge',
  aether_pr_create: 'run.pr.create',
  aether_relaunch_run: 'run.relaunch',
  // The question the answer_id names decides: an agent's, a swarm's or a teammate's.
  aether_answer: ['run.input.respond', 'mission.question.answer', 'run.room.post'],
  aether_decide_approval: 'run.input.respond',
  aether_run_lifecycle: (input) => ({ relaunch: 'run.relaunch', protect: 'run.protect', unprotect: 'run.protect', archive: 'run.archive', restore: 'run.archive' } as Record<string, string>)[text(input.action) ?? ''],
  aether_switch_run_mode: 'run.mode.switch',
  aether_handoff_run: 'run.handoff',
  aether_swarm_manage: (input) => ({ release_worker: 'mission.worker.release', archive: 'mission.archive', unarchive: 'mission.unarchive' } as Record<string, string>)[text(input.action) ?? ''],
  aether_replace_integrator: 'mission.replace-integrator',
  aether_integration_decide: (input) => (input.action === 'deliver' ? 'integration.deliver' : 'integration.decide'),
  aether_memory_add: 'memory.add',
  aether_memory_pin: (input) => (input.pin === false ? 'memory.unpin' : 'memory.pin'),
  aether_watch: (input) => (text(input.watch_id) ? 'watch.update' : 'watch.create'),
  aether_unwatch: 'watch.delete',
}

function toolMethod(tool: string, input: Record<string, unknown>): string[] {
  const method = toolMethods[tool]
  const named = typeof method === 'function' ? method(input) : method
  return named === undefined ? [] : [named].flat()
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
  aether_integration: ['integration status', 'integration statuses'],
  aether_watches: ['watch list', 'watch lists'],
}

/** The Aether tool a call's `tool_name` names: Claude Code's `mcp__aether__aether_get_run`, or a bare name. */
export function aetherTool(name: string | undefined): string | undefined {
  return /(?:^|__|[./])(aether_[a-z0-9]+(?:_[a-z0-9]+)*)$/.exec(name ?? '')?.[1]
}

/** Codex reports no tool name; its MCP call's title is `mcp.<server>.<tool>`. */
function codexTool(call: SessionToolCall | undefined): string | undefined {
  return /^mcp\.aether\.(aether_[a-z0-9]+(?:_[a-z0-9]+)*)$/.exec(call?.title ?? '')?.[1]
}

function isRead(tool: string): boolean {
  return tool in readTools || /^aether_(?:list|get)_/.test(tool)
}

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

/** A tool's result: JSON, or as Aether's tools write it, a one-line summary then JSON. */
function parsed(value: unknown): unknown {
  if (typeof value !== 'string') return value
  const text = value.trim()
  const json = text.startsWith('{') || text.startsWith('[') ? text : text.slice(text.indexOf('\n') + 1).trimStart()
  if (!json.startsWith('{') && !json.startsWith('[')) return value
  try {
    return JSON.parse(json)
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

/**
 * A deep link the result names. A dashboard link opens in this app whatever
 * its host, since the server writes its own configured dashboard address;
 * anything else, such as a pull request, opens in a new tab.
 */
function linkTarget(link: string): ActionTarget | undefined {
  let url: URL
  try {
    url = new URL(link, window.location.href)
  } catch {
    return undefined
  }
  const route = routeFromQuery(url.searchParams)
  if (route.name === 'run' && route.params.runId) return { kind: 'run', id: route.params.runId, route }
  if (route.name === 'missions' && route.params.missionId) return { kind: 'swarm', id: route.params.missionId, route }
  if (route.name === 'workspace' && route.params.workspaceId) return { kind: 'workspace', id: route.params.workspaceId, route }
  const watch = url.searchParams.get('watch')
  if (route.name === 'settings' && watch) return { kind: 'watch', id: watch, route: { ...route, params: { ...route.params, watch } } }
  if (url.origin === window.location.origin || !/^https?:$/.test(url.protocol)) return undefined
  return { kind: /\/pull\/\d+/.test(url.pathname) ? 'pr' : 'link', id: url.href, url: url.href }
}

export function runTarget(id: string): ActionTarget {
  return { kind: 'run', id, route: { name: 'run', params: { runId: id } } }
}

export function swarmTarget(id: string): ActionTarget {
  return { kind: 'swarm', id, route: { name: 'missions', params: { missionId: id } } }
}

function target(tool: string, input: Record<string, unknown>, result: Record<string, unknown>, saved?: AssistantActionRecord): ActionTarget | undefined {
  if (tool === 'aether_pr_merge' || tool === 'aether_pr_create') {
    const url = text(record(result.pull_request).url) ?? text(result.pr_url) ?? text(record(result.pr).url)
    if (url) return { kind: 'pr', id: url, url }
  }
  const link = text(result.link) ?? text(result.url) ?? text(record(result.launch).url)
  const linked = link ? linkTarget(link) : undefined
  if (linked) return linked
  const ids = record(result.ids)
  const run = text(saved?.result?.run_id) ?? text(result.run_id) ?? text(record(result.run).id) ?? text(ids.run_id)
  const swarm = text(result.swarm_id) ?? text(record(result.mission).id) ?? text(record(result.swarm).id) ?? text(ids.mission_id) ?? text(ids.swarm_id)
  const runID = run ?? text(input.run_id)
  if (tool === 'aether_swarm_manage' && input.action === 'release_worker' && runID) return runTarget(runID)
  if (tool === 'aether_save_environment') return { kind: 'environment', id: 'environment', route: { name: 'environment', params: {} } }
  const named = text(input.mission_id) ?? text(input.swarm_id)?.replace(/^swarm:/, '')
  if (swarm || ((tool.endsWith('_swarm') || tool === 'aether_swarm_manage' || tool === 'aether_replace_integrator') && named)) return swarmTarget(swarm ?? named!)
  if (runID) return runTarget(runID)
  const workspace = text(input.workspace_id)
  if (workspace) return { kind: 'workspace', id: workspace, route: { name: 'workspace', params: { workspaceId: workspace } } }
  return undefined
}

function errorText(result: unknown): string {
  if (typeof result === 'string') return result
  const body = record(result)
  return text(body.error) ?? text(record(body.error).message) ?? text(body.message) ?? JSON.stringify(result)
}

function contentText(call: SessionToolCall): string | undefined {
  return (call.content ?? []).filter((c) => c.type === 'text' && c.text).map((c) => c.text).join('\n\n') || undefined
}

/**
 * A call's arguments and result as the tool saw them. Codex wraps the
 * arguments as `{server, tool, arguments}` and the result as `{result,
 * error}` around the MCP result, and reports the call as a command, which
 * the step drops both of.
 */
function callValues(call: SessionToolCall | undefined, entry: WorkItem): { input: Record<string, unknown>; result: unknown } {
  if (!call) return { input: record(entry.input), result: parsed(entry.result) }
  const raw = record(call.raw_input)
  const input = raw.server === 'aether' && 'arguments' in raw ? record(raw.arguments) : raw
  const out = record(call.raw_output)
  if (codexTool(call) && ('result' in out || 'error' in out)) {
    const mcp = record(out.result)
    const said = Array.isArray(mcp.content) ? mcp.content.map((c) => text(record(c).text)).filter(Boolean).join('\n\n') : undefined
    return { input, result: out.error ? out.error : mcp.structuredContent ?? parsed(said) ?? out.result }
  }
  return { input, result: parsed(contentText(call) ?? call.raw_output) }
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
    case 'withdrawn':
      return { state: saved.outcome }
    case 'failed':
      return { state: 'failed', approval }
    case 'succeeded':
      // The call can still fail after its method succeeded, as when GitHub refuses a merge it was asked for.
      return { state: entry.status === 'failed' ? 'failed' : 'done', approval }
  }
  if (entry.status === 'failed') {
    if (!saved && error?.includes('the member declined this action')) return { state: 'declined' }
    if (!saved && error?.includes('the member did not answer within')) return { state: 'expired' }
    if (!saved && error?.includes('the turn or session ended before the member answered')) return { state: 'withdrawn' }
    return { state: 'failed', approval }
  }
  if (entry.status === 'running' && !approval && (entry.waiting || awaiting)) return { state: 'approval' }
  return { state: entry.status === 'running' ? 'running' : 'done', approval }
}

/** Records are keyed by the id of the event their notice shows as, calls by their step id. */
export interface TranscriptFacts {
  records: Map<string, AssistantActionRecord>
  calls: Map<string, SessionToolCall>
}

export function transcriptFacts(turns: Turn[] | undefined): TranscriptFacts {
  const facts: TranscriptFacts = { records: new Map(), calls: new Map() }
  for (const turn of turns ?? []) {
    for (const item of turn.items) {
      if (item.kind === 'notice' && item.notice?.action) facts.records.set(`ev:${item.seq}`, item.notice.action)
      if (item.kind === 'tool_call' && item.tool_call) facts.calls.set(`${turn.turn}:${item.tool_call.id}`, item.tool_call)
    }
  }
  return facts
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
  /** The id of the step block the call ran in. */
  block: string
  methods: string[]
  noted?: Approval
  saved?: AssistantActionRecord
}

function callKey(action: AssistantAction): string | undefined {
  return text(action.input.idempotency_key) ?? text(record(action.result).idempotency_key)
}

type TargetNames = Partial<Record<'run_id' | 'workspace_id' | 'watch_id' | 'swarm' | 'pr', string>>

function inputNames(input: Record<string, unknown>): TargetNames {
  return {
    run_id: text(input.run_id),
    workspace_id: text(input.workspace_id),
    watch_id: text(input.watch_id),
    swarm: text(input.swarm_id)?.replace(/^swarm:/, ''),
    pr: prKey(text(input.pr)),
  }
}

function recordNames(target: AssistantActionTarget | undefined): TargetNames {
  const watch = target?.watch
  return {
    run_id: target?.run_id ?? watch?.run_id,
    workspace_id: target?.workspace_id,
    watch_id: target?.watch_id,
    swarm: watch?.mission_id?.replace(/^swarm:/, ''),
    pr: watch?.repo && watch.number ? prKey(`${watch.repo}#${watch.number}`) : undefined,
  }
}

/** `owner/name#7` from that or a pull request URL, lowercased as GitHub matches names. */
function prKey(pr: string | undefined): string | undefined {
  const match = pr ? /([\w.-]+\/[\w.-]+?)(?:#|\/pull\/)(\d+)/.exec(pr) : null
  return match ? `${match[1]}#${match[2]}`.toLowerCase() : undefined
}

/**
 * The call a note belongs to. A record carries no tool call id; it is written
 * while its call runs, so it follows the call's first appearance. It goes to
 * the call that already holds its action, else to an unclaimed call of its
 * method whose input names no other target than the record's: the
 * oldest such call of the newest block holding one, since a block's
 * identical calls are approved in turn and an older block's call may never
 * have been recorded. An older server's note names the run only in its
 * summary, so it goes to the one call that fits; with more, it stays a row.
 */
function ownerOf(drafts: Draft[], note: Note, words: string): Draft | undefined {
  const open = drafts.filter((d) => d.methods.includes(note.method) && !d.noted && !d.saved)
  if (!note.saved) {
    const run = (d: Draft) => text(d.action.input.run_id)
    const fits = open.length > 1 ? open.filter((d) => run(d) && words.includes(run(d)!)) : open
    return fits.length === 1 ? fits[0] : undefined
  }
  const saved = note.saved
  const held = drafts.find((d) => d.saved?.id === saved.id)
  if (held) return held
  // A call's idempotency key, which the record repeats, names it exactly; a call with another key is not it.
  const keyed = saved.idempotency_key ? open.find((d) => callKey(d.action) === saved.idempotency_key) : undefined
  if (keyed) return keyed
  const recorded = recordNames(saved.target)
  // A watch record names its call's whole target, so a call naming less is another call.
  const whole = saved.target?.watch !== undefined || saved.target?.watch_id !== undefined
  const fits = open.filter((d) => (!saved.idempotency_key || !callKey(d.action)) && Object.entries(inputNames(d.action.input)).every(([key, named]) => {
    const other = recorded[key as keyof TargetNames]
    return whole ? named === other : !named || !other || named === other
  }))
  const newest = fits.at(-1)?.block
  return fits.find((d) => d.block === newest)
}

/**
 * Pulls the assistant's Aether tool calls out of the transcript's step
 * blocks into cards, consecutive reads into one line, and folds each
 * approval note or action record into the card of the call it belongs to.
 * A block splits around its calls, so each card stays where it ran.
 */
export function assistantView(rows: SessionRow[], confirmations: AssistantConfirmation[], { records, calls }: TranscriptFacts = transcriptFacts([])): AssistantView {
  const waitingMethods = new Set(confirmations.map((c) => c.method))
  const out: SessionRow[] = []
  const groups = new Map<string, ActionGroup[]>()
  const actions: AssistantAction[] = []
  const drafts: Draft[] = []
  const toolOf = (step: Step) => step.kind === 'tool' ? aetherTool(step.entry.name) ?? codexTool(calls.get(step.entry.id)) : undefined
  const draft = (entry: WorkItem, tool: string, block: string): AssistantAction => {
    const action: AssistantAction = {
      id: entry.id,
      callID: entry.id.slice(entry.id.indexOf(':') + 1),
      tool,
      read: isRead(tool),
      at: entry.at,
      state: 'running',
      ...callValues(calls.get(entry.id), entry),
      ...(entry.truncated && entry.seq !== undefined ? { seq: entry.seq } : {}),
    }
    drafts.push({ action, entry, block, methods: toolMethod(tool, action.input) })
    return action
  }
  const claim = (step: Step | SessionRow): boolean => {
    const note = noteOf(step, records)
    const owner = note && ownerOf(drafts, note, step.kind === 'event' ? step.text : '')
    if (!note || !owner) return false
    if (note.saved) owner.saved = note.saved
    else owner.noted = note.approval
    return true
  }
  const place = (action: AssistantAction) => {
    const anchor = out.at(-1)?.id ?? ''
    const list = groups.get(anchor) ?? []
    const last = list.at(-1)
    if (action.read && last?.kind === 'reads') last.actions.push(action)
    else if (action.read) list.push({ kind: 'reads', id: `reads:${action.id}`, actions: [action] })
    else {
      list.push({ kind: 'action', id: `action:${action.id}`, action })
      actions.push(action)
    }
    groups.set(anchor, list)
  }
  for (const row of rows) {
    if (row.kind !== 'steps') {
      if (!claim(row)) out.push(row)
      continue
    }
    if (!row.steps.some((step) => toolOf(step))) {
      const kept = row.steps.filter((step) => !claim(step))
      if (kept.length === row.steps.length) out.push(row)
      else if (kept.some((step) => workKinds.has(step.kind))) out.push({ ...stepsBlock(row.id, kept, row.start, row.current), live: row.live })
      else {
        out.push(...kept.map(asRow))
        if (row.live) out.push({ kind: 'live', id: 'live', at: row.at, ...row.live })
      }
      continue
    }
    let segment: Step[] = []
    let id = row.id
    const flush = (last: boolean) => {
      if (segment.length === 0) return
      if (segment.some((step) => workKinds.has(step.kind))) {
        out.push({ ...stepsBlock(id, segment, id === row.id ? row.start : segment[0]!.at, row.current && last), live: last ? row.live : undefined })
      } else out.push(...segment.map(asRow))
      segment = []
    }
    for (const step of row.steps) {
      const tool = toolOf(step)
      if (step.kind === 'tool' && tool) {
        flush(false)
        place(draft(step.entry, tool, row.id))
        id = `${row.id}:${step.id}`
      } else if (!claim(step)) segment.push(step)
    }
    const working = segment.some((step) => workKinds.has(step.kind))
    flush(true)
    if (!working && row.live) out.push({ kind: 'live', id: 'live', at: row.at, ...row.live })
  }
  for (const { action, entry, methods, noted, saved } of drafts) {
    const error = entry.status === 'failed' ? errorText(action.result) : undefined
    // A turn that ends shows its open calls as finished; the call's own status says whether it ever did.
    const status = calls.get(entry.id)?.status
    const cut = entry.status === 'done' && status !== undefined && status !== 'completed'
    const open = !saved || saved.outcome === 'approved' || saved.outcome === 'auto_approved'
    Object.assign(action, cut && open
      ? { state: 'unfinished', approval: saved ? recordApproval(saved) : noted }
      : actionOutcome(entry, error, methods.some((method) => waitingMethods.has(method)), noted, saved))
    // GitHub's reply to the create was lost and no pull request was found after: it may exist or not.
    if (action.state === 'done' && action.tool === 'aether_pr_create' && record(action.result).write === 'pending') action.state = 'unconfirmed'
    action.error = saved?.error ?? error
    // The server creates a watch that cannot check its target, such as a pull request without GitHub connected, in state error.
    const watched = record(action.result)
    if (action.state === 'done' && action.tool === 'aether_watch' && watched.state === 'error') {
      action.state = 'failed'
      action.error ??= text(watched.error)
    }
    action.target = target(action.tool, action.input, record(action.result), saved)
  }
  // The live line would name the running call by its raw tool title; its card already says what it does, and the approval card asks for a call waiting on it.
  const running = new Map(drafts.filter((d) => d.entry.status === 'running').map((d) => [d.entry.label, d.action]))
  const shown = out.flatMap((row): SessionRow[] => {
    const live = row.kind === 'live' ? row : row.kind === 'steps' ? row.live : undefined
    const action = live && running.get(live.label)
    if (!live || !action) return [row]
    const said = action.state === 'approval' ? undefined : { label: 'Generating…', tone: live.tone }
    if (row.kind === 'steps') return [{ ...row, live: said }]
    return said ? [{ ...row, ...said }] : []
  })
  return { rows: shown, groups, actions }
}

export interface ActionNames {
  workspace: (id: string) => string | undefined
  agent: (name: string) => string | undefined
  run: (id: string) => string | undefined
  /** The member who shares the account, or undefined for the member's own. */
  sharer: (id: string) => string | undefined
}

type Verb = [imperative: string, past: string, present: string]

const words = (verb: string): Verb => {
  const stem = verb.replace(/e$/, '')
  return [verb.charAt(0).toUpperCase() + verb.slice(1), `${verb.charAt(0).toUpperCase() + verb.slice(1)}${verb.endsWith('e') ? 'd' : 'ed'}`, `${stem.charAt(0).toUpperCase() + stem.slice(1)}ing`]
}

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
  const number = text(result.number) ?? text(record(result.pull_request).number) ?? text(record(result.pr).number) ?? text(action.input.number) ?? text(action.input.pr_number)
  return number ? `PR #${number}` : `the pull request of run ${runName(action, names)}`
}

function alreadyGone(action: AssistantAction): boolean {
  return action.state === 'done' && record(action.result).state === 'not_found'
}

function watchObject(action: AssistantAction, names: ActionNames): string {
  const pr = text(action.input.pr) ?? text(action.input.pr_number)
  if (pr) return `PR ${/^\d+$/.test(pr) ? `#${pr}` : pr}`
  const swarm = text(action.input.swarm_id)?.replace(/^swarm:/, '')
  if (swarm) return `swarm ${swarm}`
  return text(action.input.run_id) ? `run ${runName(action, names)}` : text(action.input.watch_id) ?? ''
}

const phrases: Record<string, { verb: Verb | ((action: AssistantAction) => Verb); object: (action: AssistantAction, names: ActionNames) => string }> = {
  aether_launch_run: { verb: ['Launch', 'Launched', 'Launching'], object: launchObject('run') },
  aether_launch_swarm: { verb: ['Launch', 'Launched', 'Launching'], object: launchObject('swarm') },
  aether_relaunch_run: runVerb(['Relaunch', 'Relaunched', 'Relaunching']),
  aether_message_run: runVerb(['Message', 'Messaged', 'Messaging']),
  aether_stop_run: {
    verb: (a) => (text(a.input.outcome) ? ['Close', 'Closed', 'Closing'] : ['Stop', 'Stopped', 'Stopping']),
    object: (a, n) => `run ${runName(a, n)}${text(a.input.outcome) ? ` as ${text(a.input.outcome)}` : ''}`,
  },
  aether_pause_run: runVerb(['Pause', 'Paused', 'Pausing']),
  aether_resume_run: runVerb(['Resume', 'Resumed', 'Resuming']),
  aether_close_run: runVerb(['Close', 'Closed', 'Closing']),
  aether_mark_reviewed: runVerb(['Mark', 'Marked', 'Marking'], ' reviewed'),
  aether_answer: runVerb(['Answer', 'Answered', 'Answering']),
  aether_decide_approval: { verb: ['Decide', 'Decided', 'Deciding'], object: (a, n) => `an approval for run ${runName(a, n)}` },
  aether_cancel_swarm: { verb: ['Cancel', 'Cancelled', 'Cancelling'], object: (a) => `swarm ${text(a.input.mission_id) ?? a.target?.id ?? ''}`.trimEnd() },
  aether_pr_merge: {
    verb: (a) => (record(a.result).write === 'pending' && record(a.result).merged === false ? ['Queue to merge', 'Queued to merge', 'Queueing to merge'] : ['Merge', 'Merged', 'Merging']),
    object: prName,
  },
  aether_save_environment: { verb: ['Save', 'Saved', 'Saving'], object: () => 'your environment' },
  aether_run_lifecycle: { verb: (a) => words(text(a.input.action) ?? 'change'), object: (a, n) => `run ${runName(a, n)}` },
  aether_switch_run_mode: { verb: ['Switch', 'Switched', 'Switching'], object: (a, n) => `run ${runName(a, n)} to ${text(a.input.mode) ?? 'another'} mode` },
  aether_handoff_run: { verb: ['Hand off', 'Handed off', 'Handing off'], object: (a, n) => `run ${runName(a, n)} to ${text(a.input.to_member_id) ?? 'a teammate'}` },
  aether_swarm_manage: {
    verb: (a) => (a.input.action === 'release_worker' ? ['Release', 'Released', 'Releasing'] : words(text(a.input.action) ?? 'change')),
    object: (a, n) => a.input.action === 'release_worker' ? `worker run ${runName(a, n)}` : `swarm ${text(a.input.swarm_id)?.replace(/^swarm:/, '') ?? ''}`.trimEnd(),
  },
  aether_replace_integrator: { verb: ['Replace', 'Replaced', 'Replacing'], object: (a) => `the integrator of swarm ${text(a.input.swarm_id)?.replace(/^swarm:/, '') ?? ''}`.trimEnd() },
  aether_integration_decide: { verb: (a) => words(text(a.input.action) ?? 'decide'), object: (a) => `candidate ${text(a.input.candidate_id) ?? ''}`.trimEnd() },
  aether_pr_create: { verb: ['Open', 'Opened', 'Opening'], object: (a, n) => `a pull request for run ${runName(a, n)}` },
  aether_memory_add: { verb: ['Add', 'Added', 'Adding'], object: (a) => (text(a.input.kind) ? `a ${text(a.input.kind)} note` : 'a note') },
  aether_memory_pin: { verb: (a) => (a.input.pin === false ? ['Unpin', 'Unpinned', 'Unpinning'] : ['Pin', 'Pinned', 'Pinning']), object: (a) => `note ${text(a.input.id) ?? ''}`.trimEnd() },
  aether_watch: {
    verb: (a) => !text(a.input.watch_id) ? ['Watch', 'Started watching', 'Starting to watch']
      : a.input.paused === true ? ['Pause', 'Paused', 'Pausing'] : a.input.paused === false ? ['Resume', 'Resumed', 'Resuming'] : ['Change', 'Changed', 'Changing'],
    object: (a, n) => (text(a.input.watch_id) ? `watch ${text(a.input.watch_id)}` : watchObject(a, n)),
  },
  aether_unwatch: {
    verb: (a) => (alreadyGone(a) ? ['Delete', 'Found no', 'Deleting'] : ['Delete', 'Deleted', 'Deleting']),
    object: (a) => `watch ${text(a.input.watch_id) ?? ''}`.trimEnd() + (alreadyGone(a) ? ' to delete' : ''),
  },
}

export function actionSummary(action: AssistantAction, names: ActionNames): string {
  const phrase = phrases[action.tool] ?? { verb: ['Use', 'Used', 'Using'] as Verb, object: () => action.tool }
  const [imperative, past, present] = typeof phrase.verb === 'function' ? phrase.verb(action) : phrase.verb
  const verb = action.state === 'done' ? past : action.state === 'running' || action.state === 'approval' ? present : imperative
  return `${verb} ${phrase.object(action, names)}`.trimEnd()
}

export function isKnownAction(tool: string): boolean {
  return tool in phrases
}

/**
 * What a launch started: the server's receipt when the tool returns one,
 * else the run it returned and the choices it was asked for.
 */
export function launchLine(action: AssistantAction, names: ActionNames): string | undefined {
  const result = record(action.result)
  const launch = record(result.launch)
  const kind = text(launch.kind)
  if (!kind) return action.state === 'done' && (action.tool === 'aether_launch_run' || action.tool === 'aether_launch_swarm') ? askedLaunch(action, result, names) : undefined
  const lead = kind === 'swarm' ? record(launch.integrator) : launch
  const workspace = text(launch.workspace_id)
  const harness = text(lead.harness)
  const requested = record(launch.requested_account)
  return [
    kind,
    workspace && (names.workspace(workspace) ?? workspace),
    harness && [names.agent(harness) ?? harness, text(lead.model)].filter(Boolean).join(' '),
    accountWords(record(lead.account), names),
    Object.keys(requested).length > 0 && `fell back from ${accountWords(requested, names)}`,
    fallbackWords(record(launch.fallback), names),
  ].filter(Boolean).join(' · ')
}

/** A receipt's account: `access` is own, shared (with the member) or other (a teammate's run on their own account). */
function accountWords(account: Record<string, unknown>, names: ActionNames): string {
  const member = text(account.member_id)
  const name = text(account.name) ?? (member && (names.sharer(member) ?? member))
  if (account.access === 'own' || !name) return 'your account'
  return account.access === 'shared' ? `${name}'s account, shared` : `${name}'s account`
}

/** The receipt's fallback policy, when it is not the automatic one. */
function fallbackWords(fallback: Record<string, unknown>, names: ActionNames): string | undefined {
  if (fallback.policy === 'off') return 'no fallback'
  const order = Array.isArray(fallback.order) ? fallback.order.map(text).filter((id): id is string => id !== undefined) : []
  return order.length ? `then ${order.map((id) => names.sharer(id) ?? 'your account').join(', ')}` : undefined
}

function askedLaunch(action: AssistantAction, result: Record<string, unknown>, names: ActionNames): string {
  const input = action.input
  const swarm = action.tool === 'aether_launch_swarm'
  const workspace = text(result.workspace_id) ?? text(input.workspace_id)
  const harness = text(result.harness) ?? text(input.harness)
  // Without account_member_id the server picks the account, and usage fallback may move it: name none.
  const account = text(input.account_member_id)
  const sharer = account ? names.sharer(account) : undefined
  return [
    swarm ? 'swarm' : 'run',
    workspace && (names.workspace(workspace) ?? workspace),
    harness && [names.agent(harness) ?? harness, text(input.model)].filter(Boolean).join(' '),
    account && (sharer ? `${sharer}'s account, shared` : 'your account'),
    input.no_fallback === true ? 'no fallback' : undefined,
  ].filter(Boolean).join(' · ')
}

export function watchLine(action: AssistantAction): string | undefined {
  if (action.tool !== 'aether_watch') return undefined
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

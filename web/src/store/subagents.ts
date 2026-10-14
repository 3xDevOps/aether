import type { SessionItem, SessionToolCall } from '@/lib/session-types'
import { isLaunch, patchCounts, toolLabel, toolStatus, type ChangedFile, type Rehome, type SessionRow, type Turn } from '@/store/session-rows'

/** A background task's own status wins; otherwise done and failed come from
 * the launching call's status; stopped is a call still in progress when its
 * turn ended; launched is a Workflow or background Agent call that returned
 * once its work started; unknown is a subagent whose launching call is not
 * loaded. */
export type AgentState = 'running' | 'done' | 'failed' | 'stopped' | 'launched' | 'unknown'

export interface Subagent {
  id: string
  parentID: string
  workflow: boolean
  background?: boolean
  call?: SessionToolCall
  callSeq?: number
  callTruncated?: boolean
  launchSeq?: number
  /** The oldest snapshot held opened the call; a replay that starts later
   * holds a settled one, so the start time and earlier items are unknown. */
  opened?: boolean
  turn?: number
  startedAt?: string
  settledAt?: string
  settledSeq?: number
  turnEndedAt?: string
  turnEndedSeq?: number
  items: SessionItem[]
  children: string[]
  latest?: SessionItem
  /** Outlives latest, which a trim frees. Order is by seq: times can tie. */
  activeSeq?: number
  tools: number
  /** Line counts of the latest diffs of each of its own tool calls. */
  edits?: Record<string, ChangedFile[]>
}

export interface AgentIndex {
  agents: Record<string, Subagent>
  roots: string[]
  /** Mutated in place once non-empty, so each session owns its own; an
   * empty map is replaced, never filled, which keeps noAgents empty.
   * Components never read them. */
  calls: Map<string, { parent: string; turn: number; open: boolean }>
  endedTurns: Map<number, { time: string; seq: number }>
  /** A launch id seen again after its call settled names a new subagent,
   * keyed `<id>@<seq>`, for items from that seq on. */
  aliases: Map<string, { key: string; seq: number }>
}

export const noAgents: AgentIndex = { agents: {}, roots: [], calls: new Map(), endedTurns: new Map(), aliases: new Map() }

function resolve(aliases: AgentIndex['aliases'], id: string, seq: number): string {
  const alias = aliases.get(id)
  return alias && seq >= alias.seq ? alias.key : id
}

function ownerOf(item: SessionItem, index: Pick<AgentIndex, 'calls' | 'aliases'>): string {
  let owner = ''
  switch (item.kind) {
    case 'tool_call':
      owner = item.tool_call ? item.tool_call.parent_id || index.calls.get(item.tool_call.id)?.parent || '' : ''
      break
    case 'message':
    case 'thought':
      owner = item.message?.parent_id || ''
      break
    case 'plan':
      owner = item.plan_parent_id || ''
  }
  return owner && resolve(index.aliases, owner, item.seq)
}

/** The item as the timeline holds it: a reused launch carries its alias. */
export function localItem(index: AgentIndex, item: SessionItem): SessionItem {
  const call = item.tool_call
  const key = call ? resolve(index.aliases, call.id, item.seq) : ''
  return call && key !== call.id ? { ...item, tool_call: { ...call, id: key } } : item
}

const activityKinds = ['message', 'thought', 'tool_call', 'plan']

function bySeq(agents: Record<string, Subagent>) {
  return (a: string, b: string) => {
    const x = agents[a]?.launchSeq ?? Infinity
    const y = agents[b]?.launchSeq ?? Infinity
    return x < y ? -1 : x > y ? 1 : 0
  }
}

/** home maps each main tool call snapshot logged after its call's first
 * turn to that turn. A settled call whose id comes back in a later turn is
 * a new call: the server only carries over calls still open at turn end. */
export function indexItems(index: AgentIndex, items: SessionItem[], older = false): { index: AgentIndex; main: SessionItem[]; rehomed: Rehome[]; home: Map<SessionItem, number> } {
  let { agents, roots, calls, endedTurns, aliases } = index
  let changed = false
  let agentsCopied = false
  let rootsCopied = false
  const main: SessionItem[] = []
  const rehomed: Rehome[] = []
  const home = new Map<SessionItem, number>()
  const fresh = new Set<string>()
  const earlier = new Map<string, { to: number; open: boolean }>()
  const touched = new Set<string>()
  const childrenCopied = new Set<string>()
  const settled = new Set<string>()
  const collected = new Map<string, SessionItem[]>()
  const unsorted = new Set<string>()

  const edit = (id: string): Subagent => {
    if (!agentsCopied) {
      agents = { ...agents }
      agentsCopied = true
      changed = true
    }
    if (!touched.has(id)) {
      agents[id] = { ...(agents[id] ?? { id, parentID: '', workflow: false, items: [], children: [], tools: 0 }) }
      touched.add(id)
    }
    return agents[id]!
  }
  const link = (parentID: string, id: string) => {
    if (parentID) {
      const parent = edit(parentID)
      if (!childrenCopied.has(parentID)) {
        parent.children = parent.children.slice()
        childrenCopied.add(parentID)
      }
      parent.children.push(id)
    } else {
      if (!rootsCopied) {
        roots = roots.slice()
        rootsCopied = true
        changed = true
      }
      roots.push(id)
    }
    unsorted.add(parentID)
  }
  const writable = <K, V>(map: Map<K, V>, original: Map<K, V>) => (map === original && map.size === 0 ? new Map<K, V>() : map)

  for (const item of items) {
    const call = item.tool_call
    const known = call && calls.get(call.id)
    const reused = known && item.turn > known.turn && !known.open && (!older || fresh.has(call.id))
    if (reused && isLaunch(call) && agents[resolve(aliases, call.id, item.seq)]?.launchSeq !== undefined) {
      aliases = writable(aliases, index.aliases)
      aliases.set(call.id, { key: `${call.id}@${item.seq}`, seq: item.seq })
      changed = true
    }
    const owner = reused ? resolve(aliases, call.parent_id || '', item.seq) : ownerOf(item, { calls, aliases })
    const key = call ? resolve(aliases, call.id, item.seq) : ''
    if (item.kind === 'turn_end') {
      if (!older || !endedTurns.has(item.turn)) {
        endedTurns = writable(endedTurns, index.endedTurns)
        endedTurns.set(item.turn, { time: item.time, seq: item.seq })
        changed = true
      }
      for (const agent of Object.values(agents)) {
        if (agent.turn === item.turn && !agent.turnEndedAt) Object.assign(edit(agent.id), { turnEndedAt: item.time, turnEndedSeq: item.seq })
      }
    } else if (item.kind === 'tool_call' && call) {
      const open = !settles(call)
      if (!known || reused) {
        calls = writable(calls, index.calls)
        calls.set(call.id, { parent: owner, turn: item.turn, open })
        fresh.add(call.id)
        changed = true
        if (!known && owner) edit(owner).tools++
      } else if (older && !fresh.has(call.id)) {
        if (item.turn < known.turn) earlier.set(call.id, { to: Math.min(item.turn, earlier.get(call.id)?.to ?? item.turn), open })
        else if (item.turn > known.turn && owner === '') home.set(item, known.turn)
      } else {
        if (item.turn > known.turn && owner === '') home.set(item, known.turn)
        known.open = open
      }
      if (isLaunch(call) || agents[key]) {
        const agent = edit(key)
        if (agent.launchSeq === undefined) {
          agent.parentID = owner
          link(owner, key)
        }
        if (call.tool_name === 'Workflow') agent.workflow = true
        if (runsInBackground(call)) agent.background = true
        if (agent.callSeq === undefined || item.seq > agent.callSeq || (item.seq === agent.callSeq && agent.callTruncated)) {
          agent.call = call
          agent.callSeq = item.seq
          agent.callTruncated = Boolean(item.truncated)
        }
        if (agent.launchSeq === undefined || item.seq < agent.launchSeq) {
          if (agent.launchSeq !== undefined) unsorted.add(agent.parentID)
          agent.launchSeq = item.seq
          agent.opened = !settles(call)
          agent.startedAt = agent.opened ? item.time : undefined
          agent.turn = item.turn
          agent.turnEndedAt = endedTurns.get(item.turn)?.time
          agent.turnEndedSeq = endedTurns.get(item.turn)?.seq
        }
        if (settles(call) && (older ? !settled.has(call.id) : agent.settledAt === undefined)) {
          agent.settledAt = item.time
          agent.settledSeq = item.seq
          settled.add(call.id)
        }
      }
    }
    if (owner === '') {
      main.push(key && key !== call!.id ? { ...item, tool_call: { ...call!, id: key } } : item)
      continue
    }
    const list = collected.get(owner)
    if (list) list.push(item)
    else collected.set(owner, [item])
    if (activityKinds.includes(item.kind) && item.seq > (agents[owner]?.activeSeq ?? -1)) {
      const agent = edit(owner)
      agent.latest = item
      agent.activeSeq = item.seq
    }
    if (call?.diffs?.length && (!older || !index.agents[owner]?.edits?.[call.id])) {
      const agent = edit(owner)
      agent.edits = { ...agent.edits, [call.id]: editCounts(call) }
    }
  }

  for (const [id, list] of collected) {
    const agent = edit(id)
    agent.items = older ? [...list, ...agent.items] : [...agent.items, ...list]
  }
  for (const [id, { to, open }] of earlier) {
    const known = calls.get(id)!
    if (!open) continue
    if (known.parent === '') rehomed.push({ id, from: known.turn, to })
    known.turn = to
  }
  if (older) {
    for (const id of unsorted) (id ? agents[id]!.children : roots).sort(bySeq(agents))
  }
  return { index: changed ? { agents, roots, calls, endedTurns, aliases } : index, main, rehomed, home }
}

/** Frees what a trimmed agent's card does not show; the call keeps what
 * its state and label read and is read whole again on demand. */
export function trimAgents(index: AgentIndex, oldestSeq: number): AgentIndex {
  let agents: Record<string, Subagent> | undefined
  for (const [id, agent] of Object.entries(index.agents)) {
    const keep = agent.items.findIndex((item) => item.seq >= oldestSeq)
    const cut = keep === -1 ? agent.items.length : keep
    const stale = agent.call !== undefined && agent.callSeq! < oldestSeq && !agent.callTruncated
    if (cut === 0 && !stale) continue
    agents ??= { ...index.agents }
    agents[id] = {
      ...agent,
      items: agent.items.slice(cut),
      latest: agent.latest && agent.latest.seq < oldestSeq ? undefined : agent.latest,
      ...(stale ? { call: slimCall(agent.call!), callTruncated: true } : {}),
    }
  }
  return agents ? { ...index, agents } : index
}

function slimCall(call: SessionToolCall): SessionToolCall {
  const raw = call.raw_input && typeof call.raw_input === 'object' ? (call.raw_input as Record<string, unknown>) : {}
  const { description, name } = raw
  return {
    id: call.id,
    parent_id: call.parent_id,
    title: call.title,
    tool_kind: call.tool_kind,
    tool_name: call.tool_name,
    subagent: call.subagent,
    task: call.task,
    status: call.status,
    raw_input: { description, name },
  }
}

export function replaceAgentItem(index: AgentIndex, whole: SessionItem): AgentIndex {
  const agents = { ...index.agents }
  let matched = false
  const owner = agents[ownerOf(whole, index)]
  const at = owner?.items.findIndex((item) => item.seq === whole.seq) ?? -1
  if (owner && at !== -1) {
    const items = owner.items.slice()
    items[at] = whole
    const call = whole.tool_call
    const newest = call?.diffs?.length && !items.slice(at + 1).some((later) => later.tool_call?.id === call.id && later.tool_call.diffs?.length)
    agents[owner.id] = {
      ...owner,
      items,
      latest: owner.latest?.seq === whole.seq ? whole : owner.latest,
      ...(newest ? { edits: { ...owner.edits, [call.id]: editCounts(call) } } : {}),
    }
    matched = true
  }
  const launched = whole.tool_call && agents[resolve(index.aliases, whole.tool_call.id, whole.seq)]
  if (launched && launched.callSeq === whole.seq) {
    agents[launched.id] = { ...launched, call: whole.tool_call, callTruncated: false, background: launched.background || runsInBackground(whole.tool_call!) }
    matched = true
  }
  return matched ? { ...index, agents } : index
}

function runsInBackground(call: SessionToolCall): boolean {
  return (call.raw_input as Record<string, unknown> | undefined)?.run_in_background === true
}

function editCounts(call: SessionToolCall): ChangedFile[] {
  return (call.diffs ?? []).map((diff) => ({ path: diff.path, ...patchCounts(diff.patch) }))
}

function settles(call: SessionToolCall): boolean {
  return call.task ? call.task.status !== 'running' : call.status === 'completed' || call.status === 'failed'
}

const taskStates: Record<NonNullable<SessionToolCall['task']>['status'], AgentState> = {
  running: 'running',
  completed: 'done',
  failed: 'failed',
  stopped: 'stopped',
}

export function agentState(agent: Subagent): AgentState {
  const status = agent.call?.status
  if (!agent.call) return 'unknown'
  if (agent.call.task) return taskStates[agent.call.task.status]
  if (status === 'failed') return 'failed'
  if (status === 'completed') {
    if (!agent.workflow && !agent.background) return 'done'
    return (agent.activeSeq ?? -1) > (agent.settledSeq ?? Infinity) ? 'running' : 'launched'
  }
  return agent.turnEndedSeq !== undefined && (agent.activeSeq ?? -1) < agent.turnEndedSeq ? 'stopped' : 'running'
}

function rawInput(agent: Subagent): Record<string, unknown> {
  const raw = agent.call?.raw_input
  return raw && typeof raw === 'object' ? (raw as Record<string, unknown>) : {}
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

export function agentLabel(agent: Subagent): string {
  const raw = rawInput(agent)
  return agent.call?.subagent?.description || (agent.workflow ? text(raw.name) : undefined) || text(raw.description) || agent.call?.title || 'Subagent'
}

export function agentType(agent: Subagent): string | undefined {
  return agent.workflow ? 'Workflow' : agent.call?.subagent?.type
}

export function agentReport(agent: Subagent): string | undefined {
  const state = agentState(agent)
  if ((state !== 'done' && state !== 'failed') || agent.call?.task) return undefined
  const report = (agent.call?.content ?? []).filter((c) => c.type === 'text').map((c) => c.text ?? '').join('\n\n').trim()
  return report || undefined
}

export function agentPrompt(agent: Subagent): string | undefined {
  const raw = rawInput(agent)
  return typeof raw.prompt === 'string' ? raw.prompt : agent.workflow ? text(raw.script) : undefined
}

export function activityLabel(item: SessionItem): string | undefined {
  const call = item.tool_call
  switch (item.kind) {
    case 'tool_call':
      if (!call) return undefined
      return isLaunch(call) ? `Started ${call.subagent?.description || call.title}` : toolLabel(call, toolStatus(call))
    case 'message': {
      const line = (item.message?.text ?? '').split('\n').map((l) => l.trim()).filter(Boolean).at(-1)
      return line?.slice(0, 120)
    }
    case 'thought':
      return 'Thinking'
    case 'plan':
      return 'Updated the plan'
    default:
      return undefined
  }
}

const turns = new WeakMap<Subagent, Turn>()

export function agentTurn(agent: Subagent): Turn {
  let turn = turns.get(agent)
  if (!turn) {
    turn = { turn: agent.turn ?? 0, items: agent.items, closed: agentState(agent) !== 'running' }
    turns.set(agent, turn)
  }
  return turn
}

export function agentLoaded(agent: Subagent, oldestSeq: number): boolean {
  const first = agent.items[0]?.seq ?? Infinity
  return agent.opened === true && agent.launchSeq! >= oldestSeq && first > agent.launchSeq!
}

export function siblings(index: AgentIndex, agent: Subagent): string[] {
  return agent.parentID ? index.agents[agent.parentID]?.children ?? [] : index.roots
}

export function ancestry(index: AgentIndex, id: string): Subagent[] {
  const path: Subagent[] = []
  const seen = new Set<string>()
  for (let agent = index.agents[id]; agent && !seen.has(agent.id); agent = index.agents[agent.parentID]) {
    seen.add(agent.id)
    path.unshift(agent)
  }
  return path
}

interface TurnExtras {
  files?: ChangedFile[]
  orphans?: { ids: string[]; at: string }
  sig: string
}

const extrasCache = new WeakMap<AgentIndex, { oldestSeq: number; extras: Map<number, TurnExtras> }>()

function subtreeEdits(index: AgentIndex, id: string, files: Map<string, ChangedFile>, seen: Set<string>): void {
  const agent = index.agents[id]
  if (!agent || seen.has(id)) return
  seen.add(id)
  for (const list of Object.values(agent.edits ?? {})) addFiles(files, list)
  for (const child of agent.children) subtreeEdits(index, child, files, seen)
}

function addFiles(files: Map<string, ChangedFile>, list: ChangedFile[]): void {
  for (const file of list) {
    const prior = files.get(file.path)
    files.set(file.path, { path: file.path, additions: (prior?.additions ?? 0) + file.additions, deletions: (prior?.deletions ?? 0) + file.deletions })
  }
}

/** Per main turn: the files its subagents changed, and the subagents none of
 * whose launch snapshots is held, placed at their first held item. */
export function turnExtras(index: AgentIndex, oldestSeq: number): Map<number, TurnExtras> {
  const cached = extrasCache.get(index)
  if (cached?.oldestSeq === oldestSeq) return cached.extras
  const held = (agent: Subagent) => agent.launchSeq !== undefined && Math.max(agent.launchSeq, agent.callSeq ?? -1) >= oldestSeq
  const files = new Map<number, Map<string, ChangedFile>>()
  const orphans = new Map<number, { ids: string[]; at: string }>()
  const seen = new Set<string>()
  for (const agent of Object.values(index.agents)) {
    const orphan = !held(agent) && agent.items.length > 0
    if (!orphan && (agent.parentID || !held(agent))) continue
    const turn = orphan ? agent.items[0]!.turn : agent.turn!
    if (orphan) {
      const held = orphans.get(turn)
      if (held) held.ids.push(agent.id)
      else orphans.set(turn, { ids: [agent.id], at: agent.items[0]!.time })
    }
    const changed = files.get(turn) ?? new Map<string, ChangedFile>()
    subtreeEdits(index, agent.id, changed, seen)
    if (changed.size) files.set(turn, changed)
  }
  const extras = new Map<number, TurnExtras>()
  for (const turn of new Set([...files.keys(), ...orphans.keys()])) {
    const changed = files.get(turn) && [...files.get(turn)!.values()]
    const lost = orphans.get(turn)
    const sig = [...(changed ?? []).map((f) => `${f.path}+${f.additions}-${f.deletions}`), ...(lost ? [lost.at, ...lost.ids] : [])].join('|')
    extras.set(turn, { files: changed, orphans: lost, sig })
  }
  extrasCache.set(index, { oldestSeq, extras })
  return extras
}

/** A signature that changes only when turnExtras would change a row. */
export function extrasSig(index: AgentIndex, oldestSeq: number): string {
  return [...turnExtras(index, oldestSeq)].map(([turn, { sig }]) => `${turn}:${sig}`).join('\n')
}

const merged = new WeakMap<SessionRow[], { sig: string; rows: SessionRow[] }>()

/** Folds a main turn's subagent file changes into its changed-files row,
 * each path once, and shows subagents whose launch is not loaded. */
export function withAgentRows(rows: SessionRow[], turn: number, extras: Map<number, TurnExtras>): SessionRow[] {
  const extra = extras.get(turn)
  if (!extra) return rows
  const held = merged.get(rows)
  if (held?.sig === extra.sig) return held.rows
  const out = rows.slice()
  if (extra.orphans) {
    const { ids, at } = extra.orphans
    const after = out.findIndex((row) => Date.parse(row.at) > Date.parse(at))
    out.splice(after === -1 ? out.length : after, 0, { kind: 'subagents', id: `orphans:${turn}`, at, ids })
  }
  if (extra.files) {
    const at = out.findIndex((row) => row.kind === 'changed-files')
    const files = new Map<string, ChangedFile>()
    const own = at === -1 ? undefined : out[at] as Extract<SessionRow, { kind: 'changed-files' }>
    addFiles(files, own?.files ?? [])
    addFiles(files, extra.files)
    const end = out.findIndex((row) => row.kind === 'finished')
    const row: SessionRow = { kind: 'changed-files', id: `files:${turn}`, at: own?.at ?? out[end]?.at ?? out.at(-1)?.at ?? '', files: [...files.values()] }
    if (own) out[at] = row
    else out.splice(end === -1 ? out.length : end, 0, row)
  }
  merged.set(rows, { sig: extra.sig, rows: out })
  return out
}

const namespaced = new WeakMap<SessionRow[], SessionRow[]>()

/** A subagent's rows, with ids apart from the main turn's and other agents'. */
export function agentRows(agent: Subagent, rows: SessionRow[]): SessionRow[] {
  let out = namespaced.get(rows)
  if (!out) {
    out = rows.map((row) => ({ ...row, id: `agent:${agent.id}/${row.id}` }))
    namespaced.set(rows, out)
  }
  return out
}

import type { Tone } from '@/components/ui/status-dot'
import type { SessionItem, SessionRequest, SessionToolCall } from '@/lib/session-types'
import { toolTenses } from '@/store/activity'

export type Delivery = 'Queued' | 'Sent' | 'Not sent' | 'Delivery uncertain' | 'Denied' | 'Cancelled'

export type WorkStatus = 'running' | 'done' | 'failed'

export interface WorkItem {
  id: string
  tool: string
  label: string
  status: WorkStatus
  at: string
  durationMs?: number
  seq?: number
  truncated?: boolean
  command?: string
  output?: string
  exitCode?: number
  diffs?: { path: string; patch: string }[]
}

export interface MessageImageRef {
  messageID: string
  index: number
}

/** Whether an attachment path is one terminal.file returned rather than a
 * terminal.image one. */
export function isAttachedFile(path: string): boolean {
  return /\/\.aether\/terminal-files\/[0-9a-f]{32}\/[^/]+$/.test(path)
}

export type SessionRow =
  | {
      kind: 'user'
      id: string
      at: string
      body: string
      images?: MessageImageRef[]
      /** Container paths of attached files that are not images. */
      files?: string[]
      authorID?: string
      delivery?: Delivery
      deliverAfter?: string
      failure?: string
    }
  | { kind: 'note'; id: string; at: string; authorID: string; body: string; question?: boolean }
  | { kind: 'assistant'; id: string; at: string; text: string; streaming: boolean }
  | { kind: 'thinking'; id: string; at: string; text: string }
  | { kind: 'work'; id: string; at: string; summary: string; entries: WorkItem[] }
  | { kind: 'subagents'; id: string; at: string; ids: string[] }
  | { kind: 'live'; id: string; at: string; label: string; waiting?: boolean }
  | { kind: 'terminal-note'; id: string; at: string }
  | { kind: 'plan'; id: string; at: string; entries: { content: string; status?: string }[] }
  | {
      kind: 'request'
      id: string
      at: string
      answer: 'input' | 'reply'
      title: string
      body?: string
      authorID?: string
      reply?: { authorID: string; body: string }
    }
  | { kind: 'answered'; id: string; at: string; request: SessionRequest; command?: string }
  | { kind: 'event'; id: string; at: string; text: string; detail?: string; tone?: Tone; report?: true }
  | { kind: 'finished'; id: string; at: string; text: string; tone: Tone }
  | { kind: 'agent-message'; id: string; at: string; messageID: string }

const categories: [RegExp, string, string, string][] = [
  [/^(bash|execute)$/, 'Ran', 'command', 'commands'],
  [/^(read)$/, 'Read', 'file', 'files'],
  [/^(edit|multiedit|notebookedit|write|delete|move)$/, 'Edited', 'file', 'files'],
  [/^(grep|glob|websearch|search)$/, 'Ran', 'search', 'searches'],
  [/^(webfetch|fetch)$/, 'Fetched', 'page', 'pages'],
  [/^(todowrite)$/, 'Updated', 'plan', 'plans'],
  [/^(subagent)$/, 'Delegated', 'task', 'tasks'],
]

export function workSummary(tools: string[]): string {
  const counts = new Map<string, { verb: string; one: string; many: string; n: number }>()
  for (const tool of tools) {
    const match = categories.find(([pattern]) => pattern.test(tool.toLowerCase())) ?? ['', 'Used', 'tool', 'tools']
    const key = `${match[1]} ${match[3]}`
    const entry = counts.get(key) ?? { verb: match[1], one: match[2], many: match[3], n: 0 }
    entry.n++
    counts.set(key, entry)
  }
  const parts = [...counts.values()].map(({ verb, one, many, n }, index) =>
    `${index === 0 ? verb : verb.toLowerCase()} ${n} ${n === 1 ? one : many}`)
  return parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}` : parts[0] ?? ''
}

export interface Turn {
  turn: number
  items: SessionItem[]
  closed: boolean
}

const inboxWake = /^Aether has \d+ unacknowledged inbox item\(s\)\./
const missionWake = /^Mission update: run \/usr\/local\/bin\/aether-internal mission plan show/

const launchTools = ['Agent', 'Task', 'Workflow']

export function isLaunch(call: SessionToolCall): boolean {
  return Boolean(call.subagent) || launchTools.includes(call.tool_name ?? '')
}

export function toolStatus(call: SessionToolCall): WorkStatus {
  if (call.status === 'failed' || (call.exit_code !== undefined && call.exit_code !== 0)) return 'failed'
  return call.status === 'completed' ? 'done' : 'running'
}

function toolTarget(call: SessionToolCall): string {
  const path = call.locations?.[0]?.path ?? call.diffs?.[0]?.path
  if (path && call.tool_kind !== 'execute') return path
  const [present, past] = toolTenses({ tool: call.tool_kind })
  const first = call.title.split(' ', 1)[0]!
  const verbs = [present, past, call.tool_kind, 'Edit', 'Write', 'Run', 'Search', 'Fetch'].map((verb) => verb?.toLowerCase())
  return verbs.includes(first.toLowerCase()) && call.title.length > first.length ? call.title.slice(first.length + 1) : call.title
}

export function toolLabel(call: SessionToolCall, status: WorkStatus): string {
  const [present, past] = toolTenses({ tool: call.tool_kind ?? 'other' })
  return `${status === 'running' ? present : past} ${toolTarget(call)}`.trimEnd()
}

interface ToolTrack {
  call: SessionToolCall
  first: SessionItem
  last: SessionItem
  output: string
  truncated: boolean
}

function workItem(turn: number, track: ToolTrack, ended: boolean): WorkItem {
  const { call } = track
  const reported = toolStatus(call)
  const status = ended && reported === 'running' ? 'done' : reported
  const took = Date.parse(track.last.time) - Date.parse(track.first.time)
  return {
    id: `${turn}:${call.id}`,
    tool: call.tool_kind ?? 'other',
    label: toolLabel(call, status),
    status,
    at: track.first.time,
    durationMs: status === 'running' || took < quietDuration ? undefined : took,
    seq: track.last.seq,
    truncated: track.truncated,
    command: call.tool_kind === 'execute' ? call.title : undefined,
    output: track.output || undefined,
    exitCode: call.exit_code,
    diffs: call.diffs?.length ? call.diffs : undefined,
  }
}

const stopText: Record<string, [string, Tone]> = {
  end_turn: ['Finished', 'done'],
  cancelled: ['Cancelled', 'paused'],
  interrupted: ['Interrupted', 'failed'],
  max_tokens: ['Stopped: the reply reached its token limit', 'failed'],
  max_turn_requests: ['Stopped: too many model requests in one turn', 'failed'],
  refusal: ['Stopped: the agent refused', 'failed'],
}

const quietDuration = 50

function seconds(ms: number): string {
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`
}

type Slot =
  | { kind: 'row'; row: SessionRow }
  | { kind: 'message'; id: string; role: string; first: SessionItem; text: string; images: MessageImageRef[]; files: string[]; complete: boolean }
  | { kind: 'tool'; id: string }
  | { kind: 'agent'; id: string }

function imageRefs(item: SessionItem): MessageImageRef[] {
  const images: MessageImageRef[] = []
  for (const content of item.message?.attachments ?? []) {
    if (content.type !== 'image' || !content.uri) continue
    const match = /^aether:\/\/room\/([^/?#\s]+)\/([0-9]+)$/.exec(content.uri)
    if (!match || match[0] !== content.uri) continue
    const index = Number(match[2])
    if (!Number.isSafeInteger(index)) continue
    try {
      const messageID = decodeURIComponent(match[1]!)
      if (!images.some((image) => image.messageID === messageID && image.index === index)) images.push({ messageID, index })
    } catch {
      // A malformed escaped ID is not an authenticated room reference.
    }
  }
  return images
}

function fileRefs(item: SessionItem): string[] {
  const files: string[] = []
  for (const content of item.message?.attachments ?? []) {
    if (content.type !== 'resource_link' || !content.uri?.startsWith('file://')) continue
    try {
      const path = decodeURIComponent(new URL(content.uri).pathname)
      if (isAttachedFile(path) && !files.includes(path)) files.push(path)
    } catch {
      // A malformed URI is not a file Aether attached.
    }
  }
  return files
}

function appendImages(images: MessageImageRef[], incoming: MessageImageRef[]): void {
  for (const image of incoming) {
    if (!images.some((held) => held.messageID === image.messageID && held.index === image.index)) images.push(image)
  }
}

export function turnRows(turn: Turn): SessionRow[] {
  const slots: Slot[] = []
  const messages = new Map<string, Extract<Slot, { kind: 'message' }>>()
  const tools = new Map<string, ToolTrack>()
  const requests = new Map<string, { request: SessionRequest; item: SessionItem }>()
  let plan: { item: SessionItem; at: number } | null = null
  let started: SessionItem | null = null
  let ended: SessionItem | null = null

  for (const item of turn.items) {
    switch (item.kind) {
      case 'turn_start':
        started = item
        break
      case 'turn_end':
        ended = item
        break
      case 'message':
      case 'thought': {
        const m = item.message
        if (!m) break
        const key = `${item.kind}:${turn.turn}:${m.message_id}`
        const open = messages.get(key)
        if (open) {
          open.text += m.text
          appendImages(open.images, imageRefs(item))
          for (const file of fileRefs(item)) if (!open.files.includes(file)) open.files.push(file)
          open.complete ||= Boolean(m.complete)
          break
        }
        const slot = { kind: 'message' as const, id: key, role: item.kind === 'thought' ? 'thought' : m.role, first: item, text: m.text, images: imageRefs(item), files: fileRefs(item), complete: Boolean(m.complete) }
        messages.set(key, slot)
        slots.push(slot)
        break
      }
      case 'tool_call': {
        const call = item.tool_call
        if (!call) break
        const track = tools.get(call.id)
        if (track) {
          track.call = { ...call, output: undefined }
          track.output += call.output ?? ''
          track.last = item
          track.truncated ||= Boolean(item.truncated)
          break
        }
        tools.set(call.id, { call, first: item, last: item, output: call.output ?? '', truncated: Boolean(item.truncated) })
        slots.push({ kind: isLaunch(call) ? 'agent' : 'tool', id: call.id })
        break
      }
      case 'plan':
        if (!plan) slots.push({ kind: 'row', row: { kind: 'plan', id: `plan:${turn.turn}`, at: item.time, entries: [] } })
        plan = { item, at: slots.length }
        break
      case 'request': {
        const request = item.request
        if (!request) break
        if (!requests.has(request.id)) {
          slots.push({ kind: 'row', row: { kind: 'answered', id: `req:${turn.turn}:${request.id}`, at: item.time, request } })
        }
        requests.set(request.id, { request, item })
        break
      }
      case 'notice':
        if (item.notice) {
          slots.push({ kind: 'row', row: {
            kind: 'event',
            id: `ev:${item.seq}`,
            at: item.time,
            text: item.notice.title,
            detail: item.notice.description,
            tone: item.notice.severity === 'error' ? 'failed' : item.notice.severity === 'warning' ? 'needs-you' : 'neutral',
          } })
        }
        break
      case 'mode_change':
        if (turn.turn > 0 && item.mode) slots.push({ kind: 'row', row: { kind: 'event', id: `ev:${item.seq}`, at: item.time, text: `Mode set to ${item.mode}` } })
        break
      case 'reset':
        slots.push({ kind: 'row', row: { kind: 'event', id: `ev:${item.seq}`, at: item.time, text: 'New agent session' } })
        break
    }
  }

  const rows: SessionRow[] = []
  let work: ToolTrack[] = []
  const flush = () => {
    if (work.length === 0) return
    const entries = work.map((track) => workItem(turn.turn, track, (turn.closed || ended !== null) && !(ended && track.last.seq > ended.seq)))
    rows.push({ kind: 'work', id: `work:${turn.turn}:${work[0]!.call.id}`, at: work[0]!.first.time, summary: workSummary(entries.map((e) => e.tool)), entries })
    work = []
  }
  let agents: string[] = []
  const flushAgents = () => {
    if (agents.length === 0) return
    rows.push({ kind: 'subagents', id: `agents:${turn.turn}:${agents[0]}`, at: tools.get(agents[0]!)!.first.time, ids: agents })
    agents = []
  }
  for (const slot of slots) {
    if (slot.kind === 'agent') {
      flush()
      agents.push(slot.id)
      continue
    }
    flushAgents()
    if (slot.kind === 'tool') {
      work.push(tools.get(slot.id)!)
      continue
    }
    flush()
    if (slot.kind === 'row') {
      const row = slot.row
      if (row.kind === 'plan' && plan) rows.push({ ...row, entries: plan.item.plan ?? [] })
      else if (row.kind === 'answered') {
        const latest = requests.get(row.request.id)!.request
        if (latest.status === 'pending') continue
        const call = latest.tool_call_id ? tools.get(latest.tool_call_id)?.call : undefined
        rows.push({ ...row, request: latest, command: call?.tool_kind === 'execute' ? call.title : undefined })
      } else rows.push(row)
      continue
    }
    const at = slot.first.time
    if (slot.role === 'user') {
      const attached = slot.images.length > 0 || slot.files.length > 0
      if (!attached && inboxWake.test(slot.text)) rows.push({ kind: 'event', id: slot.id, at, text: 'Woken by new agent messages' })
      else if (!attached && missionWake.test(slot.text)) rows.push({ kind: 'event', id: slot.id, at, text: 'Woken by a swarm update' })
      else rows.push({ kind: 'user', id: slot.id, at, body: slot.text, ...(slot.images.length ? { images: slot.images } : {}), ...(slot.files.length ? { files: slot.files } : {}) })
    } else if (slot.role === 'thought') {
      rows.push({ kind: 'thinking', id: slot.id, at, text: slot.text })
    } else if (slot.text.trim()) {
      rows.push({ kind: 'assistant', id: slot.id, at, text: slot.text, streaming: !slot.complete && !ended })
    }
  }
  flush()
  flushAgents()

  if (ended) {
    const [text, tone] = stopText[ended.stop_reason ?? ''] ?? [`Stopped: ${ended.stop_reason ?? 'unknown reason'}`, 'failed']
    const elapsed = started ? Date.parse(ended.time) - Date.parse(started.time) : 0
    const took = elapsed >= 1000 ? ` in ${seconds(elapsed)}` : ''
    rows.push({ kind: 'finished', id: `end:${turn.turn}`, at: ended.time, text: `${text}${tone === 'failed' ? '' : took}`, tone })
  }
  return rows
}

const frozen = new WeakMap<Turn, SessionRow[]>()

export function rowsOfTurn(turn: Turn): SessionRow[] {
  if (!turn.closed) return turnRows(turn)
  let rows = frozen.get(turn)
  if (!rows) {
    rows = turnRows(turn)
    frozen.set(turn, rows)
  }
  return rows
}

export interface LiveActivity {
  label: string
  waiting: boolean
}

export function liveActivity(turn: Turn | undefined): LiveActivity | null {
  if (!turn || turn.closed || !turn.items.some((item) => item.kind === 'turn_start')) return null
  const running = new Map<string, SessionToolCall>()
  const asking = new Map<string, SessionRequest>()
  let last: SessionItem | undefined
  for (const item of turn.items) {
    if (item.kind === 'tool_call' && item.tool_call) {
      const call = item.tool_call
      if (toolStatus(call) === 'running') running.set(call.id, call)
      else running.delete(call.id)
    }
    if (item.kind === 'request' && item.request) {
      if (item.request.status === 'pending') asking.set(item.request.id, item.request)
      else asking.delete(item.request.id)
    }
    if (item.kind !== 'usage' && item.kind !== 'commands' && item.kind !== 'config_options') last = item
  }
  const request = [...asking.values()].at(-1)
  if (request?.kind === 'permission') {
    const call = request.tool_call_id ? running.get(request.tool_call_id) : undefined
    return { label: `Waiting for your approval: ${call ? toolTarget(call) : request.title}`, waiting: true }
  }
  if (request) return { label: 'Waiting for your answer', waiting: true }
  const current = [...running.values()].filter((call) => !isLaunch(call)).at(-1)
  if (current) return { label: toolLabel(current, 'running'), waiting: false }
  const agents = [...running.values()].length
  if (agents) return { label: `Waiting on ${agents} ${agents === 1 ? 'subagent' : 'subagents'}`, waiting: false }
  if (last?.kind === 'thought' && !last.message?.complete) return { label: 'Thinking', waiting: false }
  if (last?.kind === 'message' && last.message?.role === 'assistant' && !last.message.complete) return null
  return { label: 'Working', waiting: false }
}

function turnIndex(turns: Turn[], turn: number): number {
  let low = 0
  let high = turns.length - 1
  while (low <= high) {
    const mid = (low + high) >> 1
    const held = turns[mid]!.turn
    if (held === turn) return mid
    if (held < turn) low = mid + 1
    else high = mid - 1
  }
  return -1
}

/** `home` names the earlier turn a snapshot of an older tool call belongs to. */
export function appendItems(turns: Turn[], items: SessionItem[], home?: (item: SessionItem) => number | undefined): Turn[] {
  if (items.length === 0) return turns
  const next = turns.slice()
  let open = next.at(-1)
  let copied = false
  const homeCopied = new Set<number>()
  for (const item of items) {
    const homeTurn = home?.(item)
    const at = homeTurn !== undefined && homeTurn < item.turn ? turnIndex(next, homeTurn) : -1
    if (at !== -1) {
      if (at === next.length - 1) {
        if (!copied) {
          open = { ...open!, items: open!.items.slice() }
          next[at] = open
          copied = true
        }
        open!.items.push(item)
      } else {
        if (!homeCopied.has(at)) {
          next[at] = { ...next[at]!, items: next[at]!.items.slice() }
          homeCopied.add(at)
        }
        next[at]!.items.push(item)
      }
      continue
    }
    if (!open || item.turn !== open.turn) {
      if (open && !open.closed) {
        next[next.length - 1] = { ...open, closed: true }
      }
      open = { turn: item.turn, items: [], closed: false }
      next.push(open)
      copied = true
    } else if (!copied) {
      open = { ...open, items: open.items.slice() }
      next[next.length - 1] = open
      copied = true
    }
    open.items.push(item)
    if (item.kind === 'turn_end') open.closed = true
  }
  return next
}

/** A held turn can predate the page: a late snapshot keeps its call's
 * first turn after a trim. Such turns are joined by number. */
export function prependItems(turns: Turn[], older: SessionItem[], home?: (item: SessionItem) => number | undefined): Turn[] {
  if (older.length === 0) return turns
  const paged = new Set(older.map((item) => item.turn))
  const away = new Map<number, SessionItem[]>()
  const page = older.filter((item) => {
    const to = home?.(item)
    if (to === undefined || to >= item.turn || paged.has(to) || turnIndex(turns, to) === -1) return true
    away.set(to, [...(away.get(to) ?? []), item])
    return false
  })
  if (away.size) {
    turns = turns.map((turn) => (away.has(turn.turn) ? { ...turn, items: [...away.get(turn.turn)!, ...turn.items] } : turn))
  }
  if (page.length === 0) return turns
  const head = appendItems([], page, home)
  const out: Turn[] = []
  let at = 0
  for (const page of head) {
    while (at < turns.length && turns[at]!.turn < page.turn) out.push(turns[at++]!)
    const held = turns[at]
    if (held?.turn === page.turn) {
      out.push({ ...held, items: [...page.items, ...held.items] })
      at++
    } else out.push({ ...page, closed: true })
  }
  return [...out, ...turns.slice(at)]
}

/** Drops items below floor, lowered so a user's image metadata stays with
 * any of its retained message segments. Items are compared by seq, not
 * position, because a late tool call snapshot joins an earlier turn. */
export function trimTurns(turns: Turn[], floor: number): { turns: Turn[]; floor: number } {
  const messages = new Set<string>()
  for (const turn of turns) {
    for (const item of turn.items) if (item.seq >= floor && item.message?.role === 'user') messages.add(item.message.message_id)
  }
  for (const turn of turns) {
    for (const item of turn.items) if (item.seq < floor && item.message && messages.has(item.message.message_id)) floor = item.seq
  }
  const kept = turns.flatMap((turn) => {
    if (turn.items.every((item) => item.seq >= floor)) return [turn]
    const items = turn.items.filter((item) => item.seq >= floor)
    return items.length ? [{ ...turn, items }] : []
  })
  return { turns: kept, floor }
}

export function replaceItem(turns: Turn[], whole: SessionItem): Turn[] {
  return turns.map((turn) => {
    const index = turn.items.findIndex((item) => item.seq === whole.seq)
    if (index === -1) return turn
    const items = turn.items.slice()
    items[index] = whole
    return { ...turn, items }
  })
}

export interface Rehome {
  id: string
  from: number
  to: number
}

export function rehome(turns: Turn[], moves: Rehome[]): Turn[] {
  const byFrom = new Map<number, Map<string, number>>()
  for (const { id, from, to } of moves) {
    if (turnIndex(turns, from) === -1 || turnIndex(turns, to) === -1) continue
    const ids = byFrom.get(from) ?? new Map<string, number>()
    ids.set(id, to)
    byFrom.set(from, ids)
  }
  let next = turns
  for (const [from, ids] of byFrom) {
    const source = turnIndex(next, from)
    const stay: SessionItem[] = []
    const moved = new Map<number, SessionItem[]>()
    for (const item of next[source]!.items) {
      const to = item.kind === 'tool_call' ? ids.get(item.tool_call?.id ?? '') : undefined
      if (to === undefined) stay.push(item)
      else moved.set(to, [...(moved.get(to) ?? []), item])
    }
    if (moved.size === 0) continue
    if (next === turns) next = turns.slice()
    next[source] = { ...next[source]!, items: stay }
    for (const [to, list] of moved) {
      const target = turnIndex(next, to)
      next[target] = { ...next[target]!, items: [...next[target]!.items, ...list] }
    }
  }
  return next
}

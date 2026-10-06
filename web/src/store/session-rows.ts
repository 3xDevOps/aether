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

export interface ChangedFile {
  path: string
  additions: number
  deletions: number
}

export type SessionRow =
  | {
      kind: 'user'
      id: string
      at: string
      body: string
      authorID?: string
      delivery?: Delivery
      deliverAfter?: string
      failure?: string
    }
  | { kind: 'note'; id: string; at: string; authorID: string; body: string; question?: boolean }
  | { kind: 'assistant'; id: string; at: string; text: string; streaming: boolean }
  | { kind: 'thinking'; id: string; at: string; text: string }
  | { kind: 'work'; id: string; at: string; summary: string; entries: WorkItem[] }
  | { kind: 'live'; id: string; at: string; label: string }
  | { kind: 'plan'; id: string; at: string; entries: { content: string; status?: string }[] }
  | { kind: 'changed-files'; id: string; at: string; files: ChangedFile[] }
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
  | { kind: 'event'; id: string; at: string; text: string; detail?: string; tone?: Tone }
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

function toolStatus(call: SessionToolCall): WorkStatus {
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

function workItem(track: ToolTrack, ended: boolean): WorkItem {
  const { call } = track
  const reported = toolStatus(call)
  const status = ended && reported === 'running' ? 'done' : reported
  return {
    id: call.id,
    tool: call.tool_kind ?? 'other',
    label: toolLabel(call, status),
    status,
    at: track.first.time,
    durationMs: status === 'running' ? undefined : Date.parse(track.last.time) - Date.parse(track.first.time),
    seq: track.last.seq,
    truncated: track.truncated,
    command: call.tool_kind === 'execute' ? call.title : undefined,
    output: track.output || undefined,
    exitCode: call.exit_code,
    diffs: call.diffs?.length ? call.diffs : undefined,
  }
}

function patchCounts(patch: string): { additions: number; deletions: number } {
  let additions = 0
  let deletions = 0
  for (const line of patch.split('\n')) {
    if (line.startsWith('+') && !line.startsWith('+++')) additions++
    else if (line.startsWith('-') && !line.startsWith('---')) deletions++
  }
  return { additions, deletions }
}

const stopText: Record<string, [string, Tone]> = {
  end_turn: ['Finished', 'done'],
  cancelled: ['Interrupted', 'paused'],
  max_tokens: ['Stopped: the reply reached its token limit', 'failed'],
  max_turn_requests: ['Stopped: too many model requests in one turn', 'failed'],
  refusal: ['Stopped: the agent refused', 'failed'],
}

function seconds(ms: number): string {
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`
}

type Slot =
  | { kind: 'row'; row: SessionRow }
  | { kind: 'message'; id: string; role: string; first: SessionItem; text: string; complete: boolean }
  | { kind: 'tool'; id: string }

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
        const key = `${item.kind}:${m.message_id}`
        const open = messages.get(key)
        if (open) {
          open.text += m.text
          open.complete ||= Boolean(m.complete)
          break
        }
        const slot = { kind: 'message' as const, id: key, role: item.kind === 'thought' ? 'thought' : m.role, first: item, text: m.text, complete: Boolean(m.complete) }
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
        slots.push({ kind: 'tool', id: call.id })
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
          slots.push({ kind: 'row', row: { kind: 'answered', id: `req:${request.id}`, at: item.time, request } })
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
    const entries = work.map((track) => workItem(track, turn.closed || ended !== null))
    rows.push({ kind: 'work', id: `work:${work[0]!.call.id}`, at: work[0]!.first.time, summary: workSummary(entries.map((e) => e.tool)), entries })
    work = []
  }
  for (const slot of slots) {
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
      if (inboxWake.test(slot.text)) rows.push({ kind: 'event', id: slot.id, at, text: 'Woken by new agent messages' })
      else if (missionWake.test(slot.text)) rows.push({ kind: 'event', id: slot.id, at, text: 'Woken by a swarm update' })
      else rows.push({ kind: 'user', id: slot.id, at, body: slot.text })
    } else if (slot.role === 'thought') {
      rows.push({ kind: 'thinking', id: slot.id, at, text: slot.text })
    } else if (slot.text.trim()) {
      rows.push({ kind: 'assistant', id: slot.id, at, text: slot.text, streaming: !slot.complete && !ended })
    }
  }
  flush()

  const files = new Map<string, ChangedFile>()
  for (const { call } of tools.values()) {
    for (const diff of call.diffs ?? []) {
      const counted = patchCounts(diff.patch)
      const prior = files.get(diff.path)
      files.set(diff.path, {
        path: diff.path,
        additions: (prior?.additions ?? 0) + counted.additions,
        deletions: (prior?.deletions ?? 0) + counted.deletions,
      })
    }
  }
  const end = ended?.time ?? turn.items.at(-1)?.time ?? ''
  if (files.size > 0) rows.push({ kind: 'changed-files', id: `files:${turn.turn}`, at: end, files: [...files.values()] })
  if (ended) {
    const [text, tone] = stopText[ended.stop_reason ?? ''] ?? [`Stopped: ${ended.stop_reason ?? 'unknown reason'}`, 'failed']
    const elapsed = started ? Date.parse(ended.time) - Date.parse(started.time) : 0
    const took = elapsed >= 1000 ? ` in ${seconds(elapsed)}` : ''
    rows.push({ kind: 'finished', id: `end:${turn.turn}`, at: ended.time, text: `${text}${tone === 'failed' ? '' : took}`, tone })
  }
  return rows
}

const frozen = new WeakMap<Turn, SessionRow[]>()

/** A closed turn derives once; only the open turn re-derives on each frame. */
export function rowsOfTurn(turn: Turn): SessionRow[] {
  if (!turn.closed) return turnRows(turn)
  let rows = frozen.get(turn)
  if (!rows) {
    rows = turnRows(turn)
    frozen.set(turn, rows)
  }
  return rows
}

export function liveLabel(turn: Turn | undefined): string | null {
  if (!turn || turn.closed || !turn.items.some((item) => item.kind === 'turn_start')) return null
  const running = new Map<string, SessionToolCall>()
  let last: SessionItem | undefined
  for (const item of turn.items) {
    if (item.kind === 'tool_call' && item.tool_call) {
      const call = item.tool_call
      if (toolStatus(call) === 'running') running.set(call.id, call)
      else running.delete(call.id)
    }
    if (item.kind !== 'usage' && item.kind !== 'commands' && item.kind !== 'config_options') last = item
  }
  const current = [...running.values()].at(-1)
  if (current) return toolLabel(current, 'running')
  if (last?.kind === 'thought' && !last.message?.complete) return 'Thinking'
  if (last?.kind === 'message' && last.message?.role === 'assistant' && !last.message.complete) return null
  return 'Working'
}

export function appendItems(turns: Turn[], items: SessionItem[]): Turn[] {
  if (items.length === 0) return turns
  const next = turns.slice()
  let open = next.at(-1)
  let copied = false
  for (const item of items) {
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

export function prependItems(turns: Turn[], older: SessionItem[]): Turn[] {
  if (older.length === 0) return turns
  const head = appendItems([], older)
  const first = turns[0]
  const tail = head.at(-1)!
  if (first && tail.turn === first.turn) {
    return [...head.slice(0, -1), { ...first, items: [...tail.items, ...first.items] }, ...turns.slice(1)]
  }
  return [...head.slice(0, -1), { ...tail, closed: true }, ...turns]
}

export function trimTurns(turns: Turn[], keep: number): Turn[] {
  let count = 0
  for (let i = turns.length - 1; i >= 0; i--) {
    count += turns[i]!.items.length
    if (count >= keep) {
      const turn = turns[i]!
      const cut = count - keep
      return cut > 0 ? [{ ...turn, items: turn.items.slice(cut) }, ...turns.slice(i + 1)] : turns.slice(i)
    }
  }
  return turns
}

export function itemCount(turns: Turn[]): number {
  return turns.reduce((n, turn) => n + turn.items.length, 0)
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

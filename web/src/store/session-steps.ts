import type { Tone } from '@/components/ui/status-dot'
import type { SessionRequest } from '@/lib/session-types'
import type { SessionRow } from '@/store/session-rows'

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
  /** The file, query or address a lookup chip names, or what an MCP tool acted on. */
  target?: string
  /** An MCP or other tool's own name, which a built-in tool kind does not need. */
  name?: string
  command?: string
  output?: string
  exitCode?: number
  diffs?: { path: string; patch: string }[]
  input?: unknown
  result?: unknown
  /** Its permission request is pending. */
  waiting?: boolean
}

export type PlanEntry = { content: string; status?: string }

export type Step =
  | { kind: 'thought'; id: string; at: string; text: string; streaming: boolean }
  | { kind: 'narration'; id: string; at: string; text: string; streaming: boolean }
  | { kind: 'tool'; id: string; at: string; entry: WorkItem }
  | { kind: 'lookup'; id: string; at: string; entries: WorkItem[] }
  | { kind: 'plan'; id: string; at: string; entries: PlanEntry[] }
  | { kind: 'subagents'; id: string; at: string; ids: string[] }
  | { kind: 'answered'; id: string; at: string; request: SessionRequest; command?: string }
  | { kind: 'event'; id: string; at: string; text: string; detail?: string; tone?: Tone }

export type StepsRow = Extract<SessionRow, { kind: 'steps' }>

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

const lookupKinds = new Set(['read', 'search', 'fetch'])

/** Steps that make a segment worth a block; prose and notices alone stay in the conversation. */
export const workKinds = new Set<Step['kind']>(['thought', 'tool', 'lookup', 'plan', 'subagents', 'answered'])

function withLookups(steps: Step[]): Step[] {
  const out: Step[] = []
  for (const step of steps) {
    if (step.kind !== 'tool' || !lookupKinds.has(step.entry.tool) || step.entry.target === undefined) {
      out.push(step)
      continue
    }
    const last = out.at(-1)
    if (last?.kind === 'lookup') out[out.length - 1] = { ...last, entries: [...last.entries, step.entry] }
    else out.push({ kind: 'lookup', id: `look:${step.entry.id}`, at: step.at, entries: [step.entry] })
  }
  return out
}

const fallbackSummary: Partial<Record<Step['kind'], string>> = {
  thought: 'Thought',
  plan: 'Updated the plan',
  answered: 'Answered a request',
}

export function stepsBlock(id: string, steps: Step[], start: string, current = false): StepsRow {
  const grouped = withLookups(steps)
  const kinds: string[] = []
  const icons: string[] = []
  const agents: string[] = []
  let failed = 0
  let running = 0
  let plan: { done: number; total: number } | undefined
  const icon = (kind: string) => {
    if (!icons.includes(kind)) icons.push(kind)
  }
  const count = (entry: WorkItem) => {
    kinds.push(entry.tool)
    icon(entry.tool)
    if (entry.status === 'failed') failed++
    if (entry.status === 'running') running++
  }
  for (const step of grouped) {
    switch (step.kind) {
      case 'thought':
        icon('think')
        break
      case 'tool':
        count(step.entry)
        break
      case 'lookup':
        step.entries.forEach(count)
        break
      case 'subagents':
        kinds.push(...step.ids.map(() => 'subagent'))
        agents.push(...step.ids)
        icon('subagent')
        break
      case 'plan':
        plan = { done: step.entries.filter((entry) => entry.status === 'completed').length, total: step.entries.length }
        icon('plan')
        break
      case 'event':
        if (step.tone === 'failed') failed++
    }
  }
  const summary = workSummary(kinds) || (grouped.map((step) => fallbackSummary[step.kind]).find(Boolean) ?? 'Worked')
  return { kind: 'steps', id, at: grouped[0]!.at, start, steps: grouped, summary, icons: icons.slice(0, 3), failed, running, plan, agents, current }
}

export function asRow(step: Step): SessionRow {
  if (step.kind === 'narration') return { kind: 'assistant', id: step.id, at: step.at, text: step.text, streaming: step.streaming }
  if (step.kind === 'event') return { kind: 'event', id: step.id, at: step.at, text: step.text, detail: step.detail, tone: step.tone }
  throw new Error(`a ${step.kind} step belongs in a block`)
}

/** A boundary starts a segment: a person's message, a wake or a new agent session. */
export type Part = { kind: 'boundary'; row: SessionRow } | { kind: 'step'; step: Step }

/** Aether's end-of-turn report, run as a command or called as an MCP tool. */
const reportCall = /(?:^|[;&|]\s*)(?:\S*\/)?aether-internal report\b|(?:^|[_.])aether_report$/

function isReport(step: Step): boolean {
  return step.kind === 'tool' && reportCall.test(step.entry.name ?? step.entry.command ?? step.entry.label)
}

/** Moves each report before the prose just ahead of it, so a reply written before reporting still ends its segment. */
function reportsFirst(steps: Step[]): Step[] {
  const out: Step[] = []
  for (const step of steps) {
    let at = out.length
    if (isReport(step)) while (at > 0 && (out[at - 1]!.kind === 'narration' || out[at - 1]!.kind === 'event')) at--
    out.splice(at, 0, step)
  }
  return out
}

/**
 * Splits a turn into segments at each boundary. Within a segment the agent's
 * prose stays in the conversation, and the work between two pieces of prose
 * joins one block. A segment without work keeps its rows as they are.
 */
export function segmentTurn(turn: number, parts: Part[], options: { start?: string; open: boolean; outcome?: Extract<SessionRow, { kind: 'finished' }> }): SessionRow[] {
  const rows: SessionRow[] = []
  let boundary: SessionRow | undefined
  let steps: Step[] = []
  const flush = (last: boolean) => {
    let afterProse = false
    let run: Step[] = []
    const close = (final: boolean) => {
      let cut = -1
      run.forEach((step, index) => {
        if (workKinds.has(step.kind)) cut = index
      })
      if (cut !== -1) {
        const id = afterProse ? `steps:${turn}:${run[0]!.id}` : `steps:${turn}:${boundary?.id ?? 'start'}`
        const start = afterProse ? run[0]!.at : (boundary?.at ?? options.start ?? run[0]!.at)
        rows.push(stepsBlock(id, run.slice(0, cut + 1), start, final && last && options.open))
      }
      rows.push(...run.slice(cut + 1).map(asRow))
      run = []
    }
    for (const step of reportsFirst(steps)) {
      if (step.kind !== 'narration') {
        run.push(step)
        continue
      }
      close(false)
      rows.push(asRow(step))
      afterProse = true
    }
    close(true)
    steps = []
  }
  for (const part of parts) {
    if (part.kind === 'step') {
      steps.push(part.step)
      continue
    }
    flush(false)
    rows.push(part.row)
    boundary = part.row
  }
  flush(true)
  const { outcome } = options
  if (outcome) {
    const last = rows.at(-1)
    if (last?.kind === 'assistant' && outcome.tone === 'done') rows[rows.length - 1] = { ...last, outcome }
    else rows.push(outcome)
  }
  return rows
}


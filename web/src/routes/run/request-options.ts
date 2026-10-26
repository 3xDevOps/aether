import type { SessionOption, SessionRequest, SessionToolCall } from '@/lib/session-types'

export interface Headline {
  who: string
  verb: string
  rest: string
}

function basename(path: string): string {
  return path.split('/').filter(Boolean).at(-1) ?? path
}

export function callPaths(call: SessionToolCall): string[] {
  const paths = call.diffs?.length ? call.diffs.map((diff) => diff.path) : (call.locations ?? []).map((location) => location.path)
  return [...new Set(paths)]
}

function fileAction(verb: string, call: SessionToolCall): string {
  const paths = callPaths(call)
  return paths.length > 1 ? `${verb} ${paths.length} files` : paths[0] ? `${verb} ${basename(paths[0])}` : `${verb} files`
}

export function requestHeadline(request: SessionRequest, call: SessionToolCall | undefined, who: string): Headline {
  const asks = (rest: string) => ({ who, verb: 'asks', rest })
  const wants = (rest: string) => ({ who, verb: 'wants to', rest })
  if (request.kind === 'question') return asks('a question')
  if (request.kind === 'link') return asks('you to open a link')
  switch (call?.tool_kind) {
    case 'execute':
      return wants('run a command')
    case 'edit':
    case 'delete':
    case 'move':
    case 'read':
      return wants(fileAction(call.tool_kind, call))
    case 'fetch':
      return wants('fetch a page')
    case 'search':
      return wants('search the code')
    case 'switch_mode':
      return wants('change how it works')
  }
  return call?.tool_name ? wants(`use ${call.tool_name}`) : asks('for permission')
}

export function plainHeadline({ who, verb, rest }: Headline): string {
  return `${who} ${verb} ${rest}`
}

const optionRank = (option: SessionOption) => (option.kind === 'allow_once' ? 0 : option.kind === 'allow_always' ? 2 : 1)

export function orderedOptions(options: SessionOption[]): SessionOption[] {
  return [...options].sort((a, b) => optionRank(a) - optionRank(b))
}

function primaryOf(options: SessionOption[]): SessionOption | undefined {
  return options.find((o) => o.kind === 'allow_once') ?? options.find((o) => o.id === 'accept') ?? options[0]
}

export interface Choice {
  option: SessionOption
  digit?: number
}

export type ChoicePlan =
  | { kind: 'pills'; once: Choice; always: Choice; deny?: Choice; extras: Choice[] }
  | { kind: 'buttons'; choices: (Choice & { primary: boolean })[] }

const isReject = (option: SessionOption) => option.kind?.startsWith('reject') || option.id === 'decline'

/**
 * Splits a request's options into "How long" pills and buttons. Pills appear
 * only for a known tool call that is not a mode switch, with exactly one
 * allow_once and one allow_always among distinct, non-empty kinds: Claude
 * Code's plan exit labels allow_always "auto-accept edits", and other agents
 * send empty or repeated kinds, so anything else keeps the agent's own
 * buttons. Digit 1 never answers allow_always.
 */
export function choicePlan(request: SessionRequest, call: SessionToolCall | undefined): ChoicePlan {
  const options = orderedOptions(request.options ?? [])
  const choices: Choice[] = options.map((option, index) => ({
    option,
    digit: index < 4 && (index > 0 || option.kind !== 'allow_always') ? index + 1 : undefined,
  }))
  const kinds = options.map((o) => o.kind ?? '')
  const pills = request.kind === 'permission' && call !== undefined && call.tool_kind !== 'switch_mode'
    && kinds.every(Boolean) && new Set(kinds).size === kinds.length
    && kinds.includes('allow_once') && kinds.includes('allow_always')
  const byKind = (kind: string) => choices.find((c) => c.option.kind === kind)
  if (pills) {
    return {
      kind: 'pills',
      once: byKind('allow_once')!,
      always: byKind('allow_always')!,
      deny: byKind('reject_once'),
      extras: choices.filter((c) => c.option.kind === 'reject_always'),
    }
  }
  const primary = primaryOf(options)
  const place = (c: Choice) => (isReject(c.option) ? 0 : c.option === primary ? 3 : c.option.kind?.startsWith('allow') || c.option.id === 'accept' ? 2 : 1)
  return {
    kind: 'buttons',
    choices: [...choices].sort((a, b) => place(a) - place(b)).map((c) => ({ ...c, primary: c.option === primary })),
  }
}

export function answerWord(request: SessionRequest): string {
  if (request.status === 'cancelled') return 'Cancelled'
  if (request.kind !== 'permission') return request.answer === 'accept' ? 'Answered' : 'Declined'
  const option = request.options?.find((o) => o.id === request.answer)
  switch (option?.kind) {
    case 'allow_once':
      return 'Allowed once'
    case 'allow_always':
      return 'Always allowed'
    case 'reject_once':
      return 'Denied'
    case 'reject_always':
      return 'Always denied'
  }
  return option ? `Answered “${option.name}”` : 'Answered'
}

export function answerTone(request: SessionRequest): 'done' | 'failed' | 'neutral' {
  if (request.status === 'cancelled') return 'neutral'
  if (request.kind !== 'permission') return request.answer === 'accept' ? 'done' : 'neutral'
  const kind = request.options?.find((o) => o.id === request.answer)?.kind ?? ''
  return kind.startsWith('allow') ? 'done' : kind.startsWith('reject') ? 'failed' : 'neutral'
}

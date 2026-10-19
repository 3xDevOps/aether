import type { MemoryItem, MemoryKind, MemorySearchParams, MemorySource } from '@/lib/types'

/** protocol.MemoryBriefMaxBytes */
export const briefMaxBytes = 3 << 10
/** protocol.MemorySearchMaxLimit */
export const listLimit = 100

export type Filter = 'all' | 'pinned' | 'agent' | 'outcomes' | 'answers' | 'superseded'

export const filters: { value: Filter; label: string; empty: string }[] = [
  { value: 'all', label: 'All', empty: 'No notes' },
  { value: 'pinned', label: 'Pinned', empty: 'No pinned notes' },
  { value: 'agent', label: 'Agent notes', empty: 'No agent notes' },
  { value: 'outcomes', label: 'Outcomes', empty: 'No run outcomes' },
  { value: 'answers', label: 'Answers', empty: 'No swarm answers' },
  { value: 'superseded', label: 'Superseded', empty: 'No superseded notes' },
]

export const kindLabel: Record<MemoryKind, string> = {
  decision: 'Decision',
  convention: 'Convention',
  gotcha: 'Gotcha',
  preference: 'Preference',
  outcome: 'Outcome',
}

export const writableKinds = ['decision', 'convention', 'gotcha', 'preference'] as const

export const sourceLabel: Record<MemorySource, string> = {
  agent: 'Agent note',
  member: 'Member note',
  report: 'Run report',
  answer: 'Swarm answer',
}

export function superseded(item: MemoryItem): boolean {
  return Boolean(item.superseded_by || item.invalid_at)
}

const narrows: Record<Filter, Partial<MemorySearchParams>> = {
  all: {},
  pinned: { pinned: true },
  agent: { source: 'agent' },
  outcomes: { kind: 'outcome' },
  answers: { source: 'answer' },
  superseded: { replaced: true },
}

/** The reads behind a filter, which memory.search applies: every pinned
 * match, then the newest or best matches. Pinned needs only the first, and
 * Superseded only the second, since a replaced note is never pinned. */
export function reads(workspaceID: string, words: string, filter: Filter): MemorySearchParams[] {
  const read: MemorySearchParams = { workspace_id: workspaceID, limit: listLimit, ...(words ? { words } : {}), ...narrows[filter] }
  return filter === 'pinned' || filter === 'superseded' ? [read] : [{ ...read, pinned: true }, read]
}

/** Each note once, pinned first; each group keeps the server's order, best match or newest. */
export function listed(items: MemoryItem[]): MemoryItem[] {
  const seen = new Set<string>()
  const kept = items.filter((item) => !seen.has(item.id) && seen.add(item.id))
  return [...kept.filter((item) => item.pinned_at), ...kept.filter((item) => !item.pinned_at)]
}

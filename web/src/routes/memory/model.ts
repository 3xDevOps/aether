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

/** The reads behind a filter: every pinned note, then the newest or best
 * of all, last. memory.search filters by kind, liveness and pin, so Agent
 * notes and Answers narrow what it returns. A replaced note is never
 * pinned, so Superseded skips the pinned read. */
export function reads(workspaceID: string, words: string, filter: Filter): MemorySearchParams[] {
  const all: MemorySearchParams = {
    workspace_id: workspaceID,
    limit: listLimit,
    ...(words ? { words } : {}),
    ...(filter === 'outcomes' ? { kind: 'outcome' as const } : {}),
    ...(filter === 'superseded' ? { all: true } : {}),
  }
  const pinned = { ...all, pinned: true }
  if (filter === 'pinned') return [pinned]
  return filter === 'superseded' ? [all] : [pinned, all]
}

const keeps: Record<Filter, (item: MemoryItem) => boolean> = {
  all: () => true,
  pinned: (item) => Boolean(item.pinned_at),
  agent: (item) => item.source === 'agent',
  outcomes: (item) => item.kind === 'outcome',
  answers: (item) => item.source === 'answer',
  superseded,
}

/** Each note once, pinned first; each group keeps the server's order, best match or newest. */
export function listed(items: MemoryItem[], filter: Filter): MemoryItem[] {
  const seen = new Set<string>()
  const kept = items.filter((item) => !seen.has(item.id) && seen.add(item.id) && keeps[filter](item))
  return [...kept.filter((item) => item.pinned_at), ...kept.filter((item) => !item.pinned_at)]
}

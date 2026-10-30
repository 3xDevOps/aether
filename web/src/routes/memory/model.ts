import type { Member, MemoryItem, MemoryKind, MemorySearchParams, MemorySource } from '@/lib/types'

/** protocol.MemoryBriefMaxBytes */
export const briefMaxBytes = 3 << 10
/** protocol.MemoryMaxBodyBytes */
export const noteMaxBytes = 1 << 10
/** protocol.MemorySearchMaxLimit */
export const listLimit = 100

export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

export type Filter = 'all' | 'pinned' | 'agent' | 'outcomes' | 'answers' | 'superseded'

export const filters: { value: Filter; label: string; empty: string; search: string }[] = [
  { value: 'all', label: 'All', empty: 'No notes', search: 'Search notes…' },
  { value: 'pinned', label: 'Pinned', empty: 'No pinned notes', search: 'Search pinned notes…' },
  { value: 'agent', label: 'Agent notes', empty: 'No agent notes', search: 'Search agent notes…' },
  { value: 'outcomes', label: 'Outcomes', empty: 'No run outcomes', search: 'Search run outcomes…' },
  { value: 'answers', label: 'Answers', empty: 'No answers', search: 'Search answers…' },
  { value: 'superseded', label: 'Superseded', empty: 'No superseded notes', search: 'Search superseded notes…' },
]

export const kindLabel: Record<MemoryKind, string> = {
  decision: 'Decision',
  convention: 'Convention',
  gotcha: 'Gotcha',
  preference: 'Preference',
  outcome: 'Outcome',
}

type Self = { id: string | null; role: Member['role'] | null }

/** The author, followed through a member merge as the server does. */
function author(item: MemoryItem, mergedInto: Record<string, string>): string | undefined {
  return item.author_id && (mergedInto[item.author_id] ?? item.author_id)
}

/** Mirrors the server: a member note is its pinner's, or unpinned its author's; only they or an admin change it. */
export function mayChange(item: MemoryItem, self: Self, mergedInto: Record<string, string>): boolean {
  if (item.scope !== 'member' || self.role === null || self.role === 'admin') return true
  return (item.pinned_at ? item.pinned_by : author(item, mergedInto)) === self.id
}

/** A member pin makes the note the pinner's: a member note only its owner's, a workspace note only its author's or an admin's. */
export function mayPinForSelf(item: MemoryItem, self: Self, mergedInto: Record<string, string>): boolean {
  if (self.role === null) return true
  if (item.scope === 'member') return (item.pinned_at ? item.pinned_by : author(item, mergedInto)) === self.id
  return self.role === 'admin' || author(item, mergedInto) === self.id
}

export const writableKinds = ['decision', 'convention', 'gotcha', 'preference'] as const

export const sourceLabel: Record<MemorySource, string> = {
  agent: 'Agent note',
  member: 'Member note',
  report: 'Run report',
  answer: 'Answer',
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

/** One read pair's answer: the notes and whether either read stopped at the limit. */
export type Listing = { items: MemoryItem[]; pinsFull: boolean; full: boolean }

/** Tab counts from the unfiltered All pair. A count is shown only while it is
 * exact: a read that stopped at the limit makes it "100+", and once the
 * newest read is cut off the other filters reach past it, so they get none.
 * Superseded never has one, since the pair leaves replaced notes out. */
export function tabCounts({ items, pinsFull, full }: Listing, hidden: ReadonlySet<string>): Partial<Record<Filter, string>> {
  const live = listed(items).filter((item) => !hidden.has(item.id))
  const count = (keep: (item: MemoryItem) => boolean) => String(live.filter(keep).length)
  const counts: Partial<Record<Filter, string>> = {
    all: full ? `${listLimit}+` : String(live.length),
    pinned: pinsFull ? `${listLimit}+` : count((item) => Boolean(item.pinned_at)),
  }
  if (full) return counts
  return {
    ...counts,
    agent: count((item) => item.source === 'agent'),
    outcomes: count((item) => item.kind === 'outcome'),
    answers: count((item) => item.source === 'answer'),
  }
}

/** What a read pair that stopped at the limit holds; empty when neither did. */
export function capNotice({ pinsFull, full }: Listing, words: string): string {
  if (!pinsFull && !full) return ''
  const pins = pinsFull ? `The ${listLimit} ${words ? 'best pinned matches' : 'newest pinned notes'}` : `Every pinned ${words ? 'match' : 'note'}`
  const rest = full ? `, then the ${listLimit} ${words ? 'best matches' : 'newest notes'}` : ''
  return `${pins}${rest}.${words ? '' : ' Search to find older ones.'}`
}

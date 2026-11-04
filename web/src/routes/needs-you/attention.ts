import { useEffect } from 'react'
import { create } from 'zustand'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { needsYouConditions } from '@/lib/needs-you'
import { groupLabel } from '@/lib/status'
import type { AttentionItem } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunTree, SidebarGroup } from '@/store/selectors'

interface Attention {
  items: AttentionItem[] | null
  error?: string
}

export const useAttention = create<Attention>(() => ({ items: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useAttention.setState({ items: null, error: undefined })
})

const rank = (item: AttentionItem) => needsYouConditions.findIndex((condition) => condition.id === item.kind)

/** What asks something of the viewer, requests first and failures last; the server lists oldest first within a kind. */
export function asking(items: AttentionItem[]): AttentionItem[] {
  return items.filter((item) => item.kind !== 'ready').sort((a, b) => rank(a) - rank(b))
}

/** Finished: review the result. Passive, so it never counts toward the badge. */
export function finished(items: AttentionItem[]): AttentionItem[] {
  return items.filter((item) => item.kind === 'ready')
}

export function useAskingCount(): number {
  return useAttention((s) => (s.items ? asking(s.items).length : 0))
}

/** Per workspace, what asks the viewer; null until the list loads. */
export function askingByWorkspace(items: AttentionItem[] | null): Record<string, number> | undefined {
  if (!items) return undefined
  const counts: Record<string, number> = {}
  for (const item of asking(items)) counts[item.workspace_id] = (counts[item.workspace_id] ?? 0) + 1
  return counts
}

/**
 * The session list's groups in the server's terms, so a phone shows one Needs you count: requests
 * in the server's order under Needs you, and a session the server lists as finished under review.
 */
export function byAttention(groups: SidebarGroup[], items: AttentionItem[] | null): SidebarGroup[] {
  const needs = groups.find((group) => group.key === 'needs-you')
  if (!items || !needs) return groups
  const asks = asking(items)
  const order = new Map(asks.map((item, index) => [item.run_id, index]))
  const done = new Set(finished(items).map((item) => item.run_id))
  const at = (tree: RunTree) => Math.min(...[tree, ...tree.children].map((row) => order.get(row.run.id) ?? Number.MAX_SAFE_INTEGER))
  const moved = needs.runs.filter((tree) => done.has(tree.run.id) && !order.has(tree.run.id) && tree.children.every((row) => !order.has(row.run.id)))
  const kept = needs.runs.filter((tree) => !moved.includes(tree)).sort((a, b) => at(a) - at(b))
  const review = groups.find((group) => group.key === 'review')
  const reviewRuns = [...moved, ...(review?.runs ?? [])]
  return [
    ...(kept.length > 0 ? [{ ...needs, runs: kept, count: asks.length }] : []),
    ...(reviewRuns.length > 0 ? [{ key: 'review' as const, label: groupLabel.review, runs: reviewRuns, count: reviewRuns.length }] : []),
    ...groups.filter((group) => group.key !== 'needs-you' && group.key !== 'review'),
  ]
}

let fetches = 0

export async function refreshAttention() {
  const attempt = ++fetches
  const same = sameIdentity()
  const current = () => attempt === fetches && same()
  try {
    const { items } = await api.attentionList()
    if (current()) useAttention.setState({ items, error: undefined })
  } catch (err) {
    if (current()) useAttention.setState({ error: message(err) })
  }
}

// Every field the server's Needs you rows read: status_seq moves with status, state and reason;
// requests, outcome flags, room messages, review and control move apart from it. A reconnect
// retries a failed read.
const changeKey = (s: ReturnType<typeof useStore.getState>) => JSON.stringify([
  s.identityKey,
  s.connection,
  ...Object.values(s.runs).map((run) => [
    run.id,
    run.status_seq,
    run.state,
    run.review,
    run.outcome_unseen,
    run.unanswered_questions,
    run.controller_member_id,
    run.archived_at,
    run.pending_inputs?.map((input) => input.id),
    s.roomMessages[run.id]?.length,
    s.roomStatus[run.id]?.queued_steers,
  ]),
])

/** A gateway older than attention.list has no Needs you page; the phone lands on the board. */
export function useAttentionSupported(): boolean {
  return useCapability().hasMethod('attention.list')
}

/** Keeps the list in step with the event feed. */
export function useAttentionFeed() {
  const supported = useAttentionSupported()
  const key = useStore(changeKey)
  useEffect(() => {
    if (!supported) return
    const timer = setTimeout(() => void refreshAttention(), 250)
    return () => clearTimeout(timer)
  }, [key, supported])
}

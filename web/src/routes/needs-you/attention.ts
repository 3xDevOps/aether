import { useEffect } from 'react'
import { create } from 'zustand'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { needsYouConditions } from '@/lib/needs-you'
import type { AttentionItem } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'

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

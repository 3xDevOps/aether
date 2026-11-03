import { useEffect } from 'react'
import { create } from 'zustand'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { needsYouConditions } from '@/lib/needs-you'
import type { AttentionItem } from '@/lib/types'
import { useStore } from '@/store'

interface Attention {
  items: AttentionItem[] | null
  error?: string
}

export const useAttention = create<Attention>(() => ({ items: null }))

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
  try {
    const { items } = await api.attentionList()
    if (attempt === fetches) useAttention.setState({ items, error: undefined })
  } catch (err) {
    if (attempt === fetches) useAttention.setState({ error: message(err) })
  }
}

// Every field the server's Needs you rows read: status_seq moves with status, state, reason and the
// outcome flags; requests, room questions, review and control move apart from it.
const changeKey = (s: ReturnType<typeof useStore.getState>) => JSON.stringify([
  s.identityKey,
  ...Object.values(s.runs).map((run) => [
    run.id,
    run.status_seq,
    run.state,
    run.review,
    run.unanswered_questions,
    run.controller_member_id,
    run.archived_at,
    run.pending_inputs?.map((input) => input.id),
  ]),
])

/** Keeps the list in step with the event feed; mounted once, by the phone shell. */
export function useAttentionFeed() {
  const key = useStore(changeKey)
  useEffect(() => {
    const timer = setTimeout(() => void refreshAttention(), 250)
    return () => clearTimeout(timer)
  }, [key])
}

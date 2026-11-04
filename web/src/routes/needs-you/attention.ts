import { useEffect, useMemo, useRef } from 'react'
import { create } from 'zustand'
import { api, ApiError, codeMethodNotFound } from '@/lib/api'
import { message } from '@/lib/format'
import { needsYouConditions } from '@/lib/needs-you'
import { groupLabel } from '@/lib/status'
import type { AttentionItem } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { queuedSteers } from '@/store/collaboration'
import { useCapability, useDeviceNeeds } from '@/store/hooks'
import { byChange, type RunTree, type SidebarGroup } from '@/store/selectors'

interface Attention {
  items: AttentionItem[] | null
  error?: string
  /** The gateway answered method-not-found: a gateway that advertises every method may still lack this one. */
  unsupported?: boolean
}

export const useAttention = create<Attention>(() => ({ items: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useAttention.setState({ items: null, error: undefined, unsupported: false })
})

const rank = (item: AttentionItem) => {
  const index = needsYouConditions.findIndex((condition) => condition.id === item.kind)
  return index === -1 ? needsYouConditions.length : index
}

/** What asks something of the viewer, requests first and failures last; the server lists oldest first within a kind. */
export function asking(items: AttentionItem[]): AttentionItem[] {
  return items.filter((item) => item.kind !== 'ready').sort((a, b) => rank(a) - rank(b))
}

/** Finished: review the result. Passive, so it never counts toward the badge. */
export function finished(items: AttentionItem[]): AttentionItem[] {
  return items.filter((item) => item.kind === 'ready')
}

/** Devices waiting for approval and merge proposals to the viewer: not sessions, so not in attention.list. */
export function useAccountNeeds() {
  const devices = useDeviceNeeds()
  const proposals = useStore((s) => s.mergeProposals)
  const viewer = useStore((s) => s.info?.member.id)
  const merges = useMemo(() => proposals.filter((proposal) => proposal.minted_id === viewer), [proposals, viewer])
  return { devices, merges }
}

/** The badge: what asks the viewer in a session, plus devices and merges, as the session list counts them. */
export function useAskingCount(): number {
  const { devices, merges } = useAccountNeeds()
  return useAttention((s) => (s.items ? asking(s.items).length : 0)) + devices.length + merges.length
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
 * A session only this dashboard thinks needs the viewer stays listed, last, but is not counted.
 * `accountNeeds` (devices and merges) keeps the group and its count, as sidebarGroups does.
 */
export function byAttention(groups: SidebarGroup[], items: AttentionItem[] | null, accountNeeds: number): SidebarGroup[] {
  if (!items) return groups
  const asks = asking(items)
  const order = new Map(asks.map((item, index) => [item.run_id, index]))
  const asked = (tree: RunTree) => [tree, ...tree.children].some((row) => order.has(row.run.id))
  // A session the server lists as asking joins Needs you from whichever group the dashboard put it in.
  const pulled = groups.filter((group) => group.key !== 'needs-you').flatMap((group) => group.runs.filter(asked))
  const found = groups.find((group) => group.key === 'needs-you')
  if (!found && pulled.length === 0 && accountNeeds === 0) return groups
  const needs: SidebarGroup = { ...(found ?? { key: 'needs-you', label: groupLabel['needs-you'], count: 0 }), runs: [...(found?.runs ?? []), ...pulled] }
  const rest = groups
    .filter((group) => group.key !== 'needs-you' && group.key !== 'review')
    .map((group) => ({ ...group, runs: group.runs.filter((tree) => !asked(tree)) }))
    .map((group) => ({ ...group, count: group.runs.length }))
    .filter((group) => group.runs.length > 0)
  const done = new Set(finished(items).map((item) => item.run_id))
  const at = (tree: RunTree) => Math.min(...[tree, ...tree.children].map((row) => order.get(row.run.id) ?? Number.MAX_SAFE_INTEGER))
  // A family moves when nothing in it asks and some member, the root or a subsession, is finished.
  const moved = needs.runs.filter((tree) => {
    const rows = [tree, ...tree.children]
    return rows.every((row) => !order.has(row.run.id)) && rows.some((row) => done.has(row.run.id))
  })
  const kept = needs.runs.filter((tree) => !moved.includes(tree)).sort((a, b) => at(a) - at(b))
  const review = groups.find((group) => group.key === 'review')
  const reviewRuns = [...moved, ...(review?.runs.filter((tree) => !asked(tree)) ?? [])].sort(byChange)
  return [
    ...(kept.length > 0 || accountNeeds > 0 ? [{ ...needs, runs: kept, count: asks.length + accountNeeds }] : []),
    ...(reviewRuns.length > 0 ? [{ key: 'review' as const, label: groupLabel.review, runs: reviewRuns, count: reviewRuns.length }] : []),
    ...rest,
  ]
}

let fetches = 0
let inFlight: Promise<void> | null = null
let again = false

async function read() {
  const attempt = ++fetches
  const same = sameIdentity()
  const current = () => attempt === fetches && same()
  try {
    const { items } = await api.attentionList()
    if (current()) useAttention.setState({ items, error: undefined })
  } catch (err) {
    if (!current()) return
    if (err instanceof ApiError && err.code === codeMethodNotFound) useAttention.setState({ unsupported: true })
    else useAttention.setState({ error: message(err) })
  }
}

/** One read at a time; a call during a read asks for one more after it. */
export function refreshAttention(): Promise<void> {
  if (inFlight) {
    again = true
    return inFlight
  }
  inFlight = read().finally(() => {
    inFlight = null
    if (again) {
      again = false
      void refreshAttention()
    }
  })
  return inFlight
}

// Every field the server's Needs you rows read: status_seq moves with status, state and reason;
// requests, outcome flags, pause, owner, room messages (opened here or not) and their decisions,
// review and control move apart from it. A reconnect retries a failed read.
const changeKey = (s: ReturnType<typeof useStore.getState>) => {
  let key = `${s.identityKey}|${s.connection}|${s.roomEvents}`
  for (const run of Object.values(s.runs)) {
    const messages = s.roomMessages[run.id]
    key += `|${run.id}:${run.member_id}:${run.status_seq}:${run.state}:${run.review}:${run.outcome_unseen}:${run.unanswered_questions}:${run.controller_member_id}:${run.archived_at}:${s.pausedRuns[run.id] ?? run.paused}`
    key += `:${run.pending_inputs?.map((input) => input.id).join(',')}:${messages && queuedSteers(messages).length}:${messages?.length}:${s.roomStatus[run.id]?.queued_steers}`
  }
  return key
}

/** A gateway older than attention.list has no Needs you page; the phone lands on the board. */
export function useAttentionSupported(): boolean {
  const unsupported = useAttention((s) => s.unsupported === true)
  return useCapability().hasMethod('attention.list') && !unsupported
}

/** Keeps the list in step with the event feed. */
export function useAttentionFeed() {
  const supported = useAttentionSupported()
  const key = useStore(changeKey)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // A throttle, not a debounce: a busy event feed still reads at least every 250ms.
  useEffect(() => {
    if (!supported || timer.current) return
    timer.current = setTimeout(() => {
      timer.current = null
      void refreshAttention()
    }, 250)
  }, [key, supported])
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
    timer.current = null
  }, [])
}

import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { RunMessage } from '@/lib/types'
import type { RootStore } from '@/store'
import { byCreated } from '@/store/collaboration'
import type { SliceCreator } from '@/store/slice'

export type MessageScope =
  | { kind: 'workspace'; workspaceID: string }
  | { kind: 'mission'; workspaceID: string; missionID: string }
  | { kind: 'run'; workspaceID: string; runID: string }

export function messageScopeKey(scope: MessageScope): string {
  switch (scope.kind) {
    case 'workspace':
      return `workspace:${scope.workspaceID}`
    case 'mission':
      return `mission:${scope.missionID}`
    case 'run':
      return `run:${scope.runID}`
  }
}

type Participants = Pick<RunMessage, 'workspace_id' | 'mission_id' | 'from_run_id' | 'to_run_id'>

/** A run scope covers mail on either side of the run. */
export function inMessageScope(scope: MessageScope, m: Participants): boolean {
  if (m.workspace_id !== scope.workspaceID) return false
  switch (scope.kind) {
    case 'workspace':
      return true
    case 'mission':
      return m.mission_id === scope.missionID
    case 'run':
      return m.from_run_id === scope.runID || m.to_run_id === scope.runID
  }
}

export interface MessageList {
  scope: MessageScope
  /** Oldest first. */
  ids: string[]
  /** Cursor for the next older page; null once history is exhausted. */
  nextBefore: string | null
}

export interface MessagesSlice {
  runMessages: Record<string, RunMessage>
  messageLists: Record<string, MessageList>
  messageErrors: Record<string, string | undefined>
  setMessagePage: (scope: MessageScope, messages: RunMessage[], nextBefore: string | undefined, older?: boolean) => void
  applyMessageAcked: (messageID: string, ackedAt: string) => void
  setMessageError: (scope: MessageScope, error?: string) => void
}

/** A list read can be older than an ack event already applied. */
function mergeMessage(prior: RunMessage | undefined, incoming: RunMessage): RunMessage {
  if (!prior) return incoming
  return {
    ...incoming,
    delivered_at: incoming.delivered_at ?? prior.delivered_at,
    acked_at: incoming.acked_at ?? prior.acked_at,
  }
}

export const createMessagesSlice: SliceCreator<MessagesSlice> = (set) => ({
  runMessages: {},
  messageLists: {},
  messageErrors: {},
  setMessagePage: (scope, messages, nextBefore, older = false) =>
    set((state) => {
      const key = messageScopeKey(scope)
      const runMessages = { ...state.runMessages }
      for (const m of messages) runMessages[m.id] = mergeMessage(runMessages[m.id], m)
      const held = state.messageLists[key]
      // A newest page that has more history behind it and shares no message
      // with what is held may have skipped messages in between.
      const gap = !older && nextBefore !== undefined && !messages.some((m) => held?.ids.includes(m.id))
      const keep = held && !gap ? held.ids : []
      const ids = byCreated([...new Set([...keep, ...messages.map((m) => m.id)])].map((id) => runMessages[id])).map(
        (m) => m.id,
      )
      const cursor = older || !held || gap || nextBefore === undefined ? (nextBefore ?? null) : held.nextBefore
      return {
        runMessages,
        messageLists: { ...state.messageLists, [key]: { scope, ids, nextBefore: cursor } },
      }
    }),
  applyMessageAcked: (messageID, ackedAt) =>
    set((state) => {
      const prior = state.runMessages[messageID]
      if (!prior || prior.acked_at === ackedAt) return {}
      return { runMessages: { ...state.runMessages, [messageID]: { ...prior, acked_at: ackedAt } } }
    }),
  setMessageError: (scope, error) =>
    set((state) => ({ messageErrors: { ...state.messageErrors, [messageScopeKey(scope)]: error } })),
})

export function scopeMessages(state: MessagesSlice, scope: MessageScope): RunMessage[] {
  const list = state.messageLists[messageScopeKey(scope)]
  return list ? list.ids.map((id) => state.runMessages[id]) : []
}

export async function loadMessagePage(store: RootStore, client: Api, scope: MessageScope, older = false): Promise<void> {
  const before = store.getState().messageLists[messageScopeKey(scope)]?.nextBefore
  if (older && !before) return
  try {
    const page = await client.coordMessagesList({
      workspace_id: scope.workspaceID,
      mission_id: scope.kind === 'mission' ? scope.missionID : undefined,
      run_id: scope.kind === 'run' ? scope.runID : undefined,
      before: older ? (before ?? undefined) : undefined,
    })
    store.getState().setMessagePage(scope, page.messages ?? [], page.next_before, older)
    store.getState().setMessageError(scope)
  } catch (err) {
    store.getState().setMessageError(scope, message(err))
  }
}

export type MessageGroup =
  | { kind: 'single'; message: RunMessage }
  | { kind: 'thread'; question: RunMessage; replies: RunMessage[] }
  | { kind: 'run'; from: string; to: string; messages: RunMessage[] }

/** Ordered by each group's latest message, oldest first. A reply joins its
 * question when that question is loaded; adjacent plain messages between one
 * pair collapse into a run. */
export function groupMessages(messages: RunMessage[]): MessageGroup[] {
  const threads = new Map<string, RunMessage[]>()
  for (const m of messages) if (m.kind === 'question') threads.set(m.correlation_id || m.id, [])
  const groups: MessageGroup[] = []
  for (const m of messages) {
    const replies = m.correlation_id ? threads.get(m.correlation_id) : undefined
    const last = groups.at(-1)
    if (m.kind === 'reply' && replies) replies.push(m)
    else if (m.kind === 'question') groups.push({ kind: 'thread', question: m, replies: threads.get(m.correlation_id || m.id)! })
    else if (m.kind !== 'message') groups.push({ kind: 'single', message: m })
    else if (last?.kind === 'run' && last.from === m.from_run_id && last.to === m.to_run_id) last.messages.push(m)
    else groups.push({ kind: 'run', from: m.from_run_id, to: m.to_run_id, messages: [m] })
  }
  return groups
    .map((g): MessageGroup => (g.kind === 'run' && g.messages.length === 1 ? { kind: 'single', message: g.messages[0] } : g))
    .sort((a, b) => latestAt(a).localeCompare(latestAt(b)))
}

function latestAt(group: MessageGroup): string {
  switch (group.kind) {
    case 'single':
      return group.message.created_at
    case 'thread':
      return (group.replies.at(-1) ?? group.question).created_at
    case 'run':
      return group.messages.at(-1)!.created_at
  }
}

export function deliveryWord(m: Pick<RunMessage, 'delivered_at' | 'acked_at'>): 'Acknowledged' | 'Delivered' | 'Sent' {
  if (m.acked_at) return 'Acknowledged'
  return m.delivered_at ? 'Delivered' : 'Sent'
}

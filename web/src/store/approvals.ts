import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Approval, ApprovalDecision, Event } from '@/lib/types'
import type { RootStore } from '@/store'
import { coalesce, readRetryDelay } from '@/store/coalesce'
import type { SliceCreator } from '@/store/slice'

export interface ApprovalsSlice {
  /** Workspace ID to that workspace's inbox, as the last fetch saw it. */
  inbox: Record<string, Approval[]>
  /** The inbox view also lists already-decided requests when set. */
  showDecided: boolean
  /** Run ID to its pending requests, oldest first; rebuilt whenever the inbox changes. */
  approvalsByRun: Record<string, Approval[]>
  /** A read failure, so an unreadable queue cannot render as empty. */
  inboxError: string | null
  /** Workspace ID to its last failed read; `inboxError` is one of these. */
  inboxErrors: Record<string, string>
  /** Bumped per read, so a slow one cannot overwrite a newer one's answer. */
  inboxRequest: number
  /**
   * Workspace ID to a count of `workspace.approval` events applied to it,
   * so a full read that started before one cannot overwrite its change.
   */
  inboxEvents: Record<string, number>
  setInbox: (workspaceID: string, approvals: Approval[]) => void
  noteInboxEvent: (workspaceID: string) => void
  decideApproval: (
    workspaceID: string,
    approvalID: string,
    decision: ApprovalDecision,
    decidedBy: string,
    decidedAt: string,
  ) => void
  setInboxError: (workspaceID: string, error: string | null) => void
  startInboxRead: () => number
  setShowDecided: (show: boolean) => void
}

export const createApprovalsSlice: SliceCreator<ApprovalsSlice> = (set, get) => ({
  inbox: {},
  approvalsByRun: {},
  showDecided: false,
  inboxError: null,
  inboxErrors: {},
  inboxRequest: 0,
  inboxEvents: {},
  setInbox: (workspaceID, approvals) =>
    set((s) => {
      const inbox = { ...s.inbox, [workspaceID]: approvals }
      return { inbox, approvalsByRun: indexByRun(inbox) }
    }),
  noteInboxEvent: (workspaceID) =>
    set((s) => ({
      inboxEvents: { ...s.inboxEvents, [workspaceID]: (s.inboxEvents[workspaceID] ?? 0) + 1 },
    })),
  decideApproval: (workspaceID, approvalID, decision, decidedBy, decidedAt) =>
    set((s) => {
      const listed = s.inbox[workspaceID] ?? []
      // A hidden decided request leaves the list the way a re-read would.
      const next = decision === 'requested' || s.showDecided
        ? listed.map((a) =>
            a.id === approvalID
              ? { ...a, decision, decided_by: decidedBy || undefined, decided_at: decision === 'requested' ? undefined : decidedAt }
              : a,
          )
        : listed.filter((a) => a.id !== approvalID)
      const inbox = { ...s.inbox, [workspaceID]: next }
      return { inbox, approvalsByRun: indexByRun(inbox) }
    }),
  setInboxError: (workspaceID, error) =>
    set((s) => {
      if ((s.inboxErrors[workspaceID] ?? null) === error) return {}
      const inboxErrors = { ...s.inboxErrors }
      if (error === null) delete inboxErrors[workspaceID]
      else inboxErrors[workspaceID] = error
      return { inboxErrors, inboxError: Object.values(inboxErrors)[0] ?? null }
    }),
  startInboxRead: () => {
    const inboxRequest = get().inboxRequest + 1
    set({ inboxRequest })
    return inboxRequest
  },
  setShowDecided: (showDecided) => set({ showDecided }),
})

/** Oldest request first. */
export function pendingApprovals(inbox: Record<string, Approval[]>): Approval[] {
  return sortByCreated(
    Object.values(inbox)
      .flat()
      .filter((a) => a.decision === 'requested'),
  )
}

function indexByRun(inbox: Record<string, Approval[]>): Record<string, Approval[]> {
  const index: Record<string, Approval[]> = {}
  for (const approval of pendingApprovals(inbox)) {
    const listed = index[approval.run_id]
    if (listed) listed.push(approval)
    else index[approval.run_id] = [approval]
  }
  return index
}

export function sortByCreated(approvals: Approval[]): Approval[] {
  return [...approvals].sort((a, b) => a.created_at.localeCompare(b.created_at))
}

/**
 * The payload carries no request text, so an unknown request is read with its
 * workspace's list without holding up the events behind it.
 */
export function applyApprovalEvent(store: RootStore, client: Api, ev: Event): void {
  const p = (ev.payload ?? {}) as { request_id?: string; decision?: ApprovalDecision }
  if (!ev.workspace_id || !p.request_id || !p.decision) return
  const s = store.getState()
  s.noteInboxEvent(ev.workspace_id)
  if (s.inbox[ev.workspace_id]?.some((a) => a.id === p.request_id)) {
    s.decideApproval(ev.workspace_id, p.request_id, p.decision, ev.actor_id, ev.time)
    return
  }
  if (p.decision !== 'requested' && !s.showDecided) return
  readInbox(store, client, ev.workspace_id)
}

export function readInbox(store: RootStore, client: Api, workspaceID: string, attempt = 0): void {
  coalesce(store, `approvals:${workspaceID}`, async () => {
    const before = store.getState()
    try {
      const list = await client.approvalList(workspaceID, before.showDecided)
      const now = store.getState()
      // An event applied meanwhile may be newer than this answer: read again.
      if (
        now.inboxEvents[workspaceID] !== before.inboxEvents[workspaceID] ||
        now.showDecided !== before.showDecided
      ) {
        return false
      }
      now.setInbox(workspaceID, list)
      now.setInboxError(workspaceID, null)
    } catch (err) {
      store.getState().setInboxError(workspaceID, message(err))
      setTimeout(() => {
        const now = store.getState()
        if (attempt > 0 && (now.connection !== 'live' || now.inboxErrors[workspaceID] === undefined)) return
        readInbox(store, client, workspaceID, attempt + 1)
      }, readRetryDelay(attempt))
    }
  })
}

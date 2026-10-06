import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Approval, ApprovalDecision, Event } from '@/lib/types'
import type { RootStore } from '@/store'
import type { SliceCreator } from '@/store/slice'

export interface ApprovalsSlice {
  /** Workspace ID to that workspace's inbox, as the last fetch saw it. */
  inbox: Record<string, Approval[]>
  /** The inbox view also lists already-decided requests when set. */
  showDecided: boolean
  /**
   * Run ID to that run's pending requests, oldest first, rebuilt whenever
   * the inbox changes so a row reads its count without walking every queue.
   */
  approvalsByRun: Record<string, Approval[]>
  /** The last read's failure, so an unreadable queue cannot render as empty. */
  inboxError: string | null
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
  setInboxError: (error: string | null) => void
  startInboxRead: () => number
  setShowDecided: (show: boolean) => void
}

export const createApprovalsSlice: SliceCreator<ApprovalsSlice> = (set, get) => ({
  inbox: {},
  approvalsByRun: {},
  showDecided: false,
  inboxError: null,
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
  setInboxError: (inboxError) => set({ inboxError }),
  startInboxRead: () => {
    const inboxRequest = get().inboxRequest + 1
    set({ inboxRequest })
    return inboxRequest
  },
  setShowDecided: (showDecided) => set({ showDecided }),
})

/** Everything still waiting on somebody, oldest request first. */
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
 * Applies one `workspace.approval` event. The payload names the request and
 * its decision but not its text, so a request the inbox does not hold yet is
 * read with its workspace's list; a known one is updated in place.
 */
export async function applyApprovalEvent(store: RootStore, client: Api, ev: Event): Promise<void> {
  const p = (ev.payload ?? {}) as { request_id?: string; decision?: ApprovalDecision }
  if (!ev.workspace_id || !p.request_id || !p.decision) return
  const s = store.getState()
  s.noteInboxEvent(ev.workspace_id)
  if (s.inbox[ev.workspace_id]?.some((a) => a.id === p.request_id)) {
    s.decideApproval(ev.workspace_id, p.request_id, p.decision, ev.actor_id, ev.time)
    return
  }
  if (p.decision !== 'requested' && !s.showDecided) return
  try {
    store.getState().setInbox(ev.workspace_id, await client.approvalList(ev.workspace_id, s.showDecided))
  } catch (err) {
    // An unreadable queue must not render as empty; the next full read clears this.
    store.getState().setInboxError(message(err))
  }
}

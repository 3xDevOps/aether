import type { Approval } from '@/lib/types'
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
  setInbox: (workspaceID: string, approvals: Approval[]) => void
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
  setInbox: (workspaceID, approvals) =>
    set((s) => {
      const inbox = { ...s.inbox, [workspaceID]: approvals }
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

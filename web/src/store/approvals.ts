import type { Approval } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface ApprovalsSlice {
  /** Workspace ID to that workspace's inbox, as the last fetch saw it. */
  inbox: Record<string, Approval[]>
  /** The inbox view also lists already-decided requests when set. */
  showDecided: boolean
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
  showDecided: false,
  inboxError: null,
  inboxRequest: 0,
  setInbox: (workspaceID, approvals) =>
    set((s) => ({ inbox: { ...s.inbox, [workspaceID]: approvals } })),
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

/** One run's share of the queue, pending first. */
export function approvalsForRun(
  inbox: Record<string, Approval[]>,
  runID: string,
): Approval[] {
  return pendingApprovals(inbox).filter((a) => a.run_id === runID)
}

export function sortByCreated(approvals: Approval[]): Approval[] {
  return [...approvals].sort((a, b) => a.created_at.localeCompare(b.created_at))
}

import type { Approval } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface ApprovalsSlice {
  /** Workspace ID to that workspace's inbox, as the last fetch saw it. */
  inbox: Record<string, Approval[]>
  /** The inbox view also lists already-decided requests when set. */
  showDecided: boolean
  setInbox: (workspaceID: string, approvals: Approval[]) => void
  setShowDecided: (show: boolean) => void
}

export const createApprovalsSlice: SliceCreator<ApprovalsSlice> = (set) => ({
  inbox: {},
  showDecided: false,
  setInbox: (workspaceID, approvals) =>
    set((s) => ({ inbox: { ...s.inbox, [workspaceID]: approvals } })),
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

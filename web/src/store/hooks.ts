import { useMemo } from 'react'
import { pendingApprovalKey } from '@/lib/status'
import { useStore } from '@/store'
import {
  sidebarGroups,
  sidebarSessions,
  sortRuns,
  type SidebarGroup,
  type SidebarRun,
} from '@/store/selectors'

/**
 * The pending-approval run set, stable across inbox refetches that changed
 * nothing: the store subscription is on a string key, so an unchanged queue
 * neither re-renders subscribers nor invalidates downstream memos.
 */
export function usePendingApprovalRuns(): Set<string> {
  const key = useStore((s) => pendingApprovalKey(s.inbox))
  return useMemo(() => new Set(key ? key.split('\n') : []), [key])
}

function useSidebarInput() {
  const sessions = useStore((s) => s.sessions)
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const groupBy = useStore((s) => s.groupBy)
  const pending = usePendingApprovalRuns()
  return useMemo(
    () => ({ sessions, runs, members, groupBy, pending }),
    [sessions, runs, members, groupBy, pending],
  )
}

export function useSidebarGroups(): SidebarGroup[] {
  const input = useSidebarInput()
  return useMemo(() => sidebarGroups(input), [input])
}

/**
 * How many runs are waiting on a human right now - the count behind the
 * sidebar's badge. Stalls, clean exits and pending approvals all present
 * as needs-attention, so one number covers the whole notification path.
 */
export function useAttentionCount(): number {
  const input = useSidebarInput()
  return useMemo(
    () =>
      sidebarSessions(input)
        .flatMap((entry) => entry.runs)
        .filter((r) => r.state === 'needs-attention').length,
    [input],
  )
}

/** Every run, worst state and most recent change first. */
export function useAttentionRuns(): SidebarRun[] {
  const input = useSidebarInput()
  return useMemo(
    () => sortRuns(sidebarSessions(input).flatMap((entry) => entry.runs)),
    [input],
  )
}

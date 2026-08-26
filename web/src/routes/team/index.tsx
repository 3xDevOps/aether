// The team surfaces: presence, the shared approval inbox, the workspace
// activity feed, and budgets. They reach into the run board and the status
// bar through the slots those views expose, and the two full views are
// ordinary registry routes.

import { registerSlot } from '@/components/slots'
import type { Api } from '@/lib/api'
import { ApprovalBadge, ApprovalInbox, ApprovalStatus } from '@/routes/team/approvals'
import { BudgetStatus } from '@/routes/team/budget'
import { PresenceStatus, Watchers } from '@/routes/team/presence'
import { registerRoute } from '@/routes/registry'
import { useTeamRefresh } from '@/routes/team/sync'
import { TimelineFeed, TimelineStatus } from '@/routes/team/timeline'

/**
 * All four readouts share one status-bar entry, which is also where the
 * refresh they all depend on is mounted: the status bar is the one surface
 * that is always on screen.
 */
export function TeamStatus({ client }: { client?: Api }) {
  useTeamRefresh(client)
  return (
    <>
      <BudgetStatus />
      <ApprovalStatus />
      <TimelineStatus />
      <PresenceStatus />
    </>
  )
}

registerSlot('statusbar', 'team', TeamStatus)
registerSlot('card:badges', 'approvals', ApprovalBadge)
registerSlot('card:footer', 'watchers', Watchers)
registerRoute('approvals', ApprovalInbox)
registerRoute('timeline', TimelineFeed)

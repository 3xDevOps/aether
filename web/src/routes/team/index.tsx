// Team routes and their concrete shell/card contributions. The outer status
// contribution owns refresh; the details readouts never start another lifecycle.

import { registerSlot } from '@/components/slots'
import type { Api } from '@/lib/api'
import { ApprovalBadge, ApprovalInbox, ApprovalStatus } from '@/routes/team/approvals'
import { BudgetStatus } from '@/routes/team/budget'
import { PresenceStatus, Watchers } from '@/routes/team/presence'
import { registerRoute } from '@/routes/registry'
import { useTeamRefresh } from '@/routes/team/sync'
import { TimelineFeed } from '@/routes/team/timeline'
import { useStore } from '@/store'

/** Keep refresh and the phone attention signal alive outside status details. */
export function TeamStatus({ client }: { client?: Api }) {
  useTeamRefresh(client)
  return <ApprovalStatus />
}

/** Secondary readouts, mounted once inside the shell's persistent disclosure. */
export function TeamStatusDetails() {
  const error = useStore((s) => s.inboxError)
  return (
    <>
      <BudgetStatus />
      {error && <p role="alert" className="break-words text-state-failed">{error}</p>}
      <PresenceStatus />
    </>
  )
}

registerSlot('statusbar', 'team', TeamStatus)
registerSlot('card:badges', 'approvals', ApprovalBadge)
registerSlot('card:footer', 'watchers', Watchers)
registerRoute('approvals', ApprovalInbox)
registerRoute('timeline', TimelineFeed)

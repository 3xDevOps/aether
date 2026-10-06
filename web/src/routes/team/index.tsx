import { registerSlot } from '@/components/slots'
import { ApprovalInbox } from '@/routes/team/approvals'
import { Watchers } from '@/routes/team/presence'
import { registerRoute } from '@/routes/registry'
import { TimelineFeed } from '@/routes/team/timeline'

export { useTeamRefresh } from '@/routes/team/sync'

registerSlot('card:footer', 'watchers', Watchers)
registerRoute('approvals', ApprovalInbox)
registerRoute('timeline', TimelineFeed)

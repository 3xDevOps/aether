import { ApprovalInbox } from '@/routes/team/approvals'
import { registerRoute } from '@/routes/registry'
import { TimelineFeed } from '@/routes/team/timeline'

export { useTeamRefresh } from '@/routes/team/sync'

registerRoute('approvals', ApprovalInbox)
registerRoute('timeline', TimelineFeed)

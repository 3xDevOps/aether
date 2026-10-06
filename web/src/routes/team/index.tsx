import { ApprovalInbox } from '@/routes/team/approvals'
import { registerRoute } from '@/routes/registry'

export { useTeamRefresh } from '@/routes/team/sync'

registerRoute('approvals', ApprovalInbox)

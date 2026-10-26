import { Users } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { StatusDot } from '@/components/ui/status-dot'
import { reopen } from '@/components/failure'
import { memberName } from '@/lib/member-name'
import type { MergeProposal } from '@/lib/types'
import { cn } from '@/lib/utils'
import { wideIndent } from '@/routes/board/run-card'
import { useStore } from '@/store'

export function MergeProposalCard({ proposal }: { proposal: MergeProposal }) {
  const proposer = useStore((s) => proposal.plan?.proposer.display_name ?? memberName(s, proposal.proposer_id))
  return (
    <Card data-merge-proposal-id={proposal.id} raised className="@container">
      <div className="flex items-start gap-3">
        <Users aria-hidden className="mt-0.75 size-3.5 shrink-0 text-state-needs-you" />
        <h3 className="min-w-0 text-ui font-medium break-words text-text">{proposer} proposes merging their sign-ins into your member</h3>
      </div>
      <p className={cn('mt-0.5 text-ui-sm text-muted', wideIndent)}>
        {proposal.refusal ?? 'Review the settings they chose to keep, then accept or cancel.'}
      </p>
      <div className={cn('mt-2 flex min-w-0 items-center gap-1.5 text-ui-sm text-muted', wideIndent)}>
        <StatusDot tone="needs-you" label="Needs you" />
        <span className="min-w-0 flex-1 truncate">Needs you</span>
        <Button variant="secondary" size="sm" onClick={() => reopen('sign-ins', null)}>Review</Button>
      </div>
    </Card>
  )
}

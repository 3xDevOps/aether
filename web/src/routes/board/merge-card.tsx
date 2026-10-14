import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { StateLine } from '@/components/ui/status-dot'
import { reopen } from '@/components/failure'
import { memberName } from '@/lib/member-name'
import type { MergeProposal } from '@/lib/types'
import { useStore } from '@/store'

export function MergeProposalCard({ proposal }: { proposal: MergeProposal }) {
  const proposer = useStore((s) => proposal.plan?.proposer.display_name ?? memberName(s, proposal.proposer_id))
  return (
    <Card data-merge-proposal-id={proposal.id}>
      <StateLine tone="needs-you">{proposer} proposes merging their sign-ins into your member</StateLine>
      <p className="text-ui-sm text-muted">
        {proposal.refusal ?? 'Review the settings they chose to keep, then accept or cancel.'}
      </p>
      <div className="flex items-center justify-end">
        <Button variant="secondary" size="sm" onClick={() => reopen('sign-ins', null)}>Review</Button>
      </div>
    </Card>
  )
}

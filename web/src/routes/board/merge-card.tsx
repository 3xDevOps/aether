import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { StateLine } from '@/components/ui/status-dot'
import { reopen } from '@/components/failure'
import type { MergePlan, MergeProposal } from '@/lib/types'

export function MergeProposalCard({ proposal }: { proposal: MergeProposal & { plan: MergePlan } }) {
  return (
    <Card data-merge-proposal-id={proposal.id}>
      <StateLine tone="needs-you">{proposal.plan.proposer.display_name} proposes merging their sign-ins into your member</StateLine>
      <p className="text-ui-sm text-muted">Review the settings they chose to keep, then accept or cancel.</p>
      <div className="flex items-center justify-end">
        <Button variant="secondary" size="sm" onClick={() => reopen('sign-ins', null)}>Review</Button>
      </div>
    </Card>
  )
}

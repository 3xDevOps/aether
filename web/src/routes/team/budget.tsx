import { CircleAlert, TriangleAlert } from 'lucide-react'
import { Chip } from '@/components/ui/heroui'
import { budgetStateLabel, money } from '@/lib/format'
import type { BudgetState } from '@/lib/types'
import { useStore } from '@/store'
import { costTotals } from '@/store/cost'

/**
 * A budget is a soft cap: it warns and it reports being past the limit, and
 * that is all it ever does. Nothing here may suggest a run was stopped,
 * because none ever is.
 */
const stateStyle: Record<BudgetState, string> = {
  ok: '',
  warn: 'text-state-waiting',
  exceeded: 'text-state-needs-attention',
}

/** Workspace spend and budget state, in the status bar. */
export function BudgetStatus() {
  const budgets = useStore((s) => s.budgets)
  const workspaces = useStore((s) => s.workspaces)
  const totals = costTotals(budgets)
  if (Object.keys(budgets).length === 0) return null

  const lines = totals.budgeted.map((report) => {
    const name = workspaces[report.workspace_id]?.name ?? report.workspace_id
    return `${name}: ${money.format(report.spend.cost_usd)} of ${money.format(
      report.budget?.limit_usd ?? 0,
    )} - ${budgetStateLabel[report.state]}`
  })
  lines.push('Budgets are advisory: a run is never stopped for being over one.')
  if (totals.advisory) {
    lines.push('Some runs report no usage, so the total is a floor.')
  }

  const stateColor =
    totals.state === 'exceeded' ? 'danger' : totals.state === 'warn' ? 'warning' : 'success'

  return (
    <span
      className={`flex items-center gap-1.5 ${stateStyle[totals.state]}`}
      title={lines.join('\n')}
      aria-label={`Budget ${money.format(totals.costUSD)}${totals.advisory ? '+' : ''}`}
    >
      <StateIcon state={totals.state} />
      <Chip color={stateColor} variant="soft" size="sm">
        <Chip.Label>
          {money.format(totals.costUSD) + (totals.advisory ? '+' : '')}
        </Chip.Label>
      </Chip>
      {totals.state !== 'ok' && (
        <Chip color={stateColor} variant="tertiary" size="sm">
          <Chip.Label>{budgetStateLabel[totals.state]}</Chip.Label>
        </Chip>
      )}
    </span>
  )
}

function StateIcon({ state }: { state: BudgetState }) {
  if (state === 'exceeded') {
    return <CircleAlert className="size-3.5" aria-label="Past the cap" />
  }
  if (state === 'warn') {
    return <TriangleAlert className="size-3.5" aria-label="Nearing the cap" />
  }
  return null
}

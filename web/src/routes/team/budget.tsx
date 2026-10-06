import { CircleAlert, TriangleAlert } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { budgetStateLabel, money } from '@/lib/format'
import type { BudgetState } from '@/lib/types'
import { useStore } from '@/store'
import { costTotals } from '@/store/cost'

/** A budget is a soft cap that never stops a run, so nothing here may suggest one was stopped. */
const stateStyle: Record<BudgetState, string> = {
  ok: '',
  warn: 'text-state-waiting',
  exceeded: 'text-state-needs-attention',
}

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
    totals.state === 'exceeded' ? 'failed' : totals.state === 'warn' ? 'needs-you' : 'done'

  return (
    <span
      className={`inline-flex h-[22px] min-h-[22px] min-w-0 max-w-full items-center gap-1.5 px-1.5 text-xs ${stateStyle[totals.state]}`}
      title={lines.join('\n')}
      aria-label={`Budget ${money.format(totals.costUSD)}${totals.advisory ? '+' : ''}`}
    >
      <StateIcon state={totals.state} />
      <Badge tone={stateColor}>
          {money.format(totals.costUSD) + (totals.advisory ? '+' : '')}
      </Badge>
      {totals.state !== 'ok' && (
        <Badge tone={stateColor}>
          {budgetStateLabel[totals.state]}
        </Badge>
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

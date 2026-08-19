import { CircleAlert, TriangleAlert } from 'lucide-react'
import type { BudgetState } from '@/lib/types'
import { useStore } from '@/store'
import { costTotals } from '@/store/cost'

const money = new Intl.NumberFormat(undefined, {
  style: 'currency',
  currency: 'USD',
})

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

const stateLabel: Record<BudgetState, string> = {
  ok: 'within budget',
  warn: 'nearing the cap',
  exceeded: 'past the cap',
}

/** Session spend and budget state, in the status bar. */
export function BudgetStatus() {
  const budgets = useStore((s) => s.budgets)
  const sessions = useStore((s) => s.sessions)
  const totals = costTotals(budgets)
  if (Object.keys(budgets).length === 0) return null

  const lines = totals.budgeted.map((report) => {
    const name = sessions[report.session_id]?.name ?? report.session_id
    return `${name}: ${money.format(report.spend.cost_usd)} of ${money.format(
      report.budget?.limit_usd ?? 0,
    )} - ${stateLabel[report.state]}`
  })
  lines.push('Budgets are advisory: a run is never stopped for being over one.')
  if (totals.advisory) {
    lines.push('Some runs report no usage, so the total is a floor.')
  }

  return (
    <span
      className={`flex items-center gap-1 ${stateStyle[totals.state]}`}
      title={lines.join('\n')}
    >
      <StateIcon state={totals.state} />
      <span>{money.format(totals.costUSD) + (totals.advisory ? '+' : '')}</span>
      {totals.state !== 'ok' && <span>{stateLabel[totals.state]}</span>}
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

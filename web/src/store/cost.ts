import type { BudgetReport, BudgetState } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface CostSlice {
  /** Session ID to its budget report: the cap, its state, and the spend. */
  budgets: Record<string, BudgetReport>
  setBudget: (report: BudgetReport) => void
}

export const createCostSlice: SliceCreator<CostSlice> = (set) => ({
  budgets: {},
  setBudget: (report) =>
    set((s) => ({ budgets: { ...s.budgets, [report.session_id]: report } })),
})

/** Worst first: a warning anywhere outranks every session that is fine. */
const severity: BudgetState[] = ['exceeded', 'warn', 'ok']

export interface CostTotals {
  costUSD: number
  /** The worst state any budgeted session is in. */
  state: BudgetState
  /**
   * True while any part of the spend is unmetered - a harness with no
   * adapter reports nothing - which makes the total a floor, not a
   * measurement.
   */
  advisory: boolean
  /** Sessions carrying a budget, worst state first. */
  budgeted: BudgetReport[]
}

export function costTotals(budgets: Record<string, BudgetReport>): CostTotals {
  const reports = Object.values(budgets)
  return {
    costUSD: reports.reduce((sum, r) => sum + r.spend.cost_usd, 0),
    state: reports.reduce<BudgetState>(
      (worst, r) =>
        severity.indexOf(r.state) < severity.indexOf(worst) ? r.state : worst,
      'ok',
    ),
    advisory: reports.some((r) => r.advisory || r.spend.unmetered_runs > 0),
    budgeted: reports
      .filter((r) => r.budget)
      .sort((a, b) => severity.indexOf(a.state) - severity.indexOf(b.state)),
  }
}

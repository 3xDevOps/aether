import type { BudgetReport, BudgetState, CostRollup } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/** The `workspace.budget` event payload. */
export interface BudgetPayload {
  state: BudgetState
  spend_usd: number
  limit_usd: number
  warn_usd?: number
  override?: boolean
  unmetered_runs?: number
}

export interface CostSlice {
  /** Workspace ID to its budget report: the cap, its state, and the spend. */
  budgets: Record<string, BudgetReport>
  setBudget: (report: BudgetReport) => void
  applyBudgetEvent: (workspaceID: string, payload: BudgetPayload) => void
}

const noSpend: CostRollup = {
  runs: 0,
  metered_runs: 0,
  unmetered_runs: 0,
  input_tokens: 0,
  output_tokens: 0,
  cost_usd: 0,
}

export const createCostSlice: SliceCreator<CostSlice> = (set) => ({
  budgets: {},
  setBudget: (report) =>
    set((s) => ({ budgets: { ...s.budgets, [report.workspace_id]: report } })),
  // The event carries the state, the cap and the total, not the per-run
  // rollup, so the counts the report already had stay.
  applyBudgetEvent: (workspaceID, p) =>
    set((s) => {
      const current = s.budgets[workspaceID]
      const unmetered = p.unmetered_runs ?? 0
      const report: BudgetReport = {
        workspace_id: workspaceID,
        state: p.state,
        budget: p.limit_usd > 0
          ? {
              ...current?.budget,
              workspace_id: workspaceID,
              limit_usd: p.limit_usd,
              warn_usd: p.warn_usd,
              override: p.override,
            }
          : undefined,
        spend: { ...(current?.spend ?? noSpend), cost_usd: p.spend_usd, unmetered_runs: unmetered },
        advisory: unmetered > 0,
      }
      return { budgets: { ...s.budgets, [workspaceID]: report } }
    }),
})

/** Worst first: a warning anywhere outranks every workspace that is fine. */
const severity: BudgetState[] = ['exceeded', 'warn', 'ok']

export interface CostTotals {
  costUSD: number
  /** The worst state any budgeted workspace is in. */
  state: BudgetState
  /**
   * True while any part of the spend is unmetered - a harness with no
   * adapter reports nothing - which makes the total a floor, not a
   * measurement.
   */
  advisory: boolean
  /** Workspaces carrying a budget, worst state first. */
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

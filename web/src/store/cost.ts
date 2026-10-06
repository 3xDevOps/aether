import type { Api } from '@/lib/api'
import type { BudgetReport, BudgetState, CostRollup } from '@/lib/types'
import type { RootStore } from '@/store'
import { coalesce } from '@/store/coalesce'
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
  /** Workspace ID to its budget report. */
  budgets: Record<string, BudgetReport>
  /** `workspace.budget` events per workspace, so a read one overtook is dropped. */
  budgetEvents: Record<string, number>
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
  budgetEvents: {},
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
      return {
        budgets: { ...s.budgets, [workspaceID]: report },
        budgetEvents: { ...s.budgetEvents, [workspaceID]: (s.budgetEvents[workspaceID] ?? 0) + 1 },
      }
    }),
})

const budgetReadDelayMs = 1500
const budgetTimers = new WeakMap<RootStore, Map<string, ReturnType<typeof setTimeout>>>()

export function scheduleBudgetRead(store: RootStore, client: Api, workspaceID: string): void {
  let timers = budgetTimers.get(store)
  if (!timers) {
    timers = new Map()
    budgetTimers.set(store, timers)
  }
  clearTimeout(timers.get(workspaceID))
  timers.set(workspaceID, setTimeout(() => {
    timers.delete(workspaceID)
    coalesce(store, `budget:${workspaceID}`, async () => {
      const stamp = store.getState().budgetEvents[workspaceID]
      const report = await client.budgetGet(workspaceID).catch(() => null)
      if (report && store.getState().budgetEvents[workspaceID] === stamp) store.getState().setBudget(report)
    })
  }, budgetReadDelayMs))
}

/** Worst first: a warning anywhere outranks every workspace that is fine. */
const severity: BudgetState[] = ['exceeded', 'warn', 'ok']

export interface CostTotals {
  costUSD: number
  state: BudgetState
  /** Some spend is unmetered, so the total is a floor, not a measurement. */
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

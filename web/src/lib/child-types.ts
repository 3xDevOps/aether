// JSON contracts from internal/protocol/children.go.

export type RunChildrenSetting = '' | 'on' | 'off'

export type ChildState =
  | 'queued'
  | 'starting'
  | 'working'
  | 'waiting'
  | 'needs-you'
  | 'finished'
  | 'failed'
  | 'ended'
  | 'cancelled'
  | 'refused'

export interface ChildLimits {
  max_live: number
}

export interface ChildReport {
  outcome: string
  summary: string
  at: string
}

export interface Child {
  run_id: string
  parent_run_id: string
  root_run_id: string
  purpose: string
  harness: string
  mode: string
  model?: string
  owner_member_id: string
  account_member_id: string
  state: ChildState
  detached?: boolean
  branch?: string
  base_commit?: string
  report?: ChildReport
  error?: string
  created_at: string
  /** Absent when the run reported no cost. */
  cost_usd?: number
}

export interface AccountUsage {
  account_member_id: string
  runs: number
  cost_usd: number
}

/** `cost_usd` covers metered runs only. */
export interface ChildUsage {
  runs: number
  metered_runs: number
  unmetered_runs: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
  by_account: AccountUsage[]
}

/** A run's descendants, nearest first, and the usage of the run plus all of them. */
export interface RunChildrenResult {
  root_run_id: string
  children: Child[]
  limits: ChildLimits
  usage: ChildUsage
}

export interface ChildSettings {
  enabled: boolean
}

/** An absent field keeps its value. */
export interface ChildSettingsParams {
  enabled?: boolean
}

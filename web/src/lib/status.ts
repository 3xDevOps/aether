// Presentation states. These are UI-only: the domain run status enum is
// unchanged, this is the two-layer status vocabulary from the GUI spec
// (the harness glyph says who, the state dot says what).

import type { Approval, RunStatus } from '@/lib/types'
import { pendingApprovals } from '@/store/approvals'

export type PresentationState =
  | 'needs-attention'
  | 'failed'
  | 'working'
  | 'waiting'
  | 'done'
  | 'idle'

/**
 * A plan or approval pause is invisible in the domain status - the run still
 * reads `running` while the agent sits on a pending request - so an active
 * run with one presents as needs-attention.
 */
export function runState(status: RunStatus, pendingApproval = false): PresentationState {
  switch (status) {
    case 'queued':
    case 'provisioning':
      return pendingApproval ? 'needs-attention' : 'waiting'
    case 'running':
      return pendingApproval ? 'needs-attention' : 'working'
    case 'needs-attention':
      return 'needs-attention'
    case 'failed':
    case 'interrupted':
      return 'failed'
    case 'merged':
    case 'abandoned':
      return 'done'
  }
}

/** A run's human title. Empty titles fall back to the task. */
export function runLabel(run: { task: string; title?: string }): string {
  return run.title?.trim() || run.task.trim() || 'Untitled run'
}

/** The runs an approval request is still waiting on, across every inbox. */
export function pendingApprovalRuns(
  inbox: Record<string, Approval[]>,
): Set<string> {
  return new Set(pendingApprovals(inbox).map((a) => a.run_id))
}

/**
 * The same set as one stable string. An inbox refetch that changed nothing
 * still builds fresh object identities; subscribing on this key lets memos
 * and store selectors see through that instead of rebuilding derived trees.
 */
export function pendingApprovalKey(inbox: Record<string, Approval[]>): string {
  return [...pendingApprovalRuns(inbox)].sort().join('\n')
}

// Worst-first. A run group shows the worst state of its runs.
const severity: PresentationState[] = [
  'needs-attention',
  'failed',
  'working',
  'waiting',
  'done',
  'idle',
]

export function stateRank(s: PresentationState): number {
  return severity.indexOf(s)
}

export function rollup(states: PresentationState[]): PresentationState {
  return states.reduce<PresentationState>(
    (worst, s) => (stateRank(s) < stateRank(worst) ? s : worst),
    'idle',
  )
}

export const stateLabel: Record<PresentationState, string> = {
  'needs-attention': 'Needs you',
  failed: 'Failed',
  working: 'Working',
  waiting: 'Waiting',
  done: 'Done',
  idle: 'Idle',
}

// Token classes only - no colour literals in components.
export const stateDotClass: Record<PresentationState, string> = {
  'needs-attention': 'bg-state-needs-attention',
  failed: 'bg-state-failed',
  working: 'bg-state-working',
  waiting: 'bg-state-waiting',
  done: 'bg-state-done',
  idle: 'bg-state-idle',
}

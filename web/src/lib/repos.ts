import type { RunRepo } from '@/lib/types'

/** The gateway method that tells the dashboard the server can launch and serve multi-repo runs. */
export const multiRepoMethod = 'run.repos'

/** How many repos beyond the primary the run has; 0 for a single-repo run. */
export function extraRepoCount(run: { repos?: RunRepo[] }): number {
  return Math.max(0, (run.repos?.length ?? 1) - 1)
}

/** The repo's name, with how many more the run has when it has any. */
export function repoLabel(name: string, run: { repos?: RunRepo[] }): string {
  const extra = extraRepoCount(run)
  return extra > 0 ? `${name} +${extra}` : name
}

import type { RunRepo } from '@/lib/types'

export const multiRepoMethod = 'run.repos'

export function extraRepoCount(run: { repos?: RunRepo[] }): number {
  return Math.max(0, (run.repos?.length ?? 1) - 1)
}

export function repoLabel(name: string, run: { repos?: RunRepo[] }): string {
  const extra = extraRepoCount(run)
  return extra > 0 ? `${name} +${extra}` : name
}

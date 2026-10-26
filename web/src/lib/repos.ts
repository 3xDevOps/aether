import type { RunRepo } from '@/lib/types'

export const multiRepoMethod = 'run.repos'

/** The `server.info` feature of a server that mounts extra repos; an older server drops them without error. */
export const multiRepoFeature = 'multi-repo'

/** The `server.info` feature of a server whose `mission.create` takes extra repos; an older one drops them. */
export const swarmReposFeature = 'swarm-repos'

export function extraRepoCount(run: { repos?: RunRepo[] }): number {
  return Math.max(0, (run.repos?.length ?? 1) - 1)
}

export function repoLabel(name: string, run: { repos?: RunRepo[] }): string {
  const extra = extraRepoCount(run)
  return extra > 0 ? `${name} +${extra}` : name
}

import { useMemo } from 'react'
import { extraRepoCount } from '@/lib/repos'
import type { Run, RunRepo } from '@/lib/types'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

export type ScopedRecord = RunRecord & { repo?: string }

type Scoped<R extends Run> = R & { repo?: string }

export function useRepoRuns<R extends Run>(run: R): { repo: RunRepo; run: Scoped<R> }[] {
  const workspaces = useStore((s) => s.workspaces)
  return useMemo(() => {
    if (extraRepoCount(run) === 0) return []
    return run.repos!.map((repo) => ({
      repo,
      run: repo.primary
        ? run
        : {
            ...run,
            repo: repo.workspace_id,
            branch: repo.branch,
            base_branch: workspaces[repo.workspace_id]?.base_branch ?? '',
            base_commit: undefined,
          },
    }))
  }, [run, workspaces])
}

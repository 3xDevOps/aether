import { useMemo } from 'react'
import { extraRepoCount } from '@/lib/repos'
import type { RunRepo } from '@/lib/types'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

export type ScopedRecord = RunRecord & { repo?: string }

/** The run seen through each of its repos, primary first, the primary being the record itself. Empty for a single-repo run. */
export function useRepoRuns(run: RunRecord): { repo: RunRepo; run: ScopedRecord }[] {
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
            workspace_id: repo.workspace_id,
            branch: repo.branch,
            base_branch: workspaces[repo.workspace_id]?.base_branch ?? '',
            base_commit: undefined,
          },
    }))
  }, [run, workspaces])
}

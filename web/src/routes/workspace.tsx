import { useMemo, useState } from 'react'
import { timeAgo } from '@/lib/format'
import { RunList } from '@/components/run-list'
import { Chip } from '@/components/ui/heroui'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import {
  BudgetDialog,
  WorkspaceSettingsDialog,
} from '@/routes/admin-dialogs'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, usePendingApprovalRuns } from '@/store/hooks'
import { sidebarRuns } from '@/store/selectors'

/**
 * One workspace: its runs, its base branch, and the two settings that used
 * to hide in the Templates header. The spend cap and the steering policy
 * belong beside the thing they govern.
 */
export function WorkspaceView({ params }: RouteProps) {
  const workspaceID = params.workspaceId
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const allRuns = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const groupBy = useStore((s) => s.groupBy)
  const pending = usePendingApprovalRuns()
  const caps = useCapability()
  const [dialog, setDialog] = useState<'budget' | 'settings' | null>(null)

  const runs = useMemo(
    () =>
      sidebarRuns({
        workspace: workspaceID,
        runs: allRuns,
        members,
        groupBy,
        pending,
      }),
    [workspaceID, allRuns, members, groupBy, pending],
  )

  if (!workspace) {
    return (
      <div className="p-6">
        <p className="text-sm text-muted-foreground">Unknown workspace.</p>
      </div>
    )
  }
  const policy =
    workspace.steer_others === 'admins_only'
      ? 'Admins steer other members’ runs'
      : 'Everyone with steer can act'

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title={workspace.name}
        subtitle={`Base branch ${workspace.base_branch}`}
        actions={
          <>
            {caps.hasMethod('budget.set') && (
              <Button size="sm" variant="outline" onClick={() => setDialog('budget')}>
                Budget
              </Button>
            )}
            {caps.hasMethod('workspace.settings') && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => setDialog('settings')}
              >
                Workspace settings
              </Button>
            )}
          </>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <section
          aria-labelledby="workspace-context-heading"
          className="mx-auto mt-4 w-[calc(100%-2rem)] max-w-[1200px] rounded-lg border bg-card p-4 shadow-xs sm:mt-6 sm:p-5"
        >
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 id="workspace-context-heading" className="text-sm font-semibold">
                Workspace context
              </h2>
              <p className="mt-1 text-[13px] leading-5 text-muted-foreground">
                Runs stay scoped to this workspace and branch.
              </p>
            </div>
            <Chip color="default" variant="soft" size="sm">
              <Chip.Label>{runs.length} {runs.length === 1 ? 'run' : 'runs'}</Chip.Label>
            </Chip>
          </div>
          <dl className="mt-4 grid gap-3 border-t pt-4 text-[13px] sm:grid-cols-3">
            <div className="min-w-0">
              <dt className="text-muted-foreground">Base branch</dt>
              <dd className="mt-1 truncate font-mono text-foreground" title={workspace.base_branch}>
                {workspace.base_branch}
              </dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground">Steering policy</dt>
              <dd className="mt-1 text-foreground">{policy}</dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground">Created</dt>
              <dd className="mt-1 text-foreground">
                <time dateTime={workspace.created_at}>{timeAgo(workspace.created_at)}</time>
              </dd>
            </div>
          </dl>
        </section>
        <section aria-labelledby="workspace-runs-heading" className="mt-2">
          <div className="mx-auto flex w-[calc(100%-2rem)] max-w-[1200px] items-center gap-2 px-0 pt-4">
            <h2 id="workspace-runs-heading" className="text-sm font-semibold">
              Runs
            </h2>
            <span className="text-[13px] text-muted-foreground">
              attention first
            </span>
          </div>
          <RunList runs={runs} empty="No runs in this workspace yet." />
        </section>
      </div>
      {dialog === 'budget' && (
        <BudgetDialog
          workspaceID={workspaceID}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === 'settings' && (
        <WorkspaceSettingsDialog
          workspaceID={workspaceID}
          onClose={() => setDialog(null)}
        />
      )}
    </div>
  )
}

registerRoute('workspace', WorkspaceView)

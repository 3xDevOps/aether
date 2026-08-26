import { useMemo, useState } from 'react'
import { RunList } from '@/components/run-list'
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
function WorkspaceView({ params }: RouteProps) {
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
    return <p className="p-4 text-sm text-muted-foreground">Unknown workspace.</p>
  }
  return (
    <div className="flex h-full flex-col">
      <ViewHeader title={workspace.name} subtitle={workspace.base_branch} />
      <div className="flex items-center gap-2 border-b px-4 py-2">
        <span className="flex-1" />
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
      </div>
      <div className="flex-1 overflow-y-auto">
        <RunList runs={runs} empty="No runs in this workspace yet." />
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

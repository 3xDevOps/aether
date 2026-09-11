import { useEffect, useMemo, useState } from 'react'
import { timeAgo } from '@/lib/format'
import type { WorkspaceMirrorResult } from '@/lib/types'
import { api } from '@/lib/api'
import { RunList } from '@/components/run-list'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { Chip } from '@/components/ui/heroui'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import {
  BudgetDialog,
  WorkspaceSettingsDialog,
} from '@/routes/admin-dialogs'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, usePendingApprovalRuns } from '@/store/hooks'
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
  const isAdmin = useIsAdmin()
  const [dialog, setDialog] = useState<'budget' | 'settings' | 'mirror' | null>(null)
  const [mirrorStatus, setMirrorStatus] = useState<WorkspaceMirrorResult | null>(null)
  const canMirror = isAdmin && caps.hasMethod('workspace.mirror.status')
  useEffect(() => {
    setMirrorStatus(null)
    if (!workspace || !canMirror) return
    let live = true
    void api.workspaceMirrorStatus(workspaceID).then(
      (status) => {
        if (live) setMirrorStatus(status)
      },
      () => {
        if (live) setMirrorStatus(null)
      },
    )
    return () => {
      live = false
    }
  }, [workspaceID, workspace, canMirror])

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

  const sourceState = mirrorStatus
    ? mirrorStatus.enabled
      ? mirrorStatus.status ?? 'unknown'
      : 'local-only'
    : 'not checked'

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title={workspace.name}
        subtitle={`Base branch ${workspace.base_branch}`}
        actions={
          <>
            {canMirror && (
              <Button size="sm" variant="outline" onClick={() => setDialog('mirror')}>
                Source control
              </Button>
            )}
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
          className="border-b border-border bg-sidebar/30 px-4 py-3 sm:px-5"
        >
          <div className="mx-auto flex w-full max-w-[1400px] flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 id="workspace-context-heading" className="text-[13px] font-semibold leading-5">
                Workspace context
              </h2>
              <p className="mt-0.5 text-xs leading-4 text-muted-foreground">
                Runs stay scoped to this workspace and branch.
              </p>
            </div>
            <Chip color="default" variant="soft" size="sm">
              <Chip.Label>{runs.length} {runs.length === 1 ? 'run' : 'runs'}</Chip.Label>
            </Chip>
          </div>
          <dl className="mx-auto mt-3 grid w-full max-w-[1400px] gap-3 border-t border-border pt-3 text-xs sm:grid-cols-3 sm:divide-x sm:divide-border">
            <div className="min-w-0 sm:pr-4">
              <dt className="text-muted-foreground">Base branch</dt>
              <dd className="mt-0.5 truncate font-mono text-foreground" title={workspace.base_branch}>
                {workspace.base_branch}
              </dd>
            </div>
            <div className="min-w-0 sm:px-4">
              <dt className="text-muted-foreground">Steering policy</dt>
              <dd className="mt-0.5 break-words text-foreground">{policy}</dd>
            </div>
            <div className="min-w-0 sm:pl-4">
              <dt className="text-muted-foreground">Created</dt>
              <dd className="mt-0.5 text-foreground">
                <time dateTime={workspace.created_at}>{timeAgo(workspace.created_at)}</time>
              </dd>
            </div>
          </dl>
          {canMirror && (
            <div className="mx-auto mt-3 flex w-full max-w-[1400px] flex-wrap items-center justify-between gap-3 border-t border-border pt-3">
              <div className="min-w-0">
                <p className="text-xs text-muted-foreground">Source state</p>
                <p className="mt-0.5 break-all font-mono text-[13px]" data-testid="workspace-source-state">
                  {sourceState}
                </p>
              </div>
              <Button size="sm" variant="outline" onClick={() => setDialog('mirror')}>
                Open Source control
              </Button>
            </div>
          )}
        </section>
        <section aria-labelledby="workspace-runs-heading" className="min-w-0">
          <div className="mx-auto flex min-h-[35px] w-full max-w-[1400px] items-center gap-2 border-b border-border px-4 sm:px-5">
            <h2 id="workspace-runs-heading" className="text-[13px] font-semibold leading-5">
              Runs
            </h2>
            <span className="text-xs text-muted-foreground">attention first</span>
          </div>
          <RunList runs={runs} empty="No runs in this workspace yet." />
        </section>
      </div>
      {dialog === 'mirror' && (
        <WorkspaceMirrorDialog
          workspaceID={workspaceID}
          onStatusChange={setMirrorStatus}
          onClose={() => setDialog(null)}
        />
      )}
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

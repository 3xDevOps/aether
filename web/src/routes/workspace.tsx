import { useEffect, useState } from 'react'
import { message, timeAgo } from '@/lib/format'
import type { WorkspaceMirrorResult } from '@/lib/types'
import { api } from '@/lib/api'
import { RunList } from '@/components/run-list'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { WorkspaceRepositoryDialog } from '@/components/workspace-repository'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import {
  BudgetDialog,
  WorkspaceSettingsDialog,
} from '@/routes/admin-dialogs'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useListedRuns } from '@/store/hooks'

export function WorkspaceView({ params }: RouteProps) {
  const workspaceID = params.workspaceId
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const runs = useListedRuns(workspaceID)
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const [dialog, setDialog] = useState<'budget' | 'settings' | 'mirror' | 'repository' | 'local' | null>(params.repository === 'local' ? 'local' : params.repository === 'remote' ? 'repository' : null)
  const [mirrorStatus, setMirrorStatus] = useState<WorkspaceMirrorResult | null>(null)
  const [mirrorError, setMirrorError] = useState<string | null>(null)
  const repositoryOpen = dialog === 'repository' || dialog === 'local'
  const canMirror = isAdmin && caps.hasMethod('workspace.mirror.status')
  useEffect(() => {
    setDialog(params.repository === 'local' ? 'local' : params.repository === 'remote' ? 'repository' : null)
  }, [workspaceID, params.repository])
  useEffect(() => {
    setMirrorStatus(null)
    setMirrorError(null)
    if (!workspace || !canMirror || repositoryOpen) return
    let live = true
    void api.workspaceMirrorStatus(workspaceID).then(
      (status) => {
        if (live) setMirrorStatus(status)
      },
      (cause) => {
        if (live) setMirrorError(message(cause))
      },
    )
    return () => {
      live = false
    }
  }, [workspaceID, workspace, canMirror, repositoryOpen])

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
            <Button size="sm" variant="secondary" onClick={() => setDialog('repository')}>Repository settings</Button>
            {canMirror && (
              <Button size="sm" variant="secondary" onClick={() => setDialog('mirror')}>
                Source control
              </Button>
            )}
            {isAdmin && caps.hasMethod('budget.set') && (
              <Button size="sm" variant="secondary" onClick={() => setDialog('budget')}>
                Budget
              </Button>
            )}
            {isAdmin && caps.hasMethod('workspace.settings') && (
              <Button
                size="sm"
                variant="secondary"
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
            <Badge>
              {runs.length} {runs.length === 1 ? 'run' : 'runs'}
            </Badge>
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
                {mirrorError && <p role="alert" className="mt-1 whitespace-pre-wrap text-xs text-state-failed">{mirrorError}</p>}
              </div>
              <Button size="sm" variant="secondary" onClick={() => setDialog('mirror')}>
                Open Source control
              </Button>
            </div>
          )}
        </section>
        <div className="flex flex-wrap gap-2 border-b px-4 py-3">
          <Button size="sm" variant="secondary" onClick={() => setDialog('local')}>Link local repository</Button>
          <Button size="sm" variant="secondary" onClick={() => useStore.getState().navigate('workspaces')}>Add another workspace</Button>
          <Button size="sm" variant="secondary" onClick={() => {
            const state = useStore.getState()
            state.setOnboardingWorkspace(workspace.id)
            state.setOnboardingStep('Agents')
            state.navigate('onboarding')
          }}>Set up agents / first run</Button>
        </div>
        <section aria-labelledby="workspace-runs-heading" className="min-w-0">
          <div className="mx-auto flex min-h-[35px] w-full max-w-[1400px] items-center gap-2 border-b border-border px-4 sm:px-5">
            <h2 id="workspace-runs-heading" className="text-[13px] font-semibold leading-5">
              Runs
            </h2>
            <span className="text-xs text-muted-foreground">Needs you first</span>
          </div>
          <RunList runs={runs} empty="No runs in this workspace yet." />
        </section>
      </div>
      {repositoryOpen && <WorkspaceRepositoryDialog key={workspaceID} workspace={workspace} initialLocal={dialog === 'local'} onClose={() => setDialog(null)} />}
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
          onRepository={() => setDialog('repository')}
          onClose={() => setDialog(null)}
        />
      )}
    </div>
  )
}

registerRoute('workspace', WorkspaceView)

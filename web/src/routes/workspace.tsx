import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Confirm } from '@/components/confirm'
import { FolderGit2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { EmptyState } from '@/components/ui/empty-state'
import { PageBody } from '@/components/ui/page-body'
import { SectionLabel } from '@/components/ui/section-label'
import { ViewHeader } from '@/components/view-header'
import { WorkspaceEnvironmentSection } from '@/components/workspace-environment'
import { WorkspaceRepository } from '@/components/workspace-repository'
import { api, type Api } from '@/lib/api'
import { budgetStateLabel, money } from '@/lib/format'
import type { BudgetReport } from '@/lib/types'
import { BudgetDialog, WorkspaceSettingsDialog } from '@/routes/admin-dialogs'
import { WorkspaceAccessDialog } from '@/routes/workspaces/access-dialog'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { ChildLimitsRow } from '@/routes/settings/children'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability, useIsSuperAdmin, useIsWorkspaceAdmin, useWorkspaceAccessServer } from '@/store/hooks'

function budgetWords(report: BudgetReport | undefined): string {
  if (!report?.budget) return 'No budget set. A budget warns and reports; it never stops a session.'
  const spent = money.format(report.spend.cost_usd) + (report.advisory ? '+' : '')
  const warn = report.budget.warn_usd === undefined ? '' : `, warns at ${money.format(report.budget.warn_usd)}`
  return `${spent} spent of ${money.format(report.budget.limit_usd)}${warn}: ${budgetStateLabel[report.state]}. It never stops a session.`
}

export function WorkspaceView({ params, client = api }: RouteProps & { client?: Api }) {
  const workspaceID = params.workspaceId
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const budget = useStore((s) => s.budgets[workspaceID])
  const caps = useCapability()
  const isAdmin = useIsWorkspaceAdmin(workspaceID)
  const navigate = useStore((s) => s.navigate)
  const scoped = useWorkspaceAccessServer()
  const superAdmin = useIsSuperAdmin()
  const selfID = useStore((s) => s.info?.member.id)
  const ownerName = useStore((s) => {
    const id = s.workspaces[workspaceID]?.owner_member_id
    return !id ? '' : id === selfID ? 'you' : s.members[id]?.display_name ?? id
  })
  const [dialog, setDialog] = useState<'budget' | 'settings' | 'delete' | 'access' | null>(null)
  // A route that moves to another workspace, as a deletion elsewhere does, closes any dialog opened for this one.
  const [dialogFor, setDialogFor] = useState(workspaceID)
  if (dialogFor !== workspaceID) {
    setDialogFor(workspaceID)
    setDialog(null)
  }
  const canAccess = scoped && isAdmin && caps.hasMethod('workspace.access.list')
  const [granted, setGranted] = useState<number | null>(null)
  useEffect(() => {
    if (!canAccess || dialog === 'access') return
    let live = true
    client.workspaceAccessList(workspaceID).then((access) => live && setGranted(access.grants.length), () => live && setGranted(null))
    return () => {
      live = false
    }
  }, [canAccess, client, dialog, workspaceID])
  const [settingsClosed, setSettingsClosed] = useState(0)

  if (!workspace) {
    return (
      <div className="flex h-full min-w-0 flex-col">
        <ViewHeader title="Workspace settings" />
        <EmptyState icon={<FolderGit2 />} title="Unknown workspace">It may have been deleted, or this link names another server&apos;s workspace.</EmptyState>
      </div>
    )
  }
  const adminOnly = workspace.steer_others === 'admins_only'

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title={workspace.name} subtitle="Workspace settings" />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <PageBody>
          <SettingsSection title="Base branch" heading="h2">
            <SettingRow
              label={<Code>{workspace.base_branch}</Code>}
              help="New sessions fork from this branch. It is fixed when the workspace is created."
            />
          </SettingsSection>
          <section aria-label="Source and clone" className="flex min-w-0 flex-col gap-2">
            <SectionLabel as="h2">Source and clone</SectionLabel>
            <div className="rounded-panel border border-seam p-4">
              <WorkspaceRepository
                key={workspace.id}
                client={client}
                caps={caps}
                workspace={workspace}
                initialLocal={params.repository === 'local'}
                advancedOpen
              />
            </div>
          </section>
          {caps.hasMethod('workspace.environment.get') && (
            <WorkspaceEnvironmentSection
              key={workspace.id}
              workspaceID={workspace.id}
              client={client}
              editable={isAdmin && caps.hasMethod('workspace.environment.set')}
              reveal={params.section === 'environment'}
            />
          )}
          <SettingsSection title="Team" heading="h2">
            {scoped && ownerName && (
              <SettingRow
                label="Access"
                help={granted === null
                  ? `Owner: ${ownerName}. The owner, super admins and the members granted access can use this workspace.`
                  : `Owner: ${ownerName}. Granted to ${granted} ${granted === 1 ? 'member' : 'members'}; super admins always have access.`}
                control={canAccess && (
                  <Button size="sm" variant="secondary" onClick={() => setDialog('access')}>Access…</Button>
                )}
              />
            )}
            <SettingRow
              label="Budget"
              help={budgetWords(budget)}
              control={isAdmin && caps.hasMethod('budget.set') && (
                <Button size="sm" variant="secondary" onClick={() => setDialog('budget')}>Set budget…</Button>
              )}
            />
            <SettingRow
              label="Messages to others' sessions"
              help={adminOnly
                ? 'Only admins may message sessions started by other members.'
                : 'Any member who can message sessions may also message sessions started by others.'}
              control={isAdmin && caps.hasMethod('workspace.settings') && (
                <Button size="sm" variant="secondary" onClick={() => setDialog('settings')}>Change…</Button>
              )}
            />
            {caps.hasMethod('workspace.child_limits') && (
              <ChildLimitsRow
                client={client}
                workspaceID={workspaceID}
                seq={settingsClosed}
                control={isAdmin && caps.hasMethod('workspace.settings') && (
                  <Button size="sm" variant="secondary" onClick={() => setDialog('settings')}>Change…</Button>
                )}
              />
            )}
          </SettingsSection>
          {isAdmin && caps.hasMethod('workspace.delete') && (
            <SettingsSection title="Danger zone" heading="h2" tone="danger">
              <SettingRow
                label="Delete this workspace"
                help="Deletes its finished sessions, retained containers, files, history and server repository. Members and their environments stay."
                control={<Button size="sm" variant="danger" onClick={() => setDialog('delete')}>Delete…</Button>}
              />
            </SettingsSection>
          )}
        </PageBody>
      </div>
      {dialog === 'budget' && <BudgetDialog workspaceID={workspaceID} client={client} onClose={() => setDialog(null)} />}
      {dialog === 'settings' && <WorkspaceSettingsDialog workspaceID={workspaceID} client={client} onClose={() => { setDialog(null); setSettingsClosed((n) => n + 1) }} />}
      {dialog === 'access' && (
        <WorkspaceAccessDialog
          workspaceID={workspaceID}
          client={client}
          onClose={() => setDialog(null)}
          canChangeOwner={superAdmin && caps.hasMethod('workspace.owner.set')}
        />
      )}
      {dialog === 'delete' && (
        <Confirm
          title={`Delete ${workspace.name}?`}
          description="This permanently deletes the workspace, its finished sessions, retained containers, files, history and server repository. Members, their environments and external repositories stay."
          action="Delete workspace"
          onConfirm={() => client.workspaceDelete(workspaceID)}
          onDone={() => {
            toast.success(`${workspace.name} deleted`)
            const { route } = useStore.getState()
            if (route.name === 'workspace' && route.params.workspaceId === workspaceID) navigate('workspaces')
            useStore.getState().removeWorkspace(workspaceID)
          }}
          onClose={() => setDialog(null)}
        >
          <p className="text-ui-sm text-muted">
            The server refuses while a session is unfinished, review work or cleanup is pending, or a schedule remains.
          </p>
        </Confirm>
      )}
    </div>
  )
}

registerRoute('workspace', WorkspaceView)

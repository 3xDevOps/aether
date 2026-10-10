import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { EmptyState } from '@/components/ui/empty-state'
import { SectionLabel } from '@/components/ui/section-label'
import { ViewHeader } from '@/components/view-header'
import { WorkspaceEnvironmentSection } from '@/components/workspace-environment'
import { WorkspaceRepository } from '@/components/workspace-repository'
import { api, type Api } from '@/lib/api'
import { budgetStateLabel, money } from '@/lib/format'
import type { BudgetReport } from '@/lib/types'
import { BudgetDialog, WorkspaceSettingsDialog } from '@/routes/admin-dialogs'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'

function budgetWords(report: BudgetReport | undefined): string {
  if (!report?.budget) return 'No budget set. A budget warns and reports; it never stops a run.'
  const spent = money.format(report.spend.cost_usd) + (report.advisory ? '+' : '')
  const warn = report.budget.warn_usd === undefined ? '' : `, warns at ${money.format(report.budget.warn_usd)}`
  return `${spent} spent of ${money.format(report.budget.limit_usd)}${warn}: ${budgetStateLabel[report.state]}. It never stops a run.`
}

export function WorkspaceView({ params, client = api }: RouteProps & { client?: Api }) {
  const workspaceID = params.workspaceId
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const budget = useStore((s) => s.budgets[workspaceID])
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const [dialog, setDialog] = useState<'budget' | 'settings' | null>(null)

  if (!workspace) {
    return (
      <div className="flex h-full min-w-0 flex-col">
        <ViewHeader title="Repository" />
        <EmptyState title="Unknown workspace">It may have been deleted, or this link names another server&apos;s workspace.</EmptyState>
      </div>
    )
  }
  const adminOnly = workspace.steer_others === 'admins_only'

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title={workspace.name} subtitle="Repository" />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-8 px-4 py-6 sm:px-6">
          <SettingsSection title="Base branch">
            <SettingRow
              label={<Code>{workspace.base_branch}</Code>}
              help="New runs fork from this branch. It is fixed when the workspace is created."
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
          <SettingsSection title="Team">
            <SettingRow
              label="Budget"
              help={budgetWords(budget)}
              control={isAdmin && caps.hasMethod('budget.set') && (
                <Button size="sm" variant="secondary" onClick={() => setDialog('budget')}>Set budget…</Button>
              )}
            />
            <SettingRow
              label="Messages to others' runs"
              help={adminOnly
                ? 'Only admins may message runs started by other members.'
                : 'Any member who can message runs may also message runs started by others.'}
              control={isAdmin && caps.hasMethod('workspace.settings') && (
                <Button size="sm" variant="secondary" onClick={() => setDialog('settings')}>Change…</Button>
              )}
            />
          </SettingsSection>
        </div>
      </div>
      {dialog === 'budget' && <BudgetDialog workspaceID={workspaceID} client={client} onClose={() => setDialog(null)} />}
      {dialog === 'settings' && <WorkspaceSettingsDialog workspaceID={workspaceID} client={client} onClose={() => setDialog(null)} />}
    </div>
  )
}

registerRoute('workspace', WorkspaceView)

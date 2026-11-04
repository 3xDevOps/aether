import type { ReactNode } from 'react'
import { customAgent, launchable } from '@/components/launch/agent-picker'
import { agentLabel, modeLabel } from '@/components/launch/modes'
import { TaskField, type useAgentChoice } from '@/components/launch/run-fields'
import { GitBranch } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Workspace } from '@/lib/types'
import { cn } from '@/lib/utils'

const chip = 'h-11 w-auto max-w-full min-w-0 shrink rounded-full px-3 text-ui-sm'

/** The launch form below 768px: task first, then repo, branch and agent as chips, Launch, and the rest folded under More options. */
export function PhoneLaunchSheet({
  open,
  onClose,
  onSubmit,
  task,
  onTask,
  shared,
  choice,
  onAgent,
  workspaces,
  workspace,
  onWorkspace,
  lastSession,
  onAddWorkspace,
  options,
  notes,
  failure,
  launch,
}: {
  open: boolean
  onClose: () => void
  onSubmit: () => void
  task: string
  onTask: (task: string) => void
  shared: boolean
  choice: ReturnType<typeof useAgentChoice>
  onAgent: (name: string) => void
  workspaces: Workspace[]
  workspace: Workspace | undefined
  onWorkspace: (id: string) => void
  /** What still matches your newest session, which the sheet starts from. */
  lastSession: 'repository and agent' | 'repository' | 'none'
  onAddWorkspace: () => void
  /** Permission, mode, model, limits and the rest of the desktop form. */
  options: ReactNode
  notes: ReactNode
  /** Stays above Launch, outside the scrolling form, so the keyboard cannot hide it. */
  failure: ReactNode
  launch: ReactNode
}) {
  const { agents, harness, mode } = choice
  const offered = (agents ?? []).filter(launchable)
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent variant="bottom" className="grid-rows-[auto_minmax(0,1fr)_auto_auto] gap-3 overflow-hidden">
        <div className="flex flex-col gap-0.5 pr-10">
          <DialogTitle>New session</DialogTitle>
          <DialogDescription className="sr-only">Starts an agent in its own container on the workspace&apos;s base branch.</DialogDescription>
        </div>
        <form id="launch-form" className="-mx-1 flex min-h-0 min-w-0 flex-col gap-3 overflow-y-auto px-1 py-1 [&>*]:shrink-0" onSubmit={(event) => { event.preventDefault(); onSubmit() }}>
          <TaskField mode={mode} task={task} onTask={onTask} autoFocus shared={shared} brief />
          {workspaces.length === 0 && (
            <Callout tone="neutral" title="No workspace to launch in yet." actions={<Button type="button" size="sm" onClick={onAddWorkspace}>Add a workspace</Button>}>
              A session runs on a workspace&apos;s repository.
            </Callout>
          )}
          <div className="flex min-w-0 flex-col gap-1">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              {workspaces.length > 0 && (
                <Select value={workspace?.id ?? ''} onValueChange={onWorkspace}>
                  <SelectTrigger aria-label="Repository" className={chip}><SelectValue placeholder="Repository" /></SelectTrigger>
                  <SelectContent>
                    {workspaces.map((item) => <SelectItem key={item.id} value={item.id}>{item.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              )}
              {workspace && (
                <span title={workspace.base_branch} className="inline-flex min-w-0 items-center gap-1 text-ui-sm text-muted">
                  <GitBranch aria-hidden className="size-3.5 shrink-0" />
                  from <code className="min-w-0 truncate font-code">{workspace.base_branch}</code>
                </span>
              )}
              {agents === null ? (
                <span className={cn(chip, 'inline-flex items-center text-muted')}>Loading agents…</span>
              ) : (
                <Select value={harness} onValueChange={onAgent}>
                  <SelectTrigger aria-label="Agent" className={chip}><SelectValue placeholder="Agent" /></SelectTrigger>
                  <SelectContent>
                    {offered.map((agent) => <SelectItem key={agent.name} value={agent.name}>{agentLabel(agent, agent.name)}</SelectItem>)}
                    <SelectItem value={customAgent}>{customAgent}</SelectItem>
                  </SelectContent>
                </Select>
              )}
            </div>
            {lastSession !== 'none' && (
              <p className="text-ui-sm text-muted">{lastSession === 'repository' ? 'Repository from your last session.' : 'Same repository and agent as your last session.'}</p>
            )}
          </div>
          <Collapsible>
            <CollapsibleTrigger>
              <span className="font-medium">More options</span>
              {harness && <span className="min-w-0 truncate text-muted">· {modeLabel(mode)}</span>}
            </CollapsibleTrigger>
            <CollapsibleContent className="flex flex-col gap-4 pt-2">{options}</CollapsibleContent>
          </Collapsible>
          {notes}
        </form>
        {failure && <div className="max-h-[40dvh] overflow-y-auto">{failure}</div>}
        {launch}
      </DialogContent>
    </Dialog>
  )
}

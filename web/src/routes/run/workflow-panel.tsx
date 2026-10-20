import { useMemo, useRef } from 'react'
import { X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/status-dot'
import { GitFlow } from '@/components/workflow/git-flow'
import { WorkflowTree } from '@/components/workflow/workflow-tree'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { countChecks, usePullRequest } from '@/routes/diff/publish-pr'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { usePrimaryAction, type RunNavigation } from '@/routes/run/header'
import { openAgent } from '@/routes/run/subagent-cards'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { noAgents } from '@/store/subagents'
import { deriveRunWorkflow, runFacts, shareTree, workflowLabel, workflowTone, type WorkflowNode } from '@/store/workflow'

export const workflowPanelID = 'run-workflow'

/** The run's workflow tree, rebuilt from the session's subagent index and the run's state. */
function useRunWorkflow(run: RunRecord): WorkflowNode {
  const shown = useRunPresentation(run)
  const agents = useStore((s) => s.acpSessions[run.id]?.agents ?? noAgents)
  const previous = useRef<WorkflowNode | undefined>(undefined)
  return useMemo(() => {
    const next = shareTree(previous.current, deriveRunWorkflow(run.id, runFacts(run, shown, runLabel(run)), agents))
    previous.current = next
    return next
  }, [run, shown, agents])
}

function runningAgents(node: WorkflowNode): number {
  return node.children.reduce((sum, child) => sum + (child.agentId && child.state === 'working' ? 1 : 0) + runningAgents(child), 0)
}

/** Beside the run's views: the state, then what it is busy with. */
export function WorkflowChip({ run, open, onToggle }: { run: RunRecord; open: boolean; onToggle: (opener: HTMLElement) => void }) {
  const tree = useRunWorkflow(run)
  const checks = usePullRequest(run, false).feedback?.checks
  const { state } = tree
  const running = runningAgents(tree)
  const parts = [workflowLabel[state]]
  if (state === 'waiting-ci' && checks && checks.length > 0) parts.push(`${countChecks(checks).passing} of ${checks.length} checks`)
  else if (running > 0) parts.push(`${running} ${running === 1 ? 'subagent' : 'subagents'}`)
  const text = parts.join(' · ')
  return (
    <Button
      variant="ghost"
      size="sm"
      aria-expanded={open}
      aria-controls={workflowPanelID}
      aria-label={`Workflow: ${text}`}
      hint={open ? 'Hide the workflow' : 'Show the workflow'}
      className="ml-1 min-w-0 shrink"
      onClick={(event) => onToggle(event.currentTarget)}
    >
      <StatusDot tone={workflowTone[state]} pulse={state === 'working'} />
      <span className="min-w-0 truncate">{text}</span>
    </Button>
  )
}

export function WorkflowPanel({ run, view, agent, nav, onClose, className }: {
  run: RunRecord
  view: RunView
  agent: AgentTerminal
  nav: RunNavigation
  onClose: () => void
  className?: string
}) {
  const tree = useRunWorkflow(run)
  const primary = usePrimaryAction(run, view, agent, nav)
  const select = (node: WorkflowNode) => {
    if (node.agentId) {
      openAgent(run.id, node.agentId)
      return true
    }
    if (node.kind === 'request') {
      if (primary) primary.act()
      else nav.reveal('details-needs-you')
      return true
    }
    return false
  }
  return (
    <div className={cn('flex min-h-0 min-w-0 flex-col', className)}>
      <div className="flex h-8 shrink-0 items-center gap-2 border-b border-seam pr-1 pl-3">
        <h2 className="min-w-0 flex-1 truncate text-ui-sm font-medium">Workflow</h2>
        <Button variant="ghost" size="icon-sm" label="Hide the workflow" onClick={onClose}>
          <X />
        </Button>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-2">
        <WorkflowTree root={tree} label="Run workflow" onSelect={select} />
        {run.branch && <GitFlow runId={run.id} compact />}
      </div>
    </div>
  )
}

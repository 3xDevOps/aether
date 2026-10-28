import { useMemo, useRef } from 'react'
import { X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/status-dot'
import { GitFlow } from '@/components/workflow/git-flow'
import { WorkflowTree } from '@/components/workflow/workflow-tree'
import { runLabel, stateName, toneOf } from '@/lib/status'
import { cn } from '@/lib/utils'
import { countChecks, usePullRequest } from '@/routes/diff/publish-pr'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { usePrimaryAction, type RunNavigation } from '@/routes/run/header'
import { openAgent } from '@/routes/run/subagent-cards'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { useChildRunFacts, useNeedsYouRuns, useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { noAgents } from '@/store/subagents'
import { deriveRunWorkflow, runFacts, shareTree, type WorkflowNode } from '@/store/workflow'

export const workflowPanelID = 'run-workflow'

function useRunWorkflow(run: RunRecord): WorkflowNode {
  const shown = useRunPresentation(run)
  const ids = useMemo(() => [run.id], [run.id])
  const asks = useNeedsYouRuns(ids).has(run.id)
  const agents = useStore((s) => s.acpSessions[run.id]?.agents ?? noAgents)
  const childRuns = useChildRunFacts(ids)
  const previous = useRef<WorkflowNode | undefined>(undefined)
  return useMemo(() => {
    const next = shareTree(previous.current, deriveRunWorkflow(run.id, runFacts(run, shown, runLabel(run), asks), agents, childRuns))
    previous.current = next
    return next
  }, [run, shown, asks, agents, childRuns])
}

export function WorkflowChip({ run, open, onToggle }: { run: RunRecord; open: boolean; onToggle: (opener: HTMLElement) => void }) {
  const tree = useRunWorkflow(run)
  const shown = useRunPresentation(run)
  const checks = usePullRequest(run, false).feedback?.checks
  const { state } = tree
  const label = stateName(shown)
  const running = tree.counts?.running ?? 0
  const busy = state === 'waiting-ci' && checks && checks.length > 0
    ? `${countChecks(checks).passing} of ${checks.length} checks`
    : running > 0 ? `${running} ${running === 1 ? 'agent' : 'agents'} running` : ''
  const text = busy ? `${label} · ${busy}` : label
  return (
    <Button
      variant="ghost"
      size="sm"
      aria-expanded={open}
      aria-controls={workflowPanelID}
      aria-label={`Workflow: ${text}`}
      hint={open ? 'Hide the workflow' : 'Show the workflow'}
      className="ml-1 shrink-0"
      onClick={(event) => onToggle(event.currentTarget)}
    >
      <StatusDot tone={toneOf(shown)} pulse={state === 'working'} />
      <span>{label}</span>
      {busy && <span className="hidden @min-[480px]/views:inline">· {busy}</span>}
    </Button>
  )
}

export function WorkflowPanel({ run, view, agent, nav, onClose, onLeave, className }: {
  run: RunRecord
  view: RunView
  agent: AgentTerminal
  nav: RunNavigation
  onClose: () => void
  /** Set when the panel is a sheet, which would cover the inspector or request it opens. */
  onLeave?: () => void
  className?: string
}) {
  const tree = useRunWorkflow(run)
  const primary = usePrimaryAction(run, view, agent, nav)
  const navigate = useStore((s) => s.navigate)
  const select = (node: WorkflowNode) => {
    if (node.kind === 'run' && node.runId && node.runId !== run.id) {
      navigate('run', { runId: node.runId })
      return true
    }
    if (node.agentId) {
      onLeave?.()
      openAgent(run.id, node.agentId)
      return true
    }
    if (node.kind === 'request') {
      onLeave?.()
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
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-2 [&>*]:shrink-0">
        <WorkflowTree root={tree} label="Run workflow" onSelect={select} />
        {run.branch && view !== 'changes' && <GitFlow runId={run.id} compact />}
      </div>
    </div>
  )
}

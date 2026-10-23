import { useState } from 'react'
import { GitPullRequest } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/status-dot'
import { PRFeedback } from '@/routes/diff/publish-feedback'
import { stateLabel, summarizePR, usePullRequest } from '@/routes/diff/publish-pr'
import type { ScopedRecord } from '@/routes/diff/repos'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useCapability } from '@/store/hooks'

/** One repo's pull request: link, state, checks, and the feedback panel. Nothing until the repo has one. */
export function RepoPR({ run, agent, agentName }: { run: ScopedRecord; agent: AgentTerminal; agentName: string }) {
  const caps = useCapability()
  const watch = (run.status === 'running' || run.status === 'needs-attention') && caps.hasMethod('run.git.status') && caps.hasMethod('run.pr.feedback')
  const tracked = usePullRequest(run, watch)
  const [open, setOpen] = useState(false)
  const feedback = tracked.feedback
  const pr = feedback?.pull_request
  if (!feedback || !pr) return null
  const { tone, text } = summarizePR(pr, feedback)
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
      <a
        className="flex items-center gap-1 rounded-control underline-offset-2 hover:text-text hover:underline coarse:h-11"
        href={pr.url}
        target="_blank"
        rel="noreferrer"
        aria-label={`Pull request #${pr.number} on GitHub`}
      >
        <GitPullRequest aria-hidden className="size-3.5" />
        <span className="tabular-nums">#{pr.number}</span>
      </a>
      <span>{stateLabel(pr)}</span>
      {watch && (
        <Button variant="ghost" size="sm" hint="Checks and review comments" aria-haspopup="dialog" onClick={() => setOpen(true)}>
          <StatusDot tone={tone} pulse={tone === 'working'} />
          {text}
        </Button>
      )}
      {watch && <PRFeedback run={run} agent={agent} agentName={agentName} tracked={tracked} open={open} onOpenChange={setOpen} />}
    </span>
  )
}

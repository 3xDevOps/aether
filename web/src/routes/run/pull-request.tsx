import { useState } from 'react'
import { GitPullRequest } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import type { RunPRCheck, RunPullRequest as PullRequest } from '@/lib/types'
import { PRFeedback } from '@/routes/diff/publish-feedback'
import { countChecks, usePullRequest } from '@/routes/diff/publish-pr'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** `short` is what a narrow header keeps of `text` beside the dot. */
function summarize(pr: PullRequest, checks: RunPRCheck[]): { tone: Tone; text: string; short?: string } {
  if (pr.state === 'merged') return { tone: 'done', text: 'Merged' }
  if (pr.state === 'closed') return { tone: 'neutral', text: 'Closed' }
  const counts = countChecks(checks)
  if (counts.failing) return { tone: 'failed', text: `${counts.failing} failing`, short: String(counts.failing) }
  if (counts.running) return { tone: 'working', text: 'Checks running' }
  if (counts.passing) return { tone: 'done', text: 'Checks passed' }
  return { tone: 'neutral', text: 'No checks' }
}

/** The run's pull request once it has one: its number, linking to GitHub, and
 * the state of its checks, which opens the checks and review comments. It is
 * read through the run's container, so a run that is no longer live keeps the
 * link and drops the state nothing can refresh. */
export function RunPullRequest({ run, agent, agentName }: { run: RunRecord; agent: AgentTerminal; agentName: string }) {
  const caps = useCapability()
  const watch = (run.status === 'running' || run.status === 'needs-attention') && caps.hasMethod('run.git.status') && caps.hasMethod('run.pr.feedback')
  const tracked = usePullRequest(run, watch)
  const [open, setOpen] = useState(false)
  const pr = tracked.feedback?.pull_request
  if (!pr) return null
  const { tone, text, short } = summarize(pr, tracked.feedback?.checks ?? [])
  return (
    <div role="group" aria-label="Pull request" className="ml-auto flex shrink-0 items-center gap-1 pl-2 text-ui-sm">
      <a
        className="flex items-center gap-1 rounded-control text-muted underline-offset-2 hover:text-text hover:underline coarse:h-11 coarse:px-1"
        href={pr.url}
        target="_blank"
        rel="noreferrer"
        aria-label={`Pull request #${pr.number} on GitHub`}
      >
        <GitPullRequest aria-hidden className="size-3.5" />
        <span className="tabular-nums">#{pr.number}</span>
      </a>
      {watch && (
        <>
          <Button variant="ghost" size="sm" hint="Checks and review comments" aria-haspopup="dialog" onClick={() => setOpen(true)}>
            <StatusDot tone={tone} pulse={tone === 'working'} />
            {short && <span aria-hidden className="tabular-nums @min-[480px]/views:hidden">{short}</span>}
            <span className="sr-only @min-[480px]/views:not-sr-only">{text}</span>
          </Button>
          <PRFeedback run={run} agent={agent} agentName={agentName} tracked={tracked} open={open} onOpenChange={setOpen} />
        </>
      )}
    </div>
  )
}

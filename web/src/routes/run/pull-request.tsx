import { useState } from 'react'
import type * as React from 'react'
import { AskAssistantButton } from '@/components/assistant/ask'
import { Eye, GitPullRequest, Sparkles } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/status-dot'
import { PRFeedback } from '@/routes/diff/publish-feedback'
import { summarizePR, usePullRequest } from '@/routes/diff/publish-pr'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useRepoPullRequests } from '@/routes/run/repo-pull-requests'
import { pullRequestTarget, isLive, watchRoute } from '@/lib/watches'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/**
 * `status` and `panel` are separate because the row `status` sits in leaves the header while the
 * composer has focus on a phone, and a send waiting for control in `panel` has to outlive it.
 * Both are read through the run's container, so a run that is no longer live keeps the link and
 * drops the state nothing can refresh.
 */
export function useRunPullRequest(run: RunRecord, agent: AgentTerminal, agentName: string): { status: React.ReactNode; panel: React.ReactNode } {
  const caps = useCapability()
  const watch = (run.status === 'running' || run.status === 'needs-attention') && caps.hasMethod('run.git.status') && caps.hasMethod('run.pr.feedback')
  const multiRepo = (run.repos?.length ?? 0) > 1
  const tracked = usePullRequest(run, watch && !multiRepo)
  const repos = useRepoPullRequests(run, agent, agentName, watch && multiRepo)
  const [open, setOpen] = useState(false)
  const url = tracked.feedback?.pull_request?.url
  const watched = useStore((s) => {
    const target = url ? pullRequestTarget(url) : null
    return target && Object.values(s.watches).find((w) => w.target.kind === 'pr' && w.target.repo === target.repo && w.target.number === target.number && isLive(w))
  })
  const navigate = useStore((s) => s.navigate)
  const openWatchDialog = useStore((s) => s.openWatchDialog)
  if (repos) return repos
  const feedback = tracked.feedback
  const pr = feedback?.pull_request
  if (!feedback || !pr) return { status: null, panel: null }
  const { tone, text, short } = summarizePR(pr, feedback)
  const failing = tone === 'failed'
  return {
    status: (
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
        {pr.state !== 'merged' && pr.state !== 'closed' && (
          <AskAssistantButton
            variant="ghost"
            size="icon-sm"
            label={`Ask the assistant about pull request #${pr.number}`}
            hint={failing ? 'Fix this pull request' : 'Review and merge when checks pass'}
            ask={{
              draft: failing ? 'Fix this pull request' : 'Review and merge when checks pass',
              attachments: [{ kind: 'pr', workspace_id: run.workspace_id, run_id: run.id, pr: pr.number, repository: pr.repository, label: `PR #${pr.number}` }],
            }}
          >
            <Sparkles />
          </AskAssistantButton>
        )}
        {watched
          ? (
            <Button variant="ghost" size="sm" hint="Open the watch on this pull request" onClick={() => navigate(watchRoute(watched.id).name, watchRoute(watched.id).params)}>
              <Eye aria-hidden />
              <span className="sr-only @min-[480px]/views:not-sr-only">Watching PR</span>
            </Button>
          )
          : caps.hasMethod('watch.create') && pullRequestTarget(pr.url) && (
            <Button variant="ghost" size="sm" hint="Get notified when checks finish, a review lands or it merges" aria-haspopup="dialog" onClick={() => openWatchDialog(pullRequestTarget(pr.url, run.id)!)}>
              <Eye aria-hidden />
              <span className="sr-only @min-[480px]/views:not-sr-only">Watch PR</span>
            </Button>
          )}
        {watch && (
          <Button variant="ghost" size="sm" hint="Checks and review comments" aria-haspopup="dialog" onClick={() => setOpen(true)}>
            <StatusDot tone={tone} pulse={tone === 'working'} />
            {short && <span aria-hidden className="tabular-nums @min-[480px]/views:hidden">{short}</span>}
            <span className="sr-only @min-[480px]/views:not-sr-only">{text}</span>
          </Button>
        )}
      </div>
    ),
    panel: watch && <PRFeedback run={run} agent={agent} agentName={agentName} tracked={tracked} open={open} onOpenChange={setOpen} />,
  }
}

import { useState } from 'react'
import type * as React from 'react'
import { GitPullRequest } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { PRFeedback } from '@/routes/diff/publish-feedback'
import { stateLabel, summarizePR, usePullRequestList } from '@/routes/diff/publish-pr'
import { useRepoRuns } from '@/routes/diff/repos'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunRecord } from '@/store/runs'

const severity: Partial<Record<Tone, number>> = { failed: 3, working: 2, neutral: 1 }

export function useRepoPullRequests(
  run: RunRecord,
  agent: AgentTerminal,
  agentName: string,
  watch: boolean,
): { status: React.ReactNode; panel: React.ReactNode } | null {
  const repos = useRepoRuns(run)
  const tracked = usePullRequestList(repos.map((entry) => entry.run), watch)
  const [open, setOpen] = useState(false)
  const [panel, setPanel] = useState<string | null>(null)
  if (repos.length === 0) return null
  const found = repos.flatMap((entry, index) => {
    const feedback = tracked[index]?.feedback
    const pr = feedback?.pull_request
    return feedback && pr ? [{ ...entry, pr, tracked: tracked[index]!, summary: summarizePR(pr, feedback) }] : []
  })
  if (found.length === 0) return { status: null, panel: null }
  const tone = found.reduce<Tone>((worst, entry) => ((severity[entry.summary.tone] ?? 0) > (severity[worst] ?? 0) ? entry.summary.tone : worst), 'done')
  const label = `${found.length} ${found.length === 1 ? 'PR' : 'PRs'}`
  return {
    status: (
      <div role="group" aria-label="Pull requests" className="ml-auto flex shrink-0 items-center gap-1 pl-2 text-ui-sm">
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <Button variant="ghost" size="sm" hint="Pull requests in each repo">
              <GitPullRequest aria-hidden className="size-3.5" />
              <StatusDot tone={tone} pulse={tone === 'working'} />
              <span aria-hidden className="tabular-nums @min-[480px]/views:hidden">{found.length}</span>
              <span className="sr-only @min-[480px]/views:not-sr-only">{label}</span>
            </Button>
          </PopoverTrigger>
          <PopoverContent align="end" className="flex flex-col gap-1 p-1" aria-label="Pull requests by repo">
            {found.map(({ repo, pr, summary }) => (
              <div key={repo.workspace_id} className="flex items-center gap-2 rounded-control px-2 py-1.5 coarse:min-h-11">
                <StatusDot tone={summary.tone} pulse={summary.tone === 'working'} />
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium">{repo.name}</p>
                  <p className="truncate text-ui-sm text-muted">
                    <a
                      className="underline-offset-2 hover:text-text hover:underline"
                      href={pr.url}
                      target="_blank"
                      rel="noreferrer"
                      aria-label={`Pull request #${pr.number} in ${repo.name} on GitHub`}
                    >
                      #{pr.number}
                    </a>
                    {' · '}{stateLabel(pr)}{' · '}{summary.text}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={`Checks and review comments for ${repo.name}`}
                  aria-haspopup="dialog"
                  onClick={() => { setOpen(false); setPanel(repo.workspace_id) }}
                >
                  Feedback
                </Button>
              </div>
            ))}
          </PopoverContent>
        </Popover>
      </div>
    ),
    panel: found.map(({ repo, run: scoped, tracked: entry }) => (
      <PRFeedback
        key={repo.workspace_id}
        run={scoped}
        agent={agent}
        agentName={agentName}
        tracked={entry}
        open={panel === repo.workspace_id}
        onOpenChange={(next) => setPanel(next ? repo.workspace_id : null)}
      />
    )),
  }
}

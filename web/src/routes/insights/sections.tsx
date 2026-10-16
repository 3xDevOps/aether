import type { ReactNode } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardControls, CardTitle } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { money } from '@/lib/format'
import type { InsightsResult, InsightsRun } from '@/lib/types'
import { useStore } from '@/store'
import { agentLabel, duration, outcome, plural, sinceLabel, successRate, versus, type Rate } from '@/routes/insights/model'

export function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h2">{title}</SectionLabel>
      {children}
    </section>
  )
}

function Tile({ label, value, children }: { label: string; value: string; children: ReactNode }) {
  return (
    <article aria-label={label} className="flex min-w-0 flex-col gap-1 rounded-panel border border-seam p-3">
      <h3 className="text-ui-sm text-muted">{label}</h3>
      <p className="text-title tabular-nums">{value}</p>
      <div className="flex flex-col gap-0.5 text-ui-sm text-muted">{children}</div>
    </article>
  )
}

function rateWords(rate: Rate): string {
  return rate.percent === null ? 'No runs ended' : `${rate.percent}% (${rate.finished} of ${rate.ended})`
}

export function Tiles({ result, before }: { result: InsightsResult; before: string }) {
  const { totals, previous, swarms, pull_requests: prs, coverage } = result
  const rate = successRate(totals)
  const was = successRate(previous)
  return (
    <div className="grid grid-cols-1 gap-2 min-[30rem]:grid-cols-2 lg:grid-cols-4">
      <Tile label="Runs finished" value={totals.finished.toLocaleString()}>
        <span>{versus(totals.finished, previous.finished, before)}</span>
        <span>{plural(totals.launched, 'run')} launched</span>
      </Tile>
      <Tile label="Success rate" value={rate.percent === null ? '—' : `${rate.percent}%`}>
        <span>{rate.percent === null ? 'No runs ended' : `${rate.finished} finished of ${plural(rate.ended, 'ended run')}`}</span>
        <span>{was.percent === null ? `No runs ended ${before}` : `${rateWords(was)} ${before}`}</span>
      </Tile>
      <Tile label="Swarms completed" value={swarms.completed.toLocaleString()}>
        <span>{plural(swarms.launched, 'swarm')} launched</span>
        <span>{plural(swarms.agents, 'agent')}, {plural(swarms.tasks_accepted, 'task')} accepted</span>
      </Tile>
      <Tile label="Pull requests opened" value={coverage.pull_requests_since ? prs.opened.toLocaleString() : '—'}>
        {coverage.pull_requests_since ? (
          <>
            <span>{prs.merged.toLocaleString()} merged</span>
            <span>Through Aether, recorded since {sinceLabel(coverage.pull_requests_since)}</span>
          </>
        ) : (
          <span>Aether counts the pull requests its runs open from the first one on. None recorded yet.</span>
        )}
      </Tile>
    </div>
  )
}

function pullRequestName(url: string): string {
  const number = /\/pull\/(\d+)/.exec(url)?.[1]
  return number ? `PR #${number}` : 'Pull request'
}

function FinishedRow({ run, repository, agent }: { run: InsightsRun; repository: string; agent: string }) {
  const navigate = useStore((s) => s.navigate)
  const { label, tone } = outcome(run)
  const title = run.title || 'Untitled run'
  return (
    <Card>
      <div className="flex min-w-0 items-start justify-between gap-2">
        <CardTitle onOpen={() => navigate('run', { runId: run.run_id })}>{title}</CardTitle>
        <RelativeTime at={run.finished_at} title={run.finished_at} className="shrink-0 text-ui-sm tabular-nums text-muted" />
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-ui-sm text-muted">
        <Badge tone={tone}>{label}</Badge>
        <span>{repository}</span>
        <span aria-hidden>·</span>
        <span>{agent}</span>
        <span aria-hidden>·</span>
        <span className="tabular-nums">{duration(run.duration_seconds)}</span>
        {run.pr_url && (
          <CardControls>
            <a href={run.pr_url} target="_blank" rel="noreferrer" className="text-accent hover:underline">
              {pullRequestName(run.pr_url)}{run.pr_state && ` (${run.pr_state})`}
            </a>
          </CardControls>
        )}
      </div>
    </Card>
  )
}

export function FinishedList({
  runs,
  capped,
  filter,
  onClear,
  repositoryOf,
  agentNames,
}: {
  runs: InsightsRun[]
  capped: boolean
  filter: string | null
  onClear: () => void
  repositoryOf: (id: string) => string
  agentNames: Record<string, string>
}) {
  return (
    <Section title="Finished work">
      {filter && (
        <p role="status" className="flex flex-wrap items-center gap-2 text-ui-sm text-muted">
          <span>Showing runs that ended {filter.startsWith('the week') ? 'in' : 'on'} {filter}.</span>
          <Button variant="link" onClick={onClear}>Show every day</Button>
        </p>
      )}
      {runs.length === 0 ? (
        <p className="text-ui text-muted">{filter ? 'No runs ended then.' : 'No runs ended in this period.'}</p>
      ) : (
        <ul className="flex min-w-0 flex-col gap-2">
          {runs.map((run) => (
            <li key={run.run_id}>
              <FinishedRow run={run} repository={repositoryOf(run.workspace_id)} agent={agentLabel(run.harness, run.model, agentNames)} />
            </li>
          ))}
        </ul>
      )}
      {capped && <p className="text-ui-sm text-muted">The 50 most recent runs that ended in this period.</p>}
    </Section>
  )
}

function Bars({ rows }: { rows: { key: string; name: string; launched: number; finished: number; failed: number }[] }) {
  const max = Math.max(1, ...rows.map((r) => r.launched))
  if (rows.length === 0) return <p className="text-ui text-muted">Nothing launched.</p>
  return (
    <ul className="flex min-w-0 flex-col gap-2">
      {rows.map((row) => (
        <li key={row.key} className="flex min-w-0 flex-col gap-1">
          <div className="flex min-w-0 items-baseline justify-between gap-2 text-ui-sm">
            <span className="min-w-0 truncate text-text">{row.name}</span>
            <span className="shrink-0 tabular-nums text-muted">
              {row.launched} launched · {row.finished} finished · {row.failed} failed
            </span>
          </div>
          <svg aria-hidden viewBox="0 0 100 4" preserveAspectRatio="none" className="h-1.5 w-full">
            <rect width="100" height="4" rx="1" className="fill-chrome" />
            <rect width={(row.launched / max) * 100} height="4" rx="1" className="fill-icon-faint" />
            <rect width={(row.finished / max) * 100} height="4" rx="1" className="fill-state-done" />
          </svg>
        </li>
      ))}
    </ul>
  )
}

export function Breakdown({ result, agentNames }: { result: InsightsResult; agentNames: Record<string, string> }) {
  return (
    <div className="grid min-w-0 grid-cols-1 gap-6 md:grid-cols-2">
      <Section title="By repository">
        <Bars rows={result.by_workspace.map((w) => ({ ...w, key: w.workspace_id }))} />
      </Section>
      <Section title="By agent">
        <Bars rows={result.by_agent.map((a) => ({ ...a, key: `${a.harness}/${a.model}`, name: agentLabel(a.harness, a.model, agentNames) }))} />
      </Section>
    </div>
  )
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-w-0 flex-col">
      <dt className="text-ui-sm text-muted">{label}</dt>
      <dd className="text-ui font-medium tabular-nums">{value}</dd>
    </div>
  )
}

export function Time({ result }: { result: InsightsResult }) {
  const d = result.durations
  const since = result.coverage.state_times_since
  return (
    <Section title="Time">
      {d.runs_measured === 0 ? (
        <p className="text-ui text-muted">No runs ended in this period, so there is nothing to measure.</p>
      ) : (
        <>
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            <Figure label="Median run" value={duration(d.median_run_seconds)} />
            <Figure label="Slowest 10% take over" value={duration(d.p90_run_seconds)} />
            {d.state_runs_measured > 0 && (
              <>
                <Figure label="Agent working, median" value={duration(d.median_working_seconds)} />
                <Figure label="Waiting on you, median" value={duration(d.median_waiting_seconds)} />
                <Figure label="Your response time, median" value={duration(d.median_response_seconds)} />
              </>
            )}
          </dl>
          <p className="text-ui-sm text-muted">
            Run time is from start to end for {plural(d.runs_measured, 'run')} that ended in this period.
            {d.state_runs_measured < d.runs_measured && (since
              ? ` Working and waiting time is measured for ${d.state_runs_measured} of ${d.runs_measured} runs; Aether records this from ${sinceLabel(since)} on.`
              : ' Working and waiting time is not recorded yet; Aether measures it for runs from now on.')}
          </p>
        </>
      )}
    </Section>
  )
}

export function Usage({ result }: { result: InsightsResult }) {
  const navigate = useStore((s) => s.navigate)
  const { cost } = result
  const runs = cost.metered_runs + cost.unmetered_runs
  const amount = cost.unmetered_runs > 0 ? `at least ${money.format(cost.cost_usd)}` : money.format(cost.cost_usd)
  return (
    <Collapsible>
      <CollapsibleTrigger>Usage</CollapsibleTrigger>
      <CollapsibleContent>
        <div className="flex min-w-0 flex-col gap-2 px-1 pt-2">
          {cost.metered_runs === 0 ? (
            <p className="text-ui text-muted">No cost reported for {plural(runs, 'run')} (headless Claude reports cost; other agents do not).</p>
          ) : (
            <>
              <p className="text-ui">
                <span className="font-medium tabular-nums">{amount}</span>
                <span className="text-muted"> metered for {cost.metered_runs} of {plural(runs, 'run')} (headless Claude reports cost; other agents do not)</span>
              </p>
              <p className="text-ui-sm tabular-nums text-muted">
                {plural(cost.input_tokens, 'input token')} · {plural(cost.output_tokens, 'output token')}
              </p>
            </>
          )}
          <p className="text-ui-sm text-muted">
            Cost is reported, not enforced: nothing here stops a run.{' '}
            <Button variant="quiet" onClick={() => navigate('settings')}>Subscription quota is in Settings › Usage</Button>
          </p>
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

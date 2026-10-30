import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Network } from '@/components/icons'
import type { ExtraItem } from '@/components/run-actions'
import { Button } from '@/components/ui/button'
import { SectionLabel } from '@/components/ui/section-label'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { api, type Api } from '@/lib/api'
import { errorSentence, message, money } from '@/lib/format'
import { memberName } from '@/lib/member-name'
import { runLabel } from '@/lib/status'
import type { Child, ChildState, ChildUsage, RunChildrenResult } from '@/lib/types'
import { cn } from '@/lib/utils'
import { limitsText } from '@/routes/settings/children'
import { useStore } from '@/store'
import { useChildTouches } from '@/store/child-touches'
import { useCapability, useSelf } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'
import { descendantRuns } from '@/store/selectors'

const childState: Record<ChildState, { label: string; tone: Tone }> = {
  queued: { label: 'Queued', tone: 'waiting' },
  starting: { label: 'Starting', tone: 'working' },
  working: { label: 'Working', tone: 'working' },
  waiting: { label: 'Waiting', tone: 'waiting' },
  'needs-you': { label: 'Needs you', tone: 'needs-you' },
  finished: { label: 'Finished', tone: 'done' },
  failed: { label: 'Failed', tone: 'failed' },
  ended: { label: 'Ended without a report', tone: 'ended' },
  cancelled: { label: 'Cancelled', tone: 'closed' },
  refused: { label: 'Refused', tone: 'failed' },
}

const tokens = new Intl.NumberFormat(undefined, { notation: 'compact' })

/** Unmetered runs report no dollars, so a total over them is a floor and a total with no metered run is unknown. */
export function costText(usage: Pick<ChildUsage, 'cost_usd' | 'metered_runs' | 'unmetered_runs'>): string {
  if (usage.metered_runs === 0) return 'Cost not reported'
  if (usage.unmetered_runs === 0) return money.format(usage.cost_usd)
  return `At least ${money.format(usage.cost_usd)}: ${usage.unmetered_runs} ${usage.unmetered_runs === 1 ? 'run reports' : 'runs report'} no cost`
}

export function tokensText(usage: Pick<ChildUsage, 'input_tokens' | 'output_tokens'>): string | null {
  if (usage.input_tokens === 0 && usage.output_tokens === 0) return null
  return `${tokens.format(usage.input_tokens)} input, ${tokens.format(usage.output_tokens)} output tokens`
}

function useChildren(run: RunRecord, client: Api): { result: RunChildrenResult | null; error: string | null } {
  const readable = useCapability().hasMethod('run.children')
  // Any change to a descendant the store holds rereads the list, which also holds reservations the store never sees.
  const changed = useStore((s) => JSON.stringify([s.runs[run.id]?.last_commit, descendantRuns(s.runs, [run.id]).map((child) => [child.id, child.status_seq, child.detached])]))
  const touched = useChildTouches((s) => s.touched[run.id] ?? 0)
  const [state, setState] = useState<{ runID: string; result: RunChildrenResult | null; error: string | null }>({ runID: run.id, result: null, error: null })
  useEffect(() => {
    if (!readable) return
    let live = true
    client.runChildren(run.id).then(
      (result) => live && setState({ runID: run.id, result, error: null }),
      (err: unknown) => live && setState((current) => ({ runID: run.id, result: current.runID === run.id ? current.result : null, error: errorSentence(err) })),
    )
    return () => {
      live = false
    }
  }, [client, readable, run.id, changed, touched])
  const shown = state.runID === run.id ? state : { result: null, error: null }
  return shown
}

function Account({ child }: { child: Child }) {
  const requested = useStore((s) => s.runs[child.run_id]?.requested_account_member_id)
  const name = useStore((s) => memberName(s, child.account_member_id))
  const from = useStore((s) => (requested ? memberName(s, requested) : ''))
  const owner = useStore((s) => memberName(s, child.owner_member_id))
  return (
    <span title={`Owned by ${owner}`}>
      On {name}&apos;s account{from && ` (fallback from ${from})`}
    </span>
  )
}

function ChildRow({ child }: { child: Child }) {
  const navigate = useStore((s) => s.navigate)
  const { label, tone } = childState[child.state]
  const started = child.state !== 'queued' && child.state !== 'cancelled' && child.state !== 'refused'
  return (
    <li className="flex min-w-0 flex-col gap-1 py-2 first:pt-0 last:pb-0">
      <div className="flex min-w-0 items-center gap-2">
        <StatusDot tone={tone} label={label} />
        <span className="min-w-0 flex-1 truncate text-ui font-medium">{child.purpose}</span>
        {child.detached && <Badge>Detached</Badge>}
      </div>
      <p className="pl-4 text-ui-sm text-muted">
        {[label, [child.harness, child.model].filter(Boolean).join(' · ')].join(' · ')} · <Account child={child} /> · {child.cost_usd === undefined ? 'Cost not reported' : money.format(child.cost_usd)}
      </p>
      {child.report && <p className="pl-4 text-ui-sm break-words">{child.report.summary}</p>}
      {child.error && <p className="pl-4 text-ui-sm break-words text-state-failed">{child.error}</p>}
      <div className="flex gap-1 pl-3">
        {started && <Button size="sm" variant="ghost" onClick={() => navigate('run', { runId: child.run_id })}>Open</Button>}
      </div>
    </li>
  )
}

function Usage({ result, root }: { result: RunChildrenResult; root: boolean }) {
  const { usage, limits, children } = result
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const busy = children.filter((child) => child.state === 'starting' || child.state === 'working').length
  const queued = children.filter((child) => child.state === 'queued').length
  return (
    <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-2 gap-y-0.5 text-ui-sm">
      <dt className="text-muted">Cost</dt>
      <dd>{costText(usage)} <span className="text-muted">over {usage.runs} {usage.runs === 1 ? 'run' : 'runs'}, this one included</span></dd>
      {usage.by_account.length > 1 && (
        <>
          <dt className="text-muted">By account</dt>
          <dd>{usage.by_account.map((entry) => `${memberName({ members, mergedInto }, entry.account_member_id)} ${money.format(entry.cost_usd)}`).join(', ')}</dd>
        </>
      )}
      {tokensText(usage) && (
        <>
          <dt className="text-muted">Tokens</dt>
          <dd>{tokensText(usage)}</dd>
        </>
      )}
      <dt className="text-muted">Limits</dt>
      <dd>{root ? `${busy} of ${limits.max_live} busy, ${queued} queued, ${children.length} started` : limitsText(limits)}</dd>
    </dl>
  )
}

function RunLink({ runID }: { runID: string }) {
  const navigate = useStore((s) => s.navigate)
  const label = useStore((s) => (s.runs[runID] ? runLabel(s.runs[runID]) : runID))
  return (
    <Button variant="link" size="sm" className="h-auto min-w-0 max-w-60 p-0" onClick={() => navigate('run', { runId: runID })}>
      <span className="truncate">{label}</span>
    </Button>
  )
}

/** Where a child run sits: the run that started it and the top of its tree. */
export function RunLineage({ run }: { run: RunRecord }) {
  if (!run.parent_run_id) return null
  const root = run.root_run_id && run.root_run_id !== run.parent_run_id ? run.root_run_id : undefined
  return (
    <nav aria-label="Run lineage" className="flex min-w-0 items-center gap-1.5 border-b border-seam px-4 py-1 text-ui-sm text-muted">
      {root && (
        <>
          <RunLink runID={root} />
          <span aria-hidden>▸</span>
        </>
      )}
      <RunLink runID={run.parent_run_id} />
      <span aria-hidden>▸</span>
      <span className="shrink-0 text-text">This child run</span>
      {run.detached && <Badge>Detached</Badge>}
    </nav>
  )
}

/** More → Allow child runs and Turn off child runs: only the owner allows; the owner or an admin turns them off, whether the run allowed them itself or inherits it. */
export function useChildRunsItems(run: RunRecord, client: Api = api): ExtraItem[] {
  const cap = useCapability()
  const self = useSelf()
  if (!cap.hasMethod('run.children.allow') || isTerminal(run.status)) return []
  const owner = self.id === run.member_id
  const item = (next: 'on' | 'off'): ExtraItem => ({
    id: `child-runs-${next}`,
    label: next === 'on' ? 'Allow child runs' : 'Turn off child runs',
    description: next === 'on' ? 'The agent may start other agents as child runs.' : 'New child runs are refused; running ones continue.',
    Icon: Network,
    onSelect: () => {
      client.runChildrenAllow(run.id, next).then(
        () => toast.success(next === 'on' ? 'Child runs allowed' : 'Child runs turned off'),
        (err: unknown) => toast.error(`${next === 'on' ? 'Allow child runs' : 'Turn off child runs'} failed: ${message(err)}`),
      )
    },
  })
  const items: ExtraItem[] = []
  if (owner && run.children !== 'on') items.push(item('on'))
  if ((owner || self.role === 'admin') && run.children !== 'off') items.push(item('off'))
  return items
}

export function ChildRuns({ run, inset, client = api }: { run: RunRecord; inset: boolean; client?: Api }) {
  const { result, error } = useChildren(run, client)
  if (!result?.children.length && !error) return null
  return (
    <section aria-label="Child runs" className={cn('flex flex-col gap-2 border-b border-seam py-3 last:border-b-0', inset && 'px-4')}>
      <SectionLabel as="h2" className="flex items-center gap-1.5">
        Child runs
        {result && <span className="text-text tabular-nums">{result.children.length}</span>}
      </SectionLabel>
      {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
      {result && (
        <>
          <ul aria-label={`Child runs of ${runLabel(run)}`} className="flex flex-col divide-y divide-seam">
            {result.children.map((child) => (
              <ChildRow key={child.run_id} child={child} />
            ))}
          </ul>
          <Usage result={result} root={result.root_run_id === run.id} />
        </>
      )}
    </section>
  )
}

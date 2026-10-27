import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { StatusDot } from '@/components/ui/status-dot'
import { api, type Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { runLabel } from '@/lib/status'
import type { WaitCondition } from '@/lib/types'
import { cn } from '@/lib/utils'
import { checkOverdue, countsText, deliveryOf, noChecks, orderWaits, prURL, waitStateLabel, waitTone, waitVerb } from '@/lib/waits'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

function Target({ wait }: { wait: WaitCondition }) {
  const navigate = useStore((s) => s.navigate)
  const run = useStore((s) => (wait.run_id ? s.runs[wait.run_id] : undefined))
  const url = prURL(wait)
  if (url) {
    return <a href={url} target="_blank" rel="noreferrer" className="min-w-0 truncate text-accent underline-offset-2 hover:underline">{wait.repo}#{wait.number}</a>
  }
  if (wait.run_id) {
    return (
      <Button variant="link" size="sm" className="h-auto min-w-0 p-0 text-ui" onClick={() => navigate('run', { runId: wait.run_id! })}>
        <span className="truncate">{run ? runLabel(run) : wait.run_id}</span>
      </Button>
    )
  }
  return <Code>{wait.spec}</Code>
}

function Fact({ term, children, tone }: { term: string; children: ReactNode; tone?: 'failed' }) {
  return (
    <div className="contents">
      <dt className="text-muted">{term}</dt>
      <dd className={cn('min-w-0 break-words', tone === 'failed' && 'text-state-failed')}>{children}</dd>
    </div>
  )
}

function WaitRow({ wait, runID, mayCancel, client }: { wait: WaitCondition; runID: string; mayCancel: boolean; client: Api }) {
  const now = useClock()
  const [cancelling, setCancelling] = useState(false)
  const verified = wait.source_checked_at ?? wait.last_checked_at
  const delivery = deliveryOf(wait)
  const overdue = checkOverdue(wait, now)
  const failures = wait.consecutive_failures
  const checks = wait.observed.checks
  const cancel = () => {
    setCancelling(true)
    client.runWaitCancel(runID, wait.id)
      .then(() => client.runGet(runID))
      .then((run) => {
        useStore.getState().upsertRun(run)
        toast.success('Wait cancelled')
      }, (err: unknown) => toast.error(`Cancel failed: ${message(err)}`))
      .finally(() => setCancelling(false))
  }
  return (
    <li className="flex min-w-0 flex-col gap-1.5 py-2 first:pt-0 last:pb-0" aria-label={wait.spec}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <StatusDot tone={waitTone(wait)} label={waitStateLabel[wait.state]} />
        <span className="flex min-w-0 flex-1 items-baseline gap-1 text-ui font-medium">
          <Target wait={wait} />
          <span className="shrink-0">{waitVerb[wait.kind]}</span>
        </span>
        <Badge tone={waitTone(wait)}>
          {waitStateLabel[wait.state]}{wait.outcome && `: ${wait.outcome}`}
        </Badge>
      </div>
      {wait.summary && <p className="text-ui-sm break-words">{wait.summary}</p>}
      <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-2 gap-y-0.5 text-ui-sm">
        {wait.observed.pr_state && <Fact term="Pull request">{wait.observed.pr_state}</Fact>}
        {wait.observed.run_status && <Fact term="Run status">{wait.observed.run_status}</Fact>}
        {wait.observed.head_sha && <Fact term="Head commit"><Code title={wait.observed.head_sha}>{wait.observed.head_sha.slice(0, 8)}</Code></Fact>}
        {checks && noChecks(checks) && <Fact term="Checks">None reported yet</Fact>}
        {checks && !noChecks(checks) && (
          <>
            {checks.required_only && checks.required && <Fact term="Required checks">{countsText(checks.required)}</Fact>}
            {checks.missing && checks.missing.length > 0 && <Fact term="Not reported" tone="failed">{checks.missing.join(', ')}</Fact>}
            <Fact term={checks.required_only ? 'All checks' : 'Checks'}>{countsText(checks)}</Fact>
          </>
        )}
        <Fact term="Last verified">{verified ? <RelativeTime at={verified} /> : 'Not yet'}</Fact>
        {wait.state === 'pending' && wait.next_check_at && (
          <Fact term="Next check" tone={overdue ? 'failed' : undefined}>
            {overdue ? <>Overdue since <RelativeTime at={wait.next_check_at} /></> : <RelativeTime at={wait.next_check_at} />}
          </Fact>
        )}
        {failures > 0 && (
          <Fact term="Failures" tone="failed">
            {failures} in a row{wait.last_error && `: ${wait.last_error}`}
            {wait.last_error_at && <span className="text-muted"> · <RelativeTime at={wait.last_error_at} /></span>}
          </Fact>
        )}
        {failures === 0 && wait.last_error && <Fact term="Last error" tone="failed">{wait.last_error}</Fact>}
        {wait.resolved_at && <Fact term="Resolved"><RelativeTime at={wait.resolved_at} /></Fact>}
        {delivery && (
          <Fact term="Delivery" tone={delivery.tone === 'failed' ? 'failed' : undefined}>
            {delivery.text}{wait.delivered_at && <> <RelativeTime at={wait.delivered_at} /></>}
          </Fact>
        )}
        {wait.ended_cause && <Fact term="Ended">{wait.ended_cause}</Fact>}
        {wait.state === 'pending' && wait.expires_at && <Fact term="Gives up"><RelativeTime at={wait.expires_at} /></Fact>}
      </dl>
      {mayCancel && wait.state === 'pending' && (
        <Button size="sm" variant="ghost" className="self-start" disabled={cancelling} onClick={cancel}>{cancelling ? 'Cancelling…' : 'Cancel'}</Button>
      )}
    </li>
  )
}

export function WaitConditions({ run, inset, client = api }: { run: RunRecord; inset: boolean; client?: Api }) {
  const cap = useCapability()
  const self = useSelf()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const waits = run.wait_conditions
  if (!waits?.length) return null
  const mayCancel = cap.hasMethod('run.wait.cancel') && allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const pending = waits.filter((w) => w.state === 'pending').length
  return (
    <section id="details-waiting" aria-label="Waiting until" className={cn('flex flex-col gap-2 border-b border-seam py-3 last:border-b-0', inset && 'px-4')}>
      <SectionLabel as="h2" className="flex items-center gap-1.5">
        Waiting until
        {pending > 0 && <span className="text-text tabular-nums">{pending}</span>}
      </SectionLabel>
      <p className="text-ui-sm text-muted">
        {pending > 0 ? 'The server checks these and wakes the agent when one resolves.' : 'Nothing is pending. The last conditions the agent waited on:'}
      </p>
      <ul className="flex flex-col divide-y divide-seam">
        {orderWaits(waits).map((wait) => <WaitRow key={wait.id} wait={wait} runID={run.id} mayCancel={mayCancel} client={client} />)}
      </ul>
    </section>
  )
}

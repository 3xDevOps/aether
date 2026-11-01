import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { api, type Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { memberName } from '@/lib/member-name'
import { allowed } from '@/lib/permissions'
import { runLabel } from '@/lib/status'
import type { WaitCondition, WaitState } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

const stateLabel: Record<WaitState, string> = {
  pending: 'Pending',
  resolved: 'Resolved',
  cancelled: 'Cancelled',
  superseded: 'Replaced',
  expired: 'Expired',
  error: 'Unreadable',
}

const goodOutcomes = new Set(['merged', 'passed', 'completed', 'reported-success'])

const verb: Record<WaitCondition['kind'], string> = {
  pr_merged: 'merged',
  pr_checks: 'checks final',
  run_finished: 'finished',
}

function toneOf(w: WaitCondition): Tone {
  switch (w.state) {
    case 'pending':
      return 'waiting'
    case 'resolved':
      return w.outcome && goodOutcomes.has(w.outcome) ? 'done' : 'failed'
    case 'error':
      return 'failed'
    default:
      return 'closed'
  }
}

function Target({ wait }: { wait: WaitCondition }) {
  const navigate = useStore((s) => s.navigate)
  const run = useStore((s) => (wait.run_id ? s.runs[wait.run_id] : undefined))
  if (wait.repo && wait.number) {
    return <a href={`https://github.com/${wait.repo}/pull/${wait.number}`} target="_blank" rel="noreferrer" className="break-all text-accent underline-offset-2 hover:underline">{wait.repo}#{wait.number}</a>
  }
  if (wait.run_id) {
    return (
      <Button variant="link" size="sm" className="inline h-auto p-0 text-left text-ui whitespace-normal" onClick={() => navigate('run', { runId: wait.run_id! })}>
        {run ? runLabel(run) : wait.run_id}
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
  const self = useSelf()
  const cancelledBy = useStore((s) => (wait.ended_by_member_id ? memberName(s, wait.ended_by_member_id) : undefined))
  const [cancelling, setCancelling] = useState(false)
  const tone = toneOf(wait)
  const badge = wait.state === 'resolved' && wait.outcome
    ? wait.outcome.charAt(0).toUpperCase() + wait.outcome.slice(1).replaceAll('-', ' ')
    : stateLabel[wait.state]
  const verified = wait.source_checked_at ?? wait.last_checked_at
  const failures = wait.consecutive_failures
  const checks = wait.observed.checks
  const attempts = `${wait.delivery_attempts} ${wait.delivery_attempts === 1 ? 'attempt' : 'attempts'}`
  const woke = wait.state === 'resolved' || wait.state === 'expired' || wait.state === 'error'
  const delivery = wait.delivered_at
    ? <>Delivered to the agent <RelativeTime at={wait.delivered_at} /></>
    : woke && (wait.last_delivery_error
      ? `Not delivered after ${attempts}: ${wait.last_delivery_error}`
      : wait.delivery_attempts > 0 ? `Delivering, ${attempts} so far` : 'Waiting to be delivered')
  const cancel = () => {
    const identity = useStore.getState().identityKey
    setCancelling(true)
    client.runWaitCancel(runID, wait.id)
      .then(({ wait: ended }) => {
        toast.success('Wait cancelled')
        const s = useStore.getState()
        const waits = s.runs[runID]?.wait_conditions
        // A run.wait.changed reread may have landed first; only a still-pending copy is older than the cancel.
        if (s.identityKey !== identity || !waits?.some((w) => w.id === ended.id && w.state === 'pending')) return
        s.patchRun(runID, { wait_conditions: waits.map((w) => (w.id === ended.id ? ended : w)) })
      }, (err: unknown) => toast.error(`Cancel failed: ${message(err)}`))
      .finally(() => setCancelling(false))
  }
  return (
    <li className="flex min-w-0 flex-col gap-1.5 py-2 first:pt-0 last:pb-0" aria-label={wait.spec}>
      <div className="flex min-w-0 items-start gap-2">
        <StatusDot tone={tone} label={stateLabel[wait.state]} className="mt-1.5" />
        <p className="flex min-w-0 flex-1 items-baseline gap-1 text-ui font-medium">
          <span className="line-clamp-2 min-w-0 break-words"><Target wait={wait} /></span>
          <span className="shrink-0">{verb[wait.kind]}</span>
        </p>
        <Badge tone={tone} className="shrink-0">{badge}</Badge>
      </div>
      {wait.summary && wait.summary !== wait.last_error && <p className="text-ui-sm break-words">{wait.summary}</p>}
      {wait.detail && wait.detail !== wait.last_error && <p className="text-ui-sm text-muted break-words">{wait.detail}</p>}
      <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-2 gap-y-0.5 text-ui-sm">
        {wait.observed.pr_state && <Fact term="Pull request">{wait.observed.pr_state}</Fact>}
        {wait.observed.run_status && <Fact term="Session status">{wait.observed.run_status}</Fact>}
        {wait.observed.head_sha && <Fact term="Head commit"><Code title={wait.observed.head_sha}>{wait.observed.head_sha.slice(0, 8)}</Code></Fact>}
        {checks && (
          <Fact term={checks.required_only ? 'Required checks' : 'Checks'}>
            {checks.passed + checks.failed + checks.pending + checks.cancelled === 0
              ? 'None reported yet'
              : `${checks.passed} passed, ${checks.failed} failed, ${checks.pending} running${checks.cancelled > 0 ? `, ${checks.cancelled} cancelled` : ''}`}
          </Fact>
        )}
        <Fact term="Last verified">{verified ? <RelativeTime at={verified} /> : 'Not yet'}</Fact>
        {wait.state === 'pending' && wait.next_check_at && Date.parse(wait.next_check_at) > now && (
          <Fact term="Next check"><RelativeTime at={wait.next_check_at} /></Fact>
        )}
        {failures > 0 && (
          <Fact term="Failures" tone="failed">
            {failures} in a row{wait.last_error && `: ${wait.last_error}`}
            {wait.last_error_at && <span className="text-muted"> · <RelativeTime at={wait.last_error_at} /></span>}
          </Fact>
        )}
        {failures === 0 && wait.last_error && <Fact term="Last error" tone="failed">{wait.last_error}</Fact>}
        {wait.resolved_at && <Fact term="Resolved"><RelativeTime at={wait.resolved_at} /></Fact>}
        {delivery && <Fact term="Delivery" tone={wait.last_delivery_error && !wait.delivered_at ? 'failed' : undefined}>{delivery}</Fact>}
        {wait.ended_by_member_id
          ? <Fact term="Ended">Cancelled by {wait.ended_by_member_id === self.id ? 'you' : cancelledBy}</Fact>
          : wait.ended_cause && <Fact term="Ended">{wait.ended_cause}</Fact>}
        {wait.state === 'pending' && <Fact term="Gives up"><RelativeTime at={wait.expires_at} /></Fact>}
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
  const ordered = [...waits].sort((a, b) =>
    Number(b.state === 'pending') - Number(a.state === 'pending') || b.revision - a.revision || b.created_at.localeCompare(a.created_at))
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
        {ordered.map((wait) => <WaitRow key={wait.id} wait={wait} runID={run.id} mayCancel={mayCancel} client={client} />)}
      </ul>
    </section>
  )
}

import { type ReactNode, useEffect, useId, useState } from 'react'
import { toast } from 'sonner'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Code } from '@/components/ui/code'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FailureCallout } from '@/components/ui/failure-callout'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { Skeleton } from '@/components/ui/skeleton'
import { StateLine, StatusDot } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { errorSentence } from '@/lib/format'
import type { Watch, WatchCondition, WatchNotification } from '@/lib/types'
import { conditionChoices, conditionLabel, deliveryText, deliveryTone, isLive, linkTarget, maxExpiryDays, stateWords, targetKindLabel } from '@/lib/watches'
import { useStore } from '@/store'
import { readWatch } from '@/store/watches'

const dayMs = 24 * 60 * 60 * 1000

function dateValue(at: string): string {
  const d = new Date(at)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** The end of the chosen day on this browser's clock, or the latest the server takes. */
function endOfDay(value: string, latest: number): string {
  const [year, month, day] = value.split('-').map(Number)
  return new Date(Math.min(new Date(year!, month! - 1, day!, 23, 59, 59).getTime(), latest)).toISOString()
}

/** The server refuses an end more than maxExpiryDays from its own now; a minute's margin covers clock skew and the request. */
function latestEnd(): number {
  return Date.now() + maxExpiryDays * dayMs - 60_000
}

export function LinkTo({ link, children, className }: { link: string; children: ReactNode; className?: string }) {
  const navigate = useStore((s) => s.navigate)
  const target = linkTarget(link)
  if ('href' in target) {
    return <a href={target.href} target="_blank" rel="noreferrer" className={className ?? 'text-accent underline-offset-2 hover:underline'}>{children}</a>
  }
  return <Button variant="link" className={className ?? 'text-ui-sm'} onClick={() => navigate(target.route.name, target.route.params)}>{children}</Button>
}

function Observed({ watch }: { watch: Watch }) {
  const o = watch.observed
  const rows: [string, ReactNode][] = []
  if (o.run_status) rows.push(['Run status', o.run_status])
  if (o.activity) rows.push(['Activity', o.activity])
  if (o.need) rows.push(['Need', o.need])
  if (o.phase) rows.push(['Phase', o.phase])
  if (o.pr_state) rows.push(['Pull request', o.pr_state])
  if (o.head_sha) rows.push(['Head commit', <Code key="sha">{o.head_sha.slice(0, 12)}</Code>])
  if (o.checks) rows.push(['Checks', `${o.checks.passed} passed, ${o.checks.failed} failed, ${o.checks.pending} pending`])
  if (o.review_state) rows.push(['Review', o.review_state])
  rows.push(['Created', <RelativeTime key="created" at={watch.created_at} />])
  if (watch.state === 'done') rows.push(['Done', <RelativeTime key="done" at={watch.updated_at} />])
  else rows.push([watch.state === 'expired' ? 'Ended' : 'Ends', <RelativeTime key="expires" at={watch.expires_at} />])
  if (watch.last_check_at) rows.push(['Last check', <RelativeTime key="last" at={watch.last_check_at} />])
  if (watch.next_check_at && isLive(watch)) rows.push(['Next check', <RelativeTime key="next" at={watch.next_check_at} />])
  return (
    <section aria-label="What the server sees" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h3">What the server sees</SectionLabel>
      {o.summary && <p className="text-ui text-text">{o.summary}</p>}
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm [&_dt]:text-muted">
        {rows.map(([term, value]) => (
          <div key={term} className="contents">
            <dt>{term}</dt>
            <dd className="min-w-0 break-words">{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

function Notices({ notices }: { notices: WatchNotification[] | undefined }) {
  return (
    <section aria-label="Notifications" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h3">Notifications</SectionLabel>
      {!notices && <Skeleton className="h-12" />}
      {notices?.length === 0 && <p className="text-ui-sm text-muted">None yet. Each notice lists where it was delivered.</p>}
      {notices && notices.length > 0 && (
        <ol className="flex min-w-0 flex-col divide-y divide-seam rounded-panel border border-seam">
          {notices.map((notice) => (
            <li key={notice.id} className="flex min-w-0 flex-col gap-1 px-3 py-2.5">
              <div className="flex min-w-0 items-baseline justify-between gap-3">
                <span className="min-w-0 text-ui font-medium text-text">{notice.title}</span>
                <RelativeTime at={notice.created_at} className="shrink-0 text-ui-sm text-muted tabular-nums" />
              </div>
              {notice.body && <p className="break-words text-ui-sm text-muted">{notice.body}</p>}
              <ul aria-label="Delivery" className="flex min-w-0 flex-col gap-0.5">
                {notice.deliveries.map((delivery) => (
                  <li key={`${delivery.channel}-${delivery.at}`} className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
                    <StatusDot tone={deliveryTone(delivery)} />
                    <span className="min-w-0 break-words">{deliveryText(delivery)}</span>
                  </li>
                ))}
              </ul>
              {notice.link && <LinkTo link={notice.link} className="self-start text-ui-sm">Open</LinkTo>}
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}

interface EditFields {
  conditions: WatchCondition[]
  label: string
  expires: string
}

function editFields(watch: Watch): EditFields {
  return { conditions: watch.conditions, label: watch.label ?? '', expires: dateValue(watch.expires_at) }
}

function sameConditions(a: WatchCondition[], b: WatchCondition[]): boolean {
  return a.length === b.length && a.every((c) => b.includes(c))
}

function Edit({ watch, client }: { watch: Watch; client: Api }) {
  const id = useId()
  // base is the watch as this form last took it. An untouched form follows later changes, such as the assistant's;
  // a touched one keeps the member's edits and sends only the fields they changed.
  const [base, setBase] = useState(() => ({ at: watch.updated_at, fields: editFields(watch) }))
  const [form, setForm] = useState(base.fields)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const conditionsChanged = !sameConditions(form.conditions, base.fields.conditions)
  const labelChanged = form.label.trim() !== base.fields.label
  const expiresChanged = form.expires !== base.fields.expires
  const touched = conditionsChanged || labelChanged || expiresChanged
  if (!touched && !busy && base.at !== watch.updated_at) {
    const next = { at: watch.updated_at, fields: editFields(watch) }
    setBase(next)
    setForm(next.fields)
  }
  const expired = watch.state === 'expired'
  const today = dateValue(new Date().toISOString())
  const latest = dateValue(new Date(latestEnd()).toISOString())

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const result = await client.watchUpdate({
        watch_id: watch.id,
        ...(conditionsChanged ? { conditions: form.conditions } : {}),
        ...(labelChanged ? { label: form.label.trim() } : {}),
        ...(expiresChanged ? { expires_at: endOfDay(form.expires, latestEnd()) } : {}),
      })
      useStore.getState().putWatch(result.watch)
      const next = { at: result.watch.updated_at, fields: editFields(result.watch) }
      setBase(next)
      setForm(next.fields)
      toast.success(result.receipt)
    } catch (err) {
      setError(errorSentence(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-label="Change this watch" className="flex min-w-0 flex-col gap-3">
      <fieldset className="flex min-w-0 flex-col gap-2">
        <legend className="mb-2 text-ui-sm font-medium text-muted">Notify me when</legend>
        {conditionChoices[watch.target.kind].map((choice) => (
          <div key={choice.value} className="flex min-w-0 items-start gap-3">
            <Checkbox
              id={`${id}-${choice.value}`}
              className="mt-0.5"
              aria-describedby={`${id}-${choice.value}-help`}
              checked={form.conditions.includes(choice.value)}
              disabled={busy}
              onCheckedChange={(on) => setForm((f) => ({
                ...f,
                conditions: on === true ? [...f.conditions, choice.value] : f.conditions.filter((c) => c !== choice.value),
              }))}
            />
            <div className="flex min-w-0 flex-col gap-0.5">
              <label htmlFor={`${id}-${choice.value}`} className="text-ui font-medium text-text">{choice.label}</label>
              <p id={`${id}-${choice.value}-help`} className="text-ui-sm text-muted">{choice.description}</p>
            </div>
          </div>
        ))}
      </fieldset>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <FormField label="Label" help="Shown instead of the target's title.">
          <Input value={form.label} maxLength={120} disabled={busy} placeholder={watch.target_title} onChange={(event) => setForm({ ...form, label: event.target.value })} />
        </FormField>
        <FormField
          label="Ends on"
          help={expired ? `Pick a new day, at most ${maxExpiryDays} days from today, to watch again.` : `At most ${maxExpiryDays} days from today.`}
        >
          <Input type="date" value={form.expires} min={today} max={latest} disabled={busy} onChange={(event) => setForm({ ...form, expires: event.target.value })} />
        </FormField>
      </div>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      <Button
        variant="secondary"
        size="sm"
        className="self-start"
        disabled={busy || !touched || form.conditions.length === 0 || (expired && !expiresChanged)}
        onClick={() => void save()}
      >
        {busy ? 'Saving…' : expired ? 'Watch again' : 'Save changes'}
      </Button>
    </section>
  )
}

/** One watch: what it follows, what the server last saw, its notices and where each went. Full screen on a phone. */
export function WatchDetail({ watchID, client, onClose }: { watchID: string; client: Api; onClose: () => void }) {
  const phone = useIsMobile()
  const watch = useStore((s) => s.watches[watchID])
  const notices = useStore((s) => s.watchNotices[watchID])
  const [failed, setFailed] = useState<string | null>(null)
  const [missing, setMissing] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState(false)

  useEffect(() => {
    let live = true
    setFailed(null)
    readWatch(useStore, client, watchID).then(
      (result) => live && setMissing(result === null),
      (err: unknown) => live && setFailed(errorSentence(err)),
    )
    return () => {
      live = false
    }
  }, [client, watchID, attempt])

  const pause = async (paused: boolean) => {
    if (!watch) return
    setBusy(true)
    try {
      const result = await client.watchUpdate({ watch_id: watch.id, paused })
      useStore.getState().putWatch(result.watch)
      toast.success(result.receipt)
    } catch (err) {
      toast.error(`${paused ? 'Pause' : 'Resume'} failed: ${errorSentence(err)}`)
    } finally {
      setBusy(false)
    }
  }

  const state = watch && stateWords[watch.state]
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent
        variant={phone ? 'side' : 'center'}
        className={phone ? 'w-full gap-4 overflow-y-auto p-4' : 'max-w-2xl'}
      >
        <DialogHeader>
          <DialogTitle>{watch ? watch.label || watch.target_title : 'Watch'}</DialogTitle>
          {!watch && <DialogDescription className="sr-only">What this watch follows and the notices it sent.</DialogDescription>}
          {watch && (
            <DialogDescription asChild>
              <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                <span>{targetKindLabel(watch.target)}</span>
                <span aria-hidden>·</span>
                <span>{watch.conditions.map((c) => conditionLabel(watch.target.kind, c)).join(', ')}</span>
                {watch.link && <><span aria-hidden>·</span><LinkTo link={watch.link}>Open {watch.target.kind === 'mission' ? 'swarm' : watch.target.kind === 'pr' ? 'pull request' : 'run'}</LinkTo></>}
              </div>
            </DialogDescription>
          )}
        </DialogHeader>
        {failed && <FailureCallout error={failed} onRetry={() => setAttempt((n) => n + 1)} />}
        {missing && <Callout tone="neutral" role="status">This watch no longer exists. It was deleted, or it ended more than 30 days ago.</Callout>}
        {!watch && !failed && !missing && <Skeleton className="h-24" />}
        {watch && state && (
          <>
            <StateLine tone={state.tone} strong>{state.label}</StateLine>
            {watch.state === 'error' && (
              <Callout tone="failed" title="The server cannot check this target">
                {watch.error || 'No reason given.'} It keeps retrying{watch.next_check_at ? <>; next check <RelativeTime at={watch.next_check_at} /></> : null}.
              </Callout>
            )}
            <Observed watch={watch} />
            {(isLive(watch) || watch.state === 'expired') && <Edit watch={watch} client={client} />}
            <Notices notices={notices} />
            <div className="flex flex-wrap gap-2 border-t border-seam pt-3">
              {watch.state === 'paused' && <Button variant="secondary" size="sm" disabled={busy} onClick={() => void pause(false)}>Resume</Button>}
              {(watch.state === 'active' || watch.state === 'error') && <Button variant="secondary" size="sm" disabled={busy} onClick={() => void pause(true)}>Pause</Button>}
              <Button variant="secondary" size="sm" disabled={busy} onClick={() => setDeleting(true)}>Delete…</Button>
            </div>
          </>
        )}
        {deleting && watch && (
          <Confirm
            title="Delete this watch?"
            description="The server stops checking it and forgets its notifications. The target itself is not touched."
            action="Delete watch"
            pending="Deleting…"
            onConfirm={() => client.watchDelete(watch.id)}
            onDone={() => {
              useStore.getState().dropWatch(watch.id)
              toast.success('Watch deleted')
              onClose()
            }}
            onClose={() => setDeleting(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { DecisionButtons } from '@/components/decision-buttons'
import { SysboxNudge } from '@/components/sysbox-nudge'
import { deliveryLabel } from '@/components/palette/inject-dialog'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { CodeBlock } from '@/components/ui/code'
import { EmptyState } from '@/components/ui/empty-state'
import { IconTile } from '@/components/ui/icon-tile'
import { ListRow } from '@/components/ui/list-row'
import { RelativeTime } from '@/components/ui/relative-time'
import { Clamped, RequestCard } from '@/components/ui/request-card'
import { RequestAction, RequestActions, RequestFooter } from '@/components/ui/request-choice'
import { SectionLabel } from '@/components/ui/section-label'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { Textarea } from '@/components/ui/textarea'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { redirectRoute } from '@/lib/url-state'
import { memberName } from '@/lib/member-name'
import { needsYouConditions } from '@/lib/needs-you'
import type { AttentionItem, RoomPostResult, RunInputRespondResult } from '@/lib/types'
import { targetRoute } from '@/routes/board/card-action'
import { asking, finished, refreshAttention, useAccountNeeds, useAttention, useAttentionFeed, useAttentionSupported } from '@/routes/needs-you/attention'
import { DeviceCard } from '@/routes/board/device-card'
import { MergeProposalCard } from '@/routes/board/merge-card'
import { registerRoute } from '@/routes/registry'
import { choicePlan } from '@/routes/run/request-options'
import { FormFields, missingRequired } from '@/routes/run/session-form'
import { useStore } from '@/store'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

const tones: Partial<Record<AttentionItem['kind'], Tone>> = { failed: 'failed', disconnected: 'disconnected', ready: 'review' }

function useOpen(item: AttentionItem) {
  const navigate = useStore((s) => s.navigate)
  return () => {
    const route = targetRoute({ id: item.run_id }, needsYouConditions.find((c) => c.id === item.kind)?.target)
    navigate(route.name, route.params)
  }
}

function settle(result: RunInputRespondResult) {
  if (result.status === 'already_answered') toast.info('Already answered: an earlier answer won')
  void refreshAttention()
}

/** Whether the server took the reply; one not sent or uncertain keeps its draft for a retry. */
function settleMessage(result: RoomPostResult): boolean {
  const label = `Reply ${deliveryLabel(result).toLowerCase()}`
  const unconfirmed = result.receipt === 'not_sent' || result.receipt === 'uncertain' || result.message.state === 'not_sent' || result.message.state === 'uncertain'
  if (unconfirmed) toast.error(label)
  else toast.success(label)
  void refreshAttention()
  return !unconfirmed
}

/** Approve and Deny answer this request by id, so a tap that lands after it changed decides nothing else. */
const decisionOptions = (item: AttentionItem) => {
  const options = item.request?.options ?? []
  const allow = options.find((o) => o.kind === 'allow_once')
  const deny = options.find((o) => o.kind === 'reject_once')
  return allow && deny ? { allow, deny } : undefined
}

/** Shown once the server took an answer, until the list drops the request; a second tap could only be refused. */
function Answered() {
  return <p role="status" className="text-ui-sm text-muted">Answered</p>
}

function Decision({ item, onError }: { item: AttentionItem; onError: (error?: string) => void }) {
  const [answered, setAnswered] = useState(false)
  const { allow, deny } = decisionOptions(item)!
  const request = item.request!
  const decide = async (approve: boolean) => {
    onError(undefined)
    try {
      settle(await api.runInputRespond(item.run_id, request.id, approve ? allow.id : deny.id))
      setAnswered(true)
    } catch (err) {
      onError(message(err))
    }
  }
  if (answered) return <Answered />
  return <div className="grid grid-cols-2 gap-2"><DecisionButtons decide={decide} /></div>
}

function Question({ item, onError }: { item: AttentionItem; onError: (error?: string) => void }) {
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [busy, setBusy] = useState<string | null>(null)
  const [answered, setAnswered] = useState(false)
  const late = usePastDeadline(busy !== null)
  const request = item.request!
  // Only a permission with a tool call is drawn as pills.
  const plan = choicePlan({
    id: request.id,
    kind: 'question',
    title: request.title ?? '',
    options: request.options?.map((o) => ({ id: o.id, name: o.label, kind: o.kind })),
    schema: request.schema,
    status: 'pending',
  }, undefined) as Extract<ReturnType<typeof choicePlan>, { kind: 'buttons' }>
  const answer = async (optionID: string) => {
    setBusy(optionID)
    onError(undefined)
    try {
      settle(await api.runInputRespond(item.run_id, request.id, optionID, optionID === 'accept' ? values : undefined))
      setAnswered(true)
    } catch (err) {
      onError(message(err))
    } finally {
      setBusy(null)
    }
  }
  if (answered) return <Answered />
  return (
    <>
      {request.schema !== undefined && <FormFields schema={request.schema} values={values} onChange={setValues} />}
      <RequestActions>
        {plan.choices.map(({ option, primary }) => (
          <RequestAction
            key={option.id}
            label={busy === option.id ? (late ? stillWaiting : 'Sending…') : option.name}
            variant={primary ? 'primary' : 'secondary'}
            disabled={busy !== null || (option.id === 'accept' && missingRequired(request.schema, values))}
            onClick={() => void answer(option.id)}
          />
        ))}
      </RequestActions>
    </>
  )
}

// The field stays above the soft keyboard: iOS reports it only as --keyboard-inset, and the list pads for it.
function keepInView(element: HTMLElement | null) {
  requestAnimationFrame(() => element?.scrollIntoView({ block: 'nearest' }))
}

function Reply({ item, onError }: { item: AttentionItem; onError: (error?: string) => void }) {
  const [open, setOpen] = useState(false)
  const [body, setBody] = useState('')
  const [sent, setSent] = useState<{ text: string; key: string } | null>(null)
  const [sending, setSending] = useState(false)
  const late = usePastDeadline(sending)
  const form = useRef<HTMLFormElement>(null)
  useEffect(() => {
    const viewport = window.visualViewport
    if (!open || !viewport) return
    const follow = () => {
      if (form.current?.contains(document.activeElement)) keepInView(form.current)
    }
    viewport.addEventListener('resize', follow)
    return () => viewport.removeEventListener('resize', follow)
  }, [open])
  const send = async () => {
    const text = body.trim()
    if (!text || sending) return
    // A retry of the same text reuses its key, so the server sends it once.
    const key = sent?.text === text ? sent.key : crypto.randomUUID()
    setSent({ text, key })
    setSending(true)
    onError(undefined)
    try {
      const taken = settleMessage(item.message
        ? await api.runRoomPost({ workspace_id: item.workspace_id, run_id: item.run_id, kind: 'reply', body: text, correlation_id: item.message.id, idempotency_key: key })
        : await api.runInject(item.run_id, text, key))
      if (taken) {
        setSent(null)
        setBody((current) => (current.trim() === text ? '' : current))
      }
    } catch (err) {
      onError(`Send failed: ${message(err)}`)
    } finally {
      setSending(false)
    }
  }
  if (!open) {
    return (
      <RequestActions>
        <RequestAction label="Reply…" onClick={() => setOpen(true)} />
      </RequestActions>
    )
  }
  return (
    <form
      ref={form}
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        void send()
      }}
    >
      <Textarea
        autoFocus
        rows={2}
        aria-label={`Reply to ${item.title}`}
        placeholder="Reply to the agent…"
        value={body}
        onFocus={() => keepInView(form.current)}
        onChange={(event) => setBody(event.target.value)}
        onKeyDown={(event) => {
          if (!event.nativeEvent.isComposing && (event.metaKey || event.ctrlKey) && event.key === 'Enter') {
            event.preventDefault()
            void send()
          }
        }}
      />
      <div className="grid grid-cols-2 gap-2">
        <RequestAction type="button" label="Cancel" disabled={sending} onClick={() => setOpen(false)} />
        <RequestAction type="submit" label={sending ? (late ? stillWaiting : 'Sending…') : 'Send'} variant="primary" disabled={sending || !body.trim()} />
      </div>
    </form>
  )
}

function inline(item: AttentionItem, replies: boolean) {
  if (item.kind === 'permission') return decisionOptions(item) ? Decision : undefined
  if (item.kind === 'question') return item.request?.options?.length && !item.request.url ? Question : undefined
  if (item.kind === 'room-question') return item.message ? Reply : undefined
  if (item.kind === 'needs-input') return replies ? Reply : undefined
  return undefined
}

function AttentionCard({ item }: { item: AttentionItem }) {
  const open = useOpen(item)
  const run = useStore((s) => s.runs[item.run_id])
  const [error, setError] = useState<string>()
  const condition = needsYouConditions.find((c) => c.id === item.kind)
  const action = run && condition?.action(run)
  const viewer = useStore((s) => s.info?.member.id)
  // run.input.respond refuses while another member holds control: the request is theirs.
  const holder = run?.controller_member_id && run.controller_member_id !== viewer ? run.controller_member_id : undefined
  const holderName = useStore((s) => (holder ? memberName(s, holder) : undefined))
  const answers = item.kind === 'permission' || item.kind === 'question'
  const Inline = holder && answers ? undefined : inline(item, action?.kind === 'reply')
  const raw = item.kind === 'permission' ? item.request?.title : item.message?.body ?? item.request?.title
  const detail = raw && !item.reason.includes(raw) ? raw : undefined
  return (
    <li>
      <RequestCard
        aria-label={item.title}
        tone={tones[item.kind] ?? 'needs-you'}
        avatar={<IconTile aria-hidden><AgentGlyph agent={item.harness} className="size-4" /></IconTile>}
        title={(
          <Button variant="quiet" onClick={open} className="text-left font-medium whitespace-normal text-text">
            <span className="line-clamp-2">{item.title}</span>
          </Button>
        )}
        meta={item.since && <RelativeTime at={item.since} compact />}
        description={(
          <>
            <span className="text-text">{item.reason}</span>
            {detail && (
              <Clamped className="mt-1.5 max-h-[5lh]"><CodeBlock className="whitespace-pre-wrap">{detail}</CodeBlock></Clamped>
            )}
          </>
        )}
        footer={(
          <RequestFooter className="static">
            {holder && answers && <p className="text-ui-sm text-muted">{holderName} controls this session, so the request is theirs to answer.</p>}
            {Inline ? <Inline key={item.request?.id ?? item.message?.id ?? item.kind} item={item} onError={setError} /> : (
              <RequestActions>
                <RequestAction label={action && action.kind === 'open' ? action.label : 'Open'} onClick={open} />
              </RequestActions>
            )}
            {error && <p role="alert" className="text-ui-sm break-words text-state-failed">{error}</p>}
          </RequestFooter>
        )}
      />
    </li>
  )
}

function FinishedRow({ item }: { item: AttentionItem }) {
  const open = useOpen(item)
  return (
    <li>
      <ListRow
        leading={<StatusDot tone="review" />}
        trailing={item.since && <RelativeTime at={item.since} compact className="text-ui-sm text-muted" />}
        onClick={open}
      >
        {item.title}
      </ListRow>
    </li>
  )
}

/** A phone's shell keeps the list in step; a wider window reached by link does it here. */
function Feed() {
  useAttentionFeed()
  return null
}

function NeedsYou() {
  const mobile = useIsMobile()
  const supported = useAttentionSupported()
  const hydrated = useStore((s) => s.hydrated)
  useEffect(() => {
    if (!supported) {
      if (hydrated) redirectRoute(useStore, { name: 'board', params: {} })
      return
    }
    void refreshAttention()
  }, [supported, hydrated])
  const items = useAttention((s) => s.items)
  const error = useAttention((s) => s.error)
  const asks = items ? asking(items) : []
  const done = items ? finished(items) : []
  const { devices, merges } = useAccountNeeds()
  const loading = useDelayed(items === null && !error)
  return (
    <div className="flex h-full min-w-0 flex-col">
      {!mobile && <Feed />}
      <ViewHeader title="Needs you" />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-3 pb-[calc(1.5rem+var(--keyboard-inset,0px))]">
        <div className="mb-3 empty:hidden"><SysboxNudge /></div>
        {error && (
          <Callout
            tone="failed"
            role="alert"
            title="Could not read what needs you"
            className="mb-3"
            actions={<Button size="sm" variant="secondary" onClick={() => void refreshAttention()}>Retry</Button>}
          >
            {error}
          </Callout>
        )}
        {(merges.length > 0 || devices.length > 0) && (
          <ul aria-label="Approvals" className="mb-3 flex flex-col gap-3">
            {merges.map((proposal) => <li key={proposal.id}><MergeProposalCard proposal={proposal} /></li>)}
            {devices.map((need) => <li key={need.device.id}><DeviceCard need={need} /></li>)}
          </ul>
        )}
        {loading && (
          <div aria-label="Loading" className="flex flex-col gap-3">
            <Skeleton className="h-32 rounded-panel" />
            <Skeleton className="h-32 rounded-panel" />
          </div>
        )}
        {items && asks.length === 0 && merges.length === 0 && devices.length === 0 && <EmptyState title="Nothing needs you" className="py-10">Requests from your sessions, devices and sign-in merges show up here.</EmptyState>}
        {asks.length > 0 && (
          <ul aria-label="Needs you" className="flex flex-col gap-3">
            {asks.map((item) => <AttentionCard key={item.run_id} item={item} />)}
          </ul>
        )}
        {done.length > 0 && (
          <section aria-labelledby="needs-you-finished" className="mt-6">
            <SectionLabel as="h2" id="needs-you-finished" className="px-1 pb-1">
              Finished: review the result · {done.length}
            </SectionLabel>
            <ul>{done.map((item) => <FinishedRow key={item.run_id} item={item} />)}</ul>
          </section>
        )}
      </div>
    </div>
  )
}

registerRoute('needs-you', NeedsYou)

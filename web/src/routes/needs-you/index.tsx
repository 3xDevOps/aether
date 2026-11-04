import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { DecisionButtons } from '@/components/decision-buttons'
import { deliveryLabel } from '@/components/palette/inject-dialog'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { CodeBlock } from '@/components/ui/code'
import { EmptyState } from '@/components/ui/empty-state'
import { IconTile } from '@/components/ui/icon-tile'
import { ListRow } from '@/components/ui/list-row'
import { RelativeTime } from '@/components/ui/relative-time'
import { RequestCard } from '@/components/ui/request-card'
import { RequestAction, RequestActions, RequestFooter } from '@/components/ui/request-choice'
import { SectionLabel } from '@/components/ui/section-label'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { Textarea } from '@/components/ui/textarea'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { message } from '@/lib/format'
import { redirectRoute } from '@/lib/url-state'
import { needsYouConditions } from '@/lib/needs-you'
import type { AttentionItem, RoomPostResult, RunInputRespondResult } from '@/lib/types'
import { targetRoute } from '@/routes/board/card-action'
import { asking, finished, refreshAttention, useAttention, useAttentionFeed, useAttentionSupported } from '@/routes/needs-you/attention'
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
function Decision({ item, onError }: { item: AttentionItem; onError: (error?: string) => void }) {
  const options = item.request?.options ?? []
  const allow = options.find((o) => o.kind === 'allow_once')
  const deny = options.find((o) => o.kind === 'reject_once')
  if (!item.request || !allow || !deny) return null
  const request = item.request
  const decide = async (approve: boolean) => {
    onError(undefined)
    try {
      settle(await api.runInputRespond(item.run_id, request.id, approve ? allow.id : deny.id))
    } catch (err) {
      onError(message(err))
    }
  }
  return <RequestActions><DecisionButtons decide={decide} /></RequestActions>
}

function Question({ item, onError }: { item: AttentionItem; onError: (error?: string) => void }) {
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [busy, setBusy] = useState<string | null>(null)
  const late = usePastDeadline(busy !== null)
  const request = item.request!
  const plan = choicePlan({
    id: request.id,
    kind: 'question',
    title: request.title ?? '',
    options: request.options?.map((o) => ({ id: o.id, name: o.label, kind: o.kind })),
    schema: request.schema,
    status: 'pending',
  }, undefined)
  if (plan.kind !== 'buttons') return null
  const answer = async (optionID: string) => {
    setBusy(optionID)
    onError(undefined)
    try {
      settle(await api.runInputRespond(item.run_id, request.id, optionID, optionID === 'accept' ? values : undefined))
    } catch (err) {
      onError(message(err))
    } finally {
      setBusy(null)
    }
  }
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

function Reply({ item, onError }: { item: AttentionItem; onError: (error?: string) => void }) {
  const [body, setBody] = useState('')
  const [sent, setSent] = useState<{ text: string; key: string } | null>(null)
  const [sending, setSending] = useState(false)
  const late = usePastDeadline(sending)
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
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        void send()
      }}
    >
      <Textarea
        rows={2}
        aria-label={`Reply to ${item.title}`}
        placeholder="Reply to the agent…"
        value={body}
        onChange={(event) => setBody(event.target.value)}
      />
      <RequestActions>
        <RequestAction type="submit" label={sending ? (late ? stillWaiting : 'Sending…') : 'Reply'} variant="primary" disabled={sending || !body.trim()} />
      </RequestActions>
    </form>
  )
}

function inline(item: AttentionItem, replies: boolean) {
  if (item.kind === 'permission') return item.request?.options?.length ? Decision : undefined
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
  const Inline = inline(item, action?.kind === 'reply')
  const detail = item.kind === 'permission' ? item.request?.title : item.message?.body ?? item.request?.title
  return (
    <li>
      <RequestCard
        aria-label={item.title}
        tone={tones[item.kind] ?? 'needs-you'}
        avatar={<IconTile aria-hidden><AgentGlyph agent={item.harness} className="size-4" /></IconTile>}
        title={(
          <Button variant="quiet" onClick={open} className="text-left font-medium whitespace-normal text-text">
            {item.title}
          </Button>
        )}
        meta={item.since && <RelativeTime at={item.since} compact />}
        description={(
          <>
            <span className="text-text">{item.reason}</span>
            {detail && detail !== item.reason && <CodeBlock className="mt-1.5 whitespace-pre-wrap">{detail}</CodeBlock>}
          </>
        )}
        footer={(
          <RequestFooter className="static">
            {Inline ? <Inline key={item.request?.id ?? item.message?.id ?? item.kind} item={item} onError={setError} /> : (
              <RequestActions>
                <RequestAction label={action && action.kind === 'open' ? action.label : 'Open'} onClick={open} />
              </RequestActions>
            )}
            {Inline && (
              <Button variant="link" size="sm" className="self-start" onClick={open}>Open session</Button>
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
  return (
    <div className="flex h-full min-w-0 flex-col">
      {!mobile && <Feed />}
      <ViewHeader title="Needs you" />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-3 pb-6">
        {error && <p role="alert" className="mb-3 text-ui-sm break-words text-state-failed">{error}</p>}
        {items && asks.length === 0 && <EmptyState title="Nothing needs you" className="py-10">Requests from your sessions show up here.</EmptyState>}
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

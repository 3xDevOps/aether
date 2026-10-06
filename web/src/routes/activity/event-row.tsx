import { memo, type ReactNode } from 'react'
import { describeEvent } from '@/components/feed-entry'
import { deliveryWord, MessageKindGlyph, Participants } from '@/components/messages/message-row'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { typeLabel } from '@/lib/events'
import { runLabel } from '@/lib/status'
import type { CoordMessageAckedPayload, CoordMessagePayload, Event, RunStatus } from '@/lib/types'
import { useStore } from '@/store'

const statusTone: Record<RunStatus, Tone> = {
  queued: 'working',
  provisioning: 'working',
  running: 'working',
  'needs-attention': 'needs-you',
  completed: 'done',
  merged: 'done',
  abandoned: 'done',
  failed: 'failed',
  interrupted: 'failed',
}

function MessageEvent({ payload }: { payload: CoordMessagePayload }) {
  const stored = useStore((s) => s.runMessages[payload.message_id])
  return (
    <span className="inline-flex max-w-full min-w-0 flex-wrap items-center gap-x-1.5 align-top">
      <Participants from={payload.from_run_id} to={payload.to_run_id} />
      <span className="text-muted">·</span>
      <span className="text-muted">{stored ? deliveryWord(stored) : 'Sent'}</span>
    </span>
  )
}

function AckEvent({ payload }: { payload: CoordMessageAckedPayload }) {
  const stored = useStore((s) => s.runMessages[payload.message_id])
  return stored ? (
    <span className="inline-flex max-w-full min-w-0 flex-wrap items-center gap-x-1.5 align-top">
      <Participants from={stored.from_run_id} to={stored.to_run_id} />
      <span className="text-muted">· Acknowledged</span>
    </span>
  ) : (
    <span className="text-muted">Acknowledged by the recipient</span>
  )
}

function body(event: Event, raw: boolean): ReactNode {
  if (raw) return <code className="font-code text-ui-sm break-all text-muted">{JSON.stringify(event.payload ?? {})}</code>
  if (event.type === 'coord.message') return <MessageEvent payload={event.payload as CoordMessagePayload} />
  if (event.type === 'coord.message.acked') return <AckEvent payload={event.payload as CoordMessageAckedPayload} />
  return describeEvent(event)
}

export const EventRow = memo(function EventRow({ event, raw }: { event: Event; raw: boolean }) {
  const actor = useStore((s) => s.members[event.actor_id])
  const run = useStore((s) => s.runs[event.run_id])
  const navigate = useStore((s) => s.navigate)
  const status = event.type === 'run.status' ? statusTone[(event.payload as { to?: RunStatus } | undefined)?.to as RunStatus] : undefined
  const message = event.type === 'coord.message' || event.type === 'coord.message.acked'
  const kind = message ? ((event.payload as Partial<CoordMessagePayload>)?.kind ?? 'message') : ''
  return (
    <div className="grid min-w-0 grid-cols-[0.625rem_1rem_minmax(0,1fr)_auto] items-start gap-x-2 px-4 py-1.5 text-ui [grid-template-areas:'dot_who_text_time'_'._._run_run'] hover:bg-hover sm:grid-cols-[6rem_0.625rem_1rem_minmax(0,1fr)_auto] sm:[grid-template-areas:'time_dot_who_text_run']">
      <span className="pt-0.5 text-ui-sm text-muted tabular-nums [grid-area:time] max-sm:text-right">
        <RelativeTime at={event.time} />
      </span>
      <span className="flex h-5 items-center [grid-area:dot]">{status && <StatusDot tone={status} />}</span>
      <span className="flex h-5 items-center [grid-area:who]">
        {message ? <MessageKindGlyph kind={kind} /> : actor ? <Avatar name={actor.display_name} color={actor.color} /> : null}
      </span>
      <div className="min-w-0 break-words [grid-area:text]">
        <span className="mr-2 text-muted" title={event.type}>{raw ? event.type : typeLabel(event.type)}</span>
        <span className="text-text select-text">{body(event, raw)}</span>
      </div>
      {run && !message && (
        <Button variant="ghost" size="sm" className="-my-0.5 max-w-56 min-w-0 justify-self-start [grid-area:run] max-sm:-ml-2" title={runLabel(run)} onClick={() => navigate('run', { runId: run.id })}>
          <span className="truncate">{runLabel(run)}</span>
        </Button>
      )}
    </div>
  )
})

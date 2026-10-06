import { memo, type ReactNode } from 'react'
import { describeEvent, Who } from '@/components/event-words'
import { deliveryWord, MessageKindGlyph, Participants } from '@/components/messages/message-row'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { runLabel } from '@/lib/status'
import type { CoordMessageAckedPayload, CoordMessagePayload, Event, RunStatus } from '@/lib/types'
import type { Visit } from '@/routes/activity/visits'
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
      {stored && <span className="text-muted">· {deliveryWord(stored)}</span>}
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
    <span>An agent read its message</span>
  )
}

function body(event: Event): ReactNode {
  if (event.type === 'coord.message') return <MessageEvent payload={event.payload as CoordMessagePayload} />
  if (event.type === 'coord.message.acked') return <AckEvent payload={event.payload as CoordMessageAckedPayload} />
  return describeEvent(event)
}

function RunLink({ runID }: { runID: string }) {
  const run = useStore((s) => s.runs[runID])
  const navigate = useStore((s) => s.navigate)
  if (!run) return null
  return (
    <Button variant="ghost" size="sm" className="-my-0.5 max-w-56 min-w-0 justify-self-start [grid-area:run] max-sm:-ml-2" title={runLabel(run)} onClick={() => navigate('run', { runId: run.id })}>
      <span className="truncate">{runLabel(run)}</span>
    </Button>
  )
}

function Row({ time, dot, who, children, runID }: { time: string; dot?: Tone; who: ReactNode; children: ReactNode; runID?: string }) {
  return (
    <div className="grid min-w-0 grid-cols-[0.625rem_1rem_minmax(0,1fr)_auto] items-start gap-x-2 px-4 py-1.5 text-ui [grid-template-areas:'dot_who_text_time'_'._._run_run'] hover:bg-hover sm:grid-cols-[6rem_0.625rem_1rem_minmax(0,1fr)_auto] sm:[grid-template-areas:'time_dot_who_text_run']">
      <span className="pt-0.5 text-ui-sm text-muted tabular-nums [grid-area:time] max-sm:text-right">
        <RelativeTime at={time} />
      </span>
      <span className="flex h-5 items-center [grid-area:dot]">{dot && <StatusDot tone={dot} />}</span>
      <span className="flex h-5 items-center [grid-area:who]">{who}</span>
      <div className="min-w-0 break-words text-text select-text [grid-area:text]">{children}</div>
      {runID && <RunLink runID={runID} />}
    </div>
  )
}

function MemberAvatar({ id }: { id: string }) {
  const member = useStore((s) => s.members[id])
  return member ? <Avatar name={member.display_name} color={member.color} /> : null
}

export const VisitRow = memo(function VisitRow({ visit }: { visit: Visit }) {
  const self = useStore((s) => s.info?.member.id === visit.memberID)
  return (
    <Row time={visit.time} who={<MemberAvatar id={visit.memberID} />} runID={visit.runID}>
      <Who id={visit.memberID} /> {visit.open ? `${self ? 'are' : 'is'} viewing the run` : 'viewed the run'}
    </Row>
  )
})

export const EventRow = memo(function EventRow({ event, raw }: { event: Event; raw: boolean }) {
  const status = event.type === 'run.status' ? statusTone[(event.payload as { to?: RunStatus } | undefined)?.to as RunStatus] : undefined
  const message = event.type === 'coord.message' || event.type === 'coord.message.acked'
  const kind = message ? ((event.payload as Partial<CoordMessagePayload>)?.kind ?? 'message') : ''
  if (raw) {
    return (
      <Row time={event.time} who={null} runID={event.run_id}>
        <code className="mr-2 font-code text-ui-sm text-text">{event.type}</code>
        <code className="font-code text-ui-sm break-all text-muted">{JSON.stringify(event.payload ?? {})}</code>
      </Row>
    )
  }
  return (
    <Row
      time={event.time}
      dot={status}
      who={message ? <MessageKindGlyph kind={kind} /> : <MemberAvatar id={event.actor_id} />}
      runID={message ? undefined : event.run_id}
    >
      {body(event)}
    </Row>
  )
})

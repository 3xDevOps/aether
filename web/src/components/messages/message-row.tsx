import { memo, useLayoutEffect, useRef, useState } from 'react'
import { ArrowRight, CircleHelp, ClipboardCheck, FileCheck, type LucideIcon, MessageCircleQuestion, MessageSquare, Reply } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { runLabel } from '@/lib/status'
import type { RunMessage, RunMessageKind } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

export type ParticipantLabel = (runID: string, run: RunRecord | undefined) => string

const kinds: Record<RunMessageKind, { icon: LucideIcon; word: string }> = {
  message: { icon: MessageSquare, word: 'Message' },
  question: { icon: CircleHelp, word: 'Question' },
  reply: { icon: Reply, word: 'Reply' },
  report: { icon: ClipboardCheck, word: 'Report' },
}

export function MessageKindGlyph({ kind }: { kind: string }) {
  const known = kinds[kind as RunMessageKind] ?? kinds.message
  const Icon = known.icon
  return <Icon role="img" aria-label={known.word} className="size-3.5 shrink-0 text-muted" />
}

export function deliveryWord(message: Pick<RunMessage, 'delivered_at' | 'acked_at'>): string {
  if (message.acked_at) return 'Acknowledged'
  if (message.delivered_at) return 'Delivered'
  return 'Sent'
}

function Participant({ runID, label }: { runID: string; label?: ParticipantLabel }) {
  const run = useStore((s) => s.runs[runID])
  const navigate = useStore((s) => s.navigate)
  const name = label ? label(runID, run) : run && runLabel(run)
  if (!name) return <span className="font-code text-ui-sm text-muted">{runID}</span>
  return (
    <Button variant="link" className="max-w-64 min-w-0 shrink" title={name} onClick={() => navigate('run', { runId: runID })}>
      <span className="truncate">{name}</span>
    </Button>
  )
}

export function Participants({ from, to, label }: { from: string; to: string; label?: ParticipantLabel }) {
  return (
    <span className="inline-flex max-w-full min-w-0 items-center gap-1 align-bottom">
      <Participant runID={from} label={label} />
      <ArrowRight aria-label="to" role="img" className="size-3 shrink-0 text-muted" />
      <Participant runID={to} label={label} />
    </span>
  )
}

export const MessageRow = memo(function MessageRow({
  message,
  onThread,
  label,
}: {
  message: RunMessage
  onThread?: (thread: string) => void
  label?: ParticipantLabel
}) {
  const [expanded, setExpanded] = useState(false)
  const long = message.body.split('\n').length > 3 || message.body.length > 280
  const thread = message.correlation_id || (message.kind === 'question' || message.kind === 'report' ? message.id : '')
  const report = message.kind === 'report' ? [['Outcome', message.outcome], ['Summary', message.summary === message.body ? '' : message.summary], ['Next', message.next_action]].filter(([, value]) => value) : []
  return (
    <article aria-label={`${kinds[message.kind]?.word ?? 'Message'} ${message.id}`} className="grid min-w-0 grid-cols-[0.875rem_minmax(0,1fr)] gap-x-2 gap-y-1 px-4 py-2 text-ui">
      <span className="flex h-5 items-center">
        <MessageKindGlyph kind={message.kind} />
      </span>
      <header className="flex min-w-0 flex-wrap items-center gap-x-3">
        <span className="min-w-0 flex-1">
          <Participants from={message.from_run_id} to={message.to_run_id} label={label} />
        </span>
        <span className="flex shrink-0 items-center gap-1.5 text-ui-sm text-muted tabular-nums max-sm:basis-full">
          {message.kind !== 'message' && <span>{kinds[message.kind]?.word} ·</span>}
          <span>{deliveryWord(message)} ·</span>
          <RelativeTime at={message.created_at} />
        </span>
      </header>
      {message.body && (
        <p className={expanded ? 'col-start-2 break-words whitespace-pre-wrap text-text' : 'col-start-2 line-clamp-3 break-words whitespace-pre-wrap text-text'}>
          {message.body}
        </p>
      )}
      {report.length > 0 && (
        <dl className="col-start-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 text-ui-sm">
          {report.map(([term, value]) => (
            <div key={term} className="contents">
              <dt className="text-muted">{term}</dt>
              <dd className="break-words text-text">{value}</dd>
            </div>
          ))}
        </dl>
      )}
      {(long || (thread && onThread)) && (
        <div className="col-start-2 flex gap-3">
          {long && <Button variant="link" size="sm" onClick={() => setExpanded((open) => !open)}>{expanded ? 'Show less' : 'Show more'}</Button>}
          {thread && onThread && <Button variant="link" size="sm" onClick={() => onThread(thread)}>Thread</Button>}
        </div>
      )}
    </article>
  )
})

const kindGlyph: Record<RunMessageKind, { Icon: LucideIcon; label: string }> = {
  message: { Icon: MessageSquare, label: 'Message' },
  question: { Icon: MessageCircleQuestion, label: 'Question' },
  reply: { Icon: Reply, label: 'Reply' },
  report: { Icon: FileCheck, label: 'Report' },
}

function bodyOf(m: RunMessage): string {
  if (m.kind !== 'report') return m.body
  return [m.outcome && `Outcome: ${m.outcome}`, m.summary, m.body, m.next_action && `Next: ${m.next_action}`]
    .filter(Boolean)
    .join('\n')
}

export function AgentMessageRow({ message, label, onOpenRun }: {
  message: RunMessage
  label: (runID: string) => string
  onOpenRun: (runID: string) => (() => void) | undefined
}) {
  const { Icon, label: kind } = kindGlyph[message.kind] ?? kindGlyph.message
  const body = useRef<HTMLParagraphElement>(null)
  const [long, setLong] = useState(false)
  const [open, setOpen] = useState(false)
  const text = bodyOf(message)
  useLayoutEffect(() => {
    const node = body.current
    if (node) setLong(node.scrollHeight > node.clientHeight + 1)
  }, [text])
  const participant = (runID: string) => {
    const open = onOpenRun(runID)
    return open
      ? <Button variant="link" size="sm" onClick={open}><span className="max-w-40 truncate">{label(runID)}</span></Button>
      : <span className="max-w-40 truncate text-text">{label(runID)}</span>
  }
  return (
    <article data-slot="agent-message" aria-label={`${kind} from ${label(message.from_run_id)} to ${label(message.to_run_id)}`} className="flex min-w-0 flex-col gap-0.5 text-ui-sm">
      <div className="flex min-w-0 items-center gap-1.5 text-muted">
        <Icon role="img" aria-label={kind} className="size-3.5 shrink-0" />
        {participant(message.from_run_id)}
        <span aria-hidden>→</span>
        {participant(message.to_run_id)}
        <RelativeTime at={message.created_at} className="ml-auto shrink-0 tabular-nums" />
      </div>
      <p ref={body} className={cn('pl-5 break-words whitespace-pre-wrap text-text', !open && 'line-clamp-3')}>{text}</p>
      <div className="flex items-center gap-2 pl-5 text-muted">
        <span>{deliveryWord(message)}</span>
        {(long || open) && (
          <Button variant="link" size="sm" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? 'Show less' : 'Show more'}
          </Button>
        )}
      </div>
    </article>
  )
}

import { memo, useState } from 'react'
import { ArrowRight, CircleHelp, ClipboardCheck, type LucideIcon, MessageSquare, Reply } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { runLabel } from '@/lib/status'
import type { RunMessage, RunMessageKind } from '@/lib/types'
import { useStore } from '@/store'

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

function Participant({ runID }: { runID: string }) {
  const run = useStore((s) => s.runs[runID])
  const navigate = useStore((s) => s.navigate)
  if (!run) return <span className="font-code text-ui-sm text-muted">{runID}</span>
  const name = runLabel(run)
  return (
    <Button variant="link" className="max-w-64 min-w-0 shrink" title={name} onClick={() => navigate('run', { runId: run.id })}>
      <span className="truncate">{name}</span>
    </Button>
  )
}

export function Participants({ from, to }: { from: string; to: string }) {
  return (
    <span className="inline-flex max-w-full min-w-0 items-center gap-1 align-bottom">
      <Participant runID={from} />
      <ArrowRight aria-label="to" role="img" className="size-3 shrink-0 text-muted" />
      <Participant runID={to} />
    </span>
  )
}

export const MessageRow = memo(function MessageRow({
  message,
  onThread,
}: {
  message: RunMessage
  onThread?: (thread: string) => void
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
          <Participants from={message.from_run_id} to={message.to_run_id} />
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
          {report.map(([label, value]) => (
            <div key={label} className="contents">
              <dt className="text-muted">{label}</dt>
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

import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { toneClasses, type Tone } from '@/components/ui/status-dot'
import { cn } from '@/lib/utils'
import { edgeTone } from '@/routes/board/lanes/connectors'
import { barFill } from '@/routes/board/lanes/lane-row'

const bars: { label: string; className: string }[] = [
  { label: 'Working', className: cn(barFill('working'), 'border-r-2 border-state-working') },
  { label: 'Needs you, or needed attention', className: barFill('needs-you') },
  { label: 'Waiting or answered', className: barFill('ended') },
  { label: 'Paused', className: barFill('paused') },
  { label: 'Ready for review', className: barFill('review') },
  { label: 'Done', className: barFill('done') },
  { label: 'Failed or interrupted', className: barFill('failed') },
  { label: 'Starting', className: barFill('starting') },
  { label: 'History not loaded', className: barFill('unloaded') },
]

function Line({ className, dash, head, tail }: { className: string; dash?: string; head?: 'filled' | 'hollow'; tail?: 'dot' }) {
  return (
    <svg aria-hidden viewBox="0 0 40 10" className={cn('h-2.5 w-10 shrink-0', className)}>
      <path d="M4 5H32" fill="none" stroke="currentColor" strokeWidth={1.5} strokeDasharray={dash} />
      {tail && <circle cx={4} cy={5} r={2} fill="currentColor" stroke="currentColor" strokeWidth={1.25} />}
      {head && <polygon points="38,5 32,1.5 32,8.5" fill={head === 'hollow' ? 'var(--raised)' : 'currentColor'} stroke="currentColor" strokeWidth={1} strokeLinejoin="round" />}
    </svg>
  )
}

function Mark({ sent }: { sent: boolean }) {
  return (
    <svg aria-hidden viewBox="0 0 40 10" className={cn('h-2.5 w-10 shrink-0', edgeTone.message)}>
      {sent
        ? <circle cx={20} cy={5} r={2.75} fill="currentColor" />
        : <circle cx={20} cy={5} r={3} fill="var(--raised)" stroke="currentColor" strokeWidth={1.5} />}
    </svg>
  )
}

const lines = [
  { label: 'Agent message sent, on the edge facing the recipient', line: <Mark sent /> },
  { label: 'Agent message received, on the edge facing the sender', line: <Mark sent={false} /> },
  { label: 'Message that just arrived; fades in a few seconds', line: <Line className={edgeTone.message} dash="4 3" tail="dot" head="filled" /> },
  { label: 'Focused session\'s message, acknowledged', line: <Line className={edgeTone.message} tail="dot" head="filled" /> },
  { label: 'Focused session\'s message, not acknowledged', line: <Line className={edgeTone.message} tail="dot" head="hollow" /> },
  { label: 'Edits the same files (selected session)', line: <Line className={edgeTone.overlap} dash="4 2" /> },
]

const strips: { label: string; tone: Tone }[] = [
  { label: 'Needs you', tone: 'needs-you' },
  { label: 'Failed', tone: 'failed' },
  { label: 'Working', tone: 'working' },
  { label: 'Ready for review', tone: 'review' },
  { label: 'Paused', tone: 'paused' },
  { label: 'Waiting', tone: 'waiting' },
  { label: 'Done', tone: 'done' },
  { label: 'Closed', tone: 'closed' },
]

export function Legend() {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm">Legend</Button>
      </PopoverTrigger>
      <PopoverContent aria-label="Lanes legend" align="end" className="w-[min(320px,calc(100vw-16px))]">
        <h3 className="text-ui-sm font-medium">Bars</h3>
        <ul className="mt-1.5 grid gap-1 text-ui-sm">
          {bars.map((bar) => (
            <li key={bar.label} className="flex items-center gap-2">
              <span aria-hidden className={cn('h-3 w-10 shrink-0 rounded-chip', bar.className)} />
              {bar.label}
            </li>
          ))}
        </ul>
        <p className="mt-1.5 text-ui-sm text-muted">A finished session&apos;s working time takes its outcome&apos;s colour. Folded subsession groups and Finished show a strip of the most urgent state at each moment, in this order:</p>
        <ul className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-ui-sm">
          {strips.map((strip) => (
            <li key={strip.label} className="flex items-center gap-1.5">
              <span aria-hidden className={cn('h-1.5 w-5 rounded-full bg-current', toneClasses(strip.tone).text)} />
              {strip.label}
            </li>
          ))}
        </ul>
        <h3 className="mt-3 text-ui-sm font-medium">Messages and overlaps</h3>
        <p className="mt-1.5 text-ui-sm text-muted">Hover, focus or select a session to draw its messages as links; its popover lists them by session.</p>
        <ul className="mt-1.5 grid gap-1 text-ui-sm">
          {lines.map((item) => (
            <li key={item.label} className="flex items-center gap-2">
              {item.line}
              {item.label}
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}

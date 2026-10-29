import type * as React from 'react'
import { cn } from '@/lib/utils'

export type Tone =
  | 'working'
  | 'waiting'
  | 'wait-unknown'
  | 'wait-fault'
  | 'ended'
  | 'needs-you'
  | 'review'
  | 'failed'
  | 'interrupted'
  | 'disconnected'
  | 'done'
  | 'closed'
  | 'paused'
  | 'neutral'

const classes: Record<Tone, { text: string; soft: string }> = {
  working: { text: 'text-state-working', soft: 'bg-state-working-soft' },
  waiting: { text: 'text-muted', soft: 'bg-chrome' },
  'wait-unknown': { text: 'text-muted', soft: 'bg-chrome' },
  'wait-fault': { text: 'text-state-failed', soft: 'bg-state-failed-soft' },
  ended: { text: 'text-muted', soft: 'bg-chrome' },
  disconnected: { text: 'text-muted', soft: 'bg-chrome' },
  'needs-you': { text: 'text-state-needs-you', soft: 'bg-state-needs-you-soft' },
  review: { text: 'text-accent', soft: 'bg-accent-soft' },
  failed: { text: 'text-state-failed', soft: 'bg-state-failed-soft' },
  interrupted: { text: 'text-state-failed', soft: 'bg-state-failed-soft' },
  done: { text: 'text-state-done', soft: 'bg-state-done-soft' },
  closed: { text: 'text-muted', soft: 'bg-chrome' },
  paused: { text: 'text-state-paused', soft: 'bg-state-paused-soft' },
  neutral: { text: 'text-muted', soft: 'bg-chrome' },
}

export function toneClasses(tone: Tone): { text: string; soft: string } {
  return classes[tone]
}

const shapes: Record<Tone, React.ReactNode> = {
  working: <circle cx="5" cy="5" r="4" fill="currentColor" />,
  waiting: (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M5 2.75V5h1.75" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  'wait-unknown': (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" strokeDasharray="2.3 1.135" />
      <path d="M5 2.75V5h1.75" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  'wait-fault': (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M5 2.75V5h1.75" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  ended: <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" strokeDasharray="2.3 1.135" />,
  'needs-you': <circle cx="5" cy="5" r="3.5" fill="none" stroke="currentColor" strokeWidth="2" />,
  review: (
    <>
      <circle cx="5" cy="5" r="5" fill="currentColor" />
      <path d="M2.9 5.1 4.4 6.6 7.1 3.7" fill="none" stroke="var(--canvas)" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  closed: (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M3.25 5h3.5" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" />
    </>
  ),
  interrupted: (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M5 2.9v2.6" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" />
      <circle cx="5" cy="7.1" r="0.7" fill="currentColor" />
    </>
  ),
  disconnected: (
    <>
      <path d="M8.1 3.2A3.75 3.75 0 0 1 5 8.75M1.9 6.8A3.75 3.75 0 0 1 5 1.25" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" />
      <path d="M2 8 8 2" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" />
    </>
  ),
  done: (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M3.2 5.1 4.5 6.4 6.9 3.8" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  failed: (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M3.5 3.5 6.5 6.5M6.5 3.5 3.5 6.5" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" />
    </>
  ),
  paused: (
    <>
      <rect x="2" y="1.5" width="2.25" height="7" rx="0.5" fill="currentColor" />
      <rect x="5.75" y="1.5" width="2.25" height="7" rx="0.5" fill="currentColor" />
    </>
  ),
  neutral: <circle cx="5" cy="5" r="4" fill="none" stroke="currentColor" strokeWidth="1" />,
}

export function StatusDot({
  tone,
  pulse = false,
  label,
  className,
}: {
  tone: Tone
  pulse?: boolean
  label?: string
  className?: string
}) {
  return (
    <svg
      data-slot="status-dot"
      data-tone={tone}
      viewBox="0 0 10 10"
      {...(label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true })}
      className={cn(
        'size-2.5 shrink-0',
        tone === 'neutral' ? 'text-icon-faint' : classes[tone].text,
        pulse && 'state-pulse',
        className,
      )}
    >
      {shapes[tone]}
    </svg>
  )
}

export function StateLine({
  tone,
  label,
  children,
  trailing,
  pulse,
  strong = tone === 'needs-you',
  title,
  className,
}: {
  tone: Tone
  label?: string
  children: React.ReactNode
  trailing?: React.ReactNode
  pulse?: boolean
  /** Full-contrast text, for a row that asks the viewer to act. */
  strong?: boolean
  title?: string
  className?: string
}) {
  return (
    <span data-slot="state-line" title={title} className={cn('flex min-w-0 items-center gap-1.5 text-ui-sm', className)}>
      <StatusDot tone={tone} pulse={pulse} label={label} />
      <span className={cn('min-w-0 truncate', strong ? 'text-text' : 'text-muted')}>{children}</span>
      {trailing !== undefined && <span className="ml-auto shrink-0 text-muted tabular-nums">{trailing}</span>}
    </span>
  )
}

// One event as a feed row, shared by the team activity view and the run
// detail's Events tab so the two feeds cannot drift: actor dot, age, type,
// and the one-line description. The team view also asks for a jump-to-run
// button; the Events tab is already pinned to one run and leaves it off.

import { timeAgo } from '@/lib/format'
import type { Event } from '@/lib/types'
import { useStore } from '@/store'

export function FeedEntry({ event, runLink = false }: { event: Event; runLink?: boolean }) {
  const actor = useStore((s) => s.members[event.actor_id])
  const run = useStore((s) => (runLink ? s.runs[event.run_id] : undefined))
  const navigate = useStore((s) => s.navigate)

  return (
    <li className="flex items-baseline gap-2 rounded-sm px-1 py-0.5 text-xs hover:bg-accent/50">
      <span
        aria-label={actor?.display_name ?? 'system'}
        title={actor?.display_name ?? 'system'}
        className="size-2 shrink-0 translate-y-px rounded-full bg-muted"
        style={actor ? { backgroundColor: actor.color } : undefined}
      />
      <time className="shrink-0 text-muted-foreground" title={event.time}>
        {timeAgo(event.time)}
      </time>
      <span className="shrink-0 text-muted-foreground">{event.type}</span>
      <span className="min-w-0 flex-1 break-words">{describe(event)}</span>
      {run && (
        <button
          type="button"
          onClick={() => navigate('run', { runId: run.id })}
          className="max-w-40 shrink-0 truncate text-muted-foreground hover:text-foreground hover:underline"
        >
          {run.task}
        </button>
      )}
    </li>
  )
}

/** The one line of an event that belongs in a feed. */
function describe(event: Event): string {
  const p = (event.payload ?? {}) as Record<string, unknown>
  switch (event.type) {
    case 'run.status':
      return join([p.to, p.reason])
    case 'run.agent':
      return join([p.kind, p.tool, p.detail])
    case 'run.diff':
      return diffLine(p.files)
    case 'session.timeline':
      return join([p.kind, p.message])
    case 'session.approval':
      return join([p.action, p.decision])
    case 'session.presence':
      return join([p.state])
    case 'run.cost':
      return `${p.input_tokens} in, ${p.output_tokens} out`
    case 'git.branch':
      return join([p.branch, p.commit])
    default:
      return ''
  }
}

function diffLine(files: unknown): string {
  if (!Array.isArray(files)) return ''
  return `${files.length} ${files.length === 1 ? 'file' : 'files'} changed`
}

function join(parts: unknown[]): string {
  return parts.filter(Boolean).map(String).join(' - ')
}

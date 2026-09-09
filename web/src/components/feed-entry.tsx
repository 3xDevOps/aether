// One event as a feed row, shared by the team activity view and the run
// detail's Events tab so the two feeds cannot drift: actor dot, age, type,
// and the one-line description. The team view also asks for a jump-to-run
// button; the Events tab is already pinned to one run and leaves it off.

import { typeLabel, type EventType } from '@/lib/events'
import { budgetStateLabel, money, timeAgo } from '@/lib/format'
import { runLabel } from '@/lib/status'
import type { BudgetState, Event } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
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
      <span className="shrink-0 text-muted-foreground" title={event.type}>
        {typeLabel(event.type)}
      </span>
      <span className="min-w-0 flex-1 break-words">{describe(event)}</span>
      {run && (
        <button
          type="button"
          onClick={() => navigate('terminal', { runId: run.id })}
          className={cn(
            focusRing,
            'max-w-40 shrink-0 truncate text-muted-foreground hover:text-foreground hover:underline',
          )}
        >
          {runLabel(run)}
        </button>
      )}
    </li>
  )
}

/**
 * The one line of an event that belongs in a feed. Keyed by `EventType`, so a
 * describer without a name in `eventLabel`, or a name without a describer, is a
 * compile error rather than a row that renders half of itself.
 */
const describers: Record<EventType, (p: Record<string, unknown>) => string> = {
  'run.status': (p) => join([p.to, p.reason]),
  'run.deleted': () => 'record removed',
  'run.protected': (p) => (p.protected ? 'protected' : 'unprotected'),
  'run.title': (p) => String(p.title ?? ''),
  'run.agent': (p) => join([p.kind, p.tool, p.detail]),
  'run.diff': (p) => suffix(fileCount(p.files), 'changed'),
  'run.cost': (p) => `${p.input_tokens} in, ${p.output_tokens} out`,
  'run.overlap': (p) => overlapLine(p.with),
  'workspace.timeline': (p) => join([p.kind, p.message]),
  'workspace.approval': (p) => join([p.action, p.decision]),
  'workspace.presence': (p) => join([p.state]),
  'workspace.budget': (p) => budgetLine(p),
  'git.branch': (p) => join([p.branch, p.commit]),
  'sync.conflict': (p) => suffix(fileCount(p.files), 'in conflict'),
  'server.update': (p) => join([p.phase, p.version, p.detail]),
}

function describe(event: Event): string {
  if (!Object.hasOwn(describers, event.type)) return ''
  return describers[event.type as EventType]((event.payload ?? {}) as Record<string, unknown>)
}

// An absent or empty `with` means the run's overlaps cleared.
function overlapLine(peers: unknown): string {
  if (!Array.isArray(peers) || peers.length === 0) return 'no longer overlapping'
  return `${peers.length} ${peers.length === 1 ? 'run' : 'runs'} in the same files`
}

// The spend is a floor whenever a run went unmetered, so it is rendered as one.
function budgetLine(p: Record<string, unknown>): string {
  const spend = money.format(Number(p.spend_usd ?? 0))
  const floor = Number(p.unmetered_runs ?? 0) > 0 ? '+' : ''
  const cap = Number(p.limit_usd ?? 0)
  const state = budgetStateLabel[p.state as BudgetState] ?? p.state
  const of = cap > 0 ? ` of ${money.format(cap)}` : ''
  return join([state, `${spend}${floor}${of}`, p.reason])
}

function suffix(count: string, tail: string): string {
  return count ? `${count} ${tail}` : ''
}

function fileCount(files: unknown): string {
  if (!Array.isArray(files)) return ''
  return `${files.length} ${files.length === 1 ? 'file' : 'files'}`
}

function join(parts: unknown[]): string {
  return parts.filter(Boolean).map(String).join(' - ')
}

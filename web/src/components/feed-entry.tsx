// One event as a feed row, shared by the team activity view and the run
// detail's Events tab so the two feeds cannot drift: actor dot, age, type,
// and the description. The team view also asks for a jump-to-run button;
// the Events tab is already pinned to one run and leaves it off.

import type { ReactNode } from 'react'
import { Chip } from '@/components/ui/heroui'
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
    <li className="group grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-x-2 border-b border-border px-3 py-2 text-[13px] transition-colors last:border-b-0 hover:bg-toolbar-hover">
      <span
        role="img"
        aria-label={actor?.display_name ?? 'system'}
        title={actor?.display_name ?? 'system'}
        className="mt-1 size-2 shrink-0 rounded-full bg-muted-foreground/45"
        style={actor ? { backgroundColor: actor.color } : undefined}
      />
      <div className="@container/feed-entry min-w-0">
        <div className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-x-2 gap-y-1 @md/feed-entry:grid-cols-[auto_minmax(0,1fr)_minmax(8rem,14rem)]">
          <time
            className="shrink-0 pt-px text-xs tabular-nums text-muted-foreground"
            title={event.time}
          >
            {timeAgo(event.time)}
          </time>
          <span title={event.type} className="min-w-0 max-w-full">
            <Chip
              color="default"
              variant="tertiary"
              size="sm"
              className="min-w-0 max-w-full justify-self-start"
            >
              <Chip.Label className="truncate">{typeLabel(event.type)}</Chip.Label>
            </Chip>
          </span>
          <span className="col-start-2 min-w-0 break-words leading-5 text-foreground/90 select-text">
            {describe(event)}
          </span>
          {run && (
            <button
              type="button"
              onClick={() => navigate('terminal', { runId: run.id })}
              aria-label={runLabel(run)}
              title={runLabel(run)}
              className={cn(
                focusRing,
                'col-start-2 row-start-3 min-h-[26px] coarse:min-h-11 min-w-0 max-w-full justify-self-start break-words text-left text-xs text-muted-foreground hover:text-foreground hover:underline @md/feed-entry:col-start-3 @md/feed-entry:row-start-1 @md/feed-entry:row-span-2 @md/feed-entry:justify-self-end',
              )}
            >
              <span className="line-clamp-2">{runLabel(run)}</span>
            </button>
          )}
        </div>
      </div>
    </li>
  )
}

/**
 * The one line of an event that belongs in a feed. Keyed by `EventType`, so a
 * describer without a name in `eventLabel`, or a name without a describer, is a
 * compile error rather than a row that renders half of itself.
 */
const describers: Record<EventType, (p: Record<string, unknown>) => ReactNode> = {
  'run.status': (p) => join([p.to, p.reason]),
  'run.deleted': () => 'record removed',
  'run.protected': (p) => (p.protected ? 'protected' : 'unprotected'),
  'run.title': (p) => String(p.title ?? ''),
  'run.agent': (p) => join([p.kind, p.tool, p.detail]),
  'run.diff': (p) => suffix(fileCount(p.files), 'changed'),
  'run.cost': (p) => `${p.input_tokens} in, ${p.output_tokens} out`,
  'run.overlap': (p) => overlapLine(p.with),
  'workspace.timeline': (p) => timelineLine(p),
  'workspace.approval': (p) => join([p.action, p.decision]),
  'workspace.presence': (p) => join([p.state]),
  'workspace.budget': (p) => budgetLine(p),
  'git.branch': (p) => join([p.branch, p.commit]),
  'sync.conflict': (p) => suffix(fileCount(p.files), 'in conflict'),
  'server.update': (p) => join([p.phase, p.version, p.detail]),
  'workspace.room_message': (p) => join([p.kind, p.state, p.message_id]),
  'workspace.evidence_packet': (p) => join([p.trigger, p.packet_id]),
}

function describe(event: Event): ReactNode {
  if (!Object.hasOwn(describers, event.type)) return ''
  return describers[event.type as EventType]((event.payload ?? {}) as Record<string, unknown>)
}

function timelineLine(p: Record<string, unknown>): ReactNode {
  if (p.kind !== 'report') return join([p.kind, p.message])
  const outcome = boundedText(p.outcome, 64)
  const summary = boundedText(p.summary, 512)
  const nextAction = boundedText(p.next_action, 512)
  const reportID = boundedText(p.report_id, 128)
  const refs = Array.isArray(p.evidence_refs)
    ? p.evidence_refs
        .filter((ref): ref is string => typeof ref === 'string' && ref.length > 0)
        .slice(0, 8)
        .map((ref) => boundedText(ref, 128))
    : []
  return (
    <span className="inline-flex max-w-full flex-wrap gap-x-2 gap-y-1">
      {reportID && <span>Report: <code>{reportID}</code></span>}
      {outcome && <span>Outcome: {outcome}</span>}
      {summary && <span>Summary: {summary}</span>}
      {nextAction && <span>Next action: {nextAction}</span>}
      {refs.length > 0 && (
        <span>
          Evidence: {refs.map((ref, index) => (
            <code key={`${ref}-${index}`} className="mr-1">{ref}</code>
          ))}
        </span>
      )}
    </span>
  )
}

function boundedText(value: unknown, max: number): string {
  if (typeof value !== 'string') return ''
  return value.length > max ? `${value.slice(0, max)}…` : value
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

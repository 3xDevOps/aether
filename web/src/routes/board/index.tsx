import { CheckCheck, Eye, EyeOff } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { timeAgo } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { registerRoute } from '@/routes/registry'
import { RunCard } from '@/routes/board/run-card'
import {
  bucketLabel,
  useBoard,
  type BoardColumn,
  type IdleSession,
} from '@/routes/board/selectors'
import { useStore } from '@/store'
import '@/components/palette'

/**
 * The default centre view: run cards in Orca's four buckets. Idle holds the
 * sessions with nothing active rather than runs, and stays hidden until asked
 * for.
 */
export function Board() {
  const { columns, idle } = useBoard()
  const showIdle = useStore((s) => s.showIdle)
  const toggleIdle = useStore((s) => s.toggleIdle)
  const ackAll = useStore((s) => s.ackAll)
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const unreachable = error !== null
  const total = columns.reduce((n, c) => n + c.cards.length, 0)
  const loading = useDelayed(!hydrated && !unreachable && total === 0)

  return (
    <div className="flex h-full flex-col">
      <header className="flex items-center gap-2 border-b px-4 py-2">
        <h1 className="text-sm font-medium">Run board</h1>
        <span className="text-xs text-muted-foreground">
          {total} {total === 1 ? 'run' : 'runs'}
        </span>
        <div className="ml-auto flex items-center gap-1">
          <Button variant="ghost" size="sm" onClick={ackAll} title="Mark every run seen">
            <CheckCheck />
            Mark all seen
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={toggleIdle}
            aria-pressed={showIdle}
            title={`${showIdle ? 'Hide' : 'Show'} idle sessions`}
          >
            {showIdle ? <EyeOff /> : <Eye />}
            Idle
          </Button>
        </div>
      </header>

      {unreachable && total === 0 ? (
        <p className="p-4 text-sm text-muted-foreground">
          {dead ? error : 'Cannot reach the server. Retrying.'}
        </p>
      ) : (
        <div className="flex min-h-0 flex-1 gap-3 overflow-x-auto p-3">
          {columns.map((column) => (
            <Column key={column.key} column={column} loading={loading} />
          ))}
          {showIdle && <IdleColumn sessions={idle} />}
        </div>
      )}
    </div>
  )
}

function Column({ column, loading }: { column: BoardColumn; loading: boolean }) {
  return (
    <section className="flex min-h-0 w-72 min-w-72 flex-col" aria-label={column.label}>
      <ColumnHeader label={column.label} count={column.cards.length} />
      <div className="flex-1 space-y-2 overflow-y-auto pr-1">
        {column.cards.map((card) => (
          <RunCard key={card.run.id} card={card} />
        ))}
        {column.cards.length === 0 &&
          (loading ? (
            <>
              <Skeleton className="h-20 w-full" />
              <Skeleton className="h-20 w-full" />
            </>
          ) : (
            <p className="px-1 text-xs text-muted-foreground">Nothing here.</p>
          ))}
      </div>
    </section>
  )
}

function IdleColumn({ sessions }: { sessions: IdleSession[] }) {
  const navigate = useStore((s) => s.navigate)
  return (
    <section className="flex min-h-0 w-72 min-w-72 flex-col" aria-label={bucketLabel.idle}>
      <ColumnHeader label={bucketLabel.idle} count={sessions.length} />
      <div className="flex-1 space-y-2 overflow-y-auto pr-1">
        {sessions.map(({ session, runs, changedAt }) => (
          <button
            key={session.id}
            type="button"
            onClick={() => navigate('session', { sessionId: session.id })}
            className="w-full rounded-md border bg-card p-2 text-left opacity-90 shadow-xs transition-colors hover:bg-accent/50"
          >
            <span className="block truncate text-sm">{session.name}</span>
            <span className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
              <span className="truncate">{session.base_branch}</span>
              <span className="ml-auto shrink-0">
                {runs === 0 ? 'no runs' : `${runs} done`} · {timeAgo(changedAt)}
              </span>
            </span>
          </button>
        ))}
        {sessions.length === 0 && (
          <p className="px-1 text-xs text-muted-foreground">No idle sessions.</p>
        )}
      </div>
    </section>
  )
}

function ColumnHeader({ label, count }: { label: string; count: number }) {
  return (
    <h2 className="mb-2 flex items-center gap-2 px-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
      {label}
      <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] normal-case">
        {count}
      </span>
    </h2>
  )
}

registerRoute('board', Board)

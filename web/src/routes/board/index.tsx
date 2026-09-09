import { CheckCheck, Rocket } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { canLaunch } from '@/lib/commands'
import { useDelayed } from '@/lib/hooks'
import { registerRoute } from '@/routes/registry'
import { TerminalDock } from '@/routes/board/terminal-dock'
import { RunCard } from '@/routes/board/run-card'
import { useBoard, type BoardColumn } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { useCapability, useSelfRole } from '@/store/hooks'
import '@/components/palette'

/** The default centre view: the active workspace's run cards in three buckets. */
export function Board() {
  const { columns } = useBoard()
  const ackAll = useStore((s) => s.ackAll)
  const caps = useCapability()
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const unreachable = error !== null
  const total = columns.reduce((n, c) => n + c.cards.length, 0)
  const loading = useDelayed(!hydrated && !unreachable && total === 0)
  // Nothing to sort into buckets, and nothing still on its way.
  const empty = hydrated && total === 0
  const placeholder = loading ? 'skeleton' : hydrated ? 'empty' : 'none'

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-9 items-center gap-2 border-b px-4">
        <h1 className="text-sm font-medium">Board</h1>
        <span className="text-xs text-muted-foreground">
          {total} {total === 1 ? 'run' : 'runs'}
        </span>
        <div className="ml-auto flex items-center gap-1">
          <NewRunButton />
          <Button
            variant="ghost"
            size="sm"
            onClick={ackAll}
            title="Mark every run seen"
          >
            <CheckCheck />
            Mark all seen
          </Button>
        </div>
      </header>

      <div className="flex min-h-0 flex-1 flex-col">
        {unreachable && total === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            {dead ? error : 'Cannot reach the server. Retrying.'}
          </p>
        ) : empty ? (
          <EmptyNotice />
        ) : (
          <div className="flex min-h-0 flex-1 gap-3 overflow-x-auto p-3">
            {columns.map((column) => (
              <Column key={column.key} column={column} placeholder={placeholder} />
            ))}
          </div>
        )}
        {caps.hasWS('terminal') && <TerminalDock />}
      </div>
    </div>
  )
}

/**
 * The way in, wherever a member is looking. The launch form is hosted
 * app-wide, so asking the store to open it is the whole of it; a member who
 * cannot start a run is not offered the button.
 */
function NewRunButton({
  variant = 'ghost',
  size = 'sm',
}: {
  variant?: 'ghost' | 'default'
  size?: 'sm' | 'default'
}) {
  const openDialog = useStore((s) => s.openPaletteDialog)
  const cap = useCapability()
  const role = useSelfRole()
  if (!canLaunch({ cap, role })) return null
  return (
    <Button
      variant={variant}
      size={size}
      title="Launch a run"
      onClick={() => openDialog('launch')}
    >
      <Rocket />
      New run
    </Button>
  )
}

/** What an empty workspace says, in place of the columns. */
function EmptyNotice() {
  return (
    <div className="flex min-h-0 flex-1 flex-col items-start gap-3 p-6">
      <p className="max-w-prose text-sm text-muted-foreground">
        No runs yet. A run is one agent working on its own branch of this
        workspace, in its own container.
      </p>
      <NewRunButton variant="default" size="default" />
    </div>
  )
}

function Column({
  column,
  placeholder,
}: {
  column: BoardColumn
  placeholder: 'skeleton' | 'empty' | 'none'
}) {
  return (
    <section className="flex min-h-0 w-72 min-w-72 flex-col" aria-label={column.label}>
      <ColumnHeader label={column.label} count={column.cards.length} />
      <div className="flex-1 space-y-2 overflow-y-auto pr-1">
        {column.cards.map((card) => (
          <RunCard key={card.run.id} card={card} />
        ))}
        {column.cards.length === 0 && placeholder === 'skeleton' && (
          <>
            <Skeleton className="h-20 w-full" />
            <Skeleton className="h-20 w-full" />
          </>
        )}
        {column.cards.length === 0 && placeholder === 'empty' && (
          <p className="px-1 text-xs text-muted-foreground">Nothing here.</p>
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

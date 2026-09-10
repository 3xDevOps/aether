import { CheckCheck, Rocket } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
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
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
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
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title="Board"
        titleAdornment={
          <Chip color="default" variant="soft" size="sm">
            <Chip.Label>
              {total} {total === 1 ? 'run' : 'runs'}
            </Chip.Label>
          </Chip>
        }
        subtitle={
          workspace ? `${workspace.name} · base ${workspace.base_branch}` : 'All workspaces'
        }
        actions={
          <>
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
          </>
        }
      />

      <div className="flex min-h-0 flex-1 flex-col">
        {unreachable && total === 0 ? (
          <div className="m-4 rounded-lg border border-state-failed/30 bg-state-failed/10 p-4 sm:m-6">
            <p className="text-sm text-state-failed">
              {dead ? error : 'Cannot reach the server. Retrying.'}
            </p>
          </div>
        ) : empty ? (
          <EmptyNotice />
        ) : (
          <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 overflow-y-auto p-4 sm:p-6 md:grid-cols-3 md:overflow-hidden">
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
    <div className="flex min-h-0 flex-1 items-center justify-center p-4 sm:p-6">
      <div className="w-full max-w-xl rounded-lg border bg-card p-6 shadow-xs sm:p-8">
        <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
          Ready for a task
        </p>
        <h2 className="mt-2 text-xl font-semibold tracking-tight">No runs yet</h2>
        <p className="mt-2 max-w-prose text-sm leading-6 text-muted-foreground">
          A run is one agent working on its own branch of this workspace, in its
          own container.
        </p>
        <div className="mt-5">
          <NewRunButton variant="default" size="default" />
        </div>
      </div>
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
    <section
      className="flex min-w-0 flex-col rounded-lg border bg-muted/30 p-3 md:min-h-0"
      aria-label={column.label}
    >
      <ColumnHeader label={column.label} count={column.cards.length} />
      <div className="space-y-2 md:min-h-0 md:flex-1 md:overflow-y-auto md:pr-1">
        {column.cards.map((card) => (
          <RunCard key={card.run.id} card={card} />
        ))}
        {column.cards.length === 0 && placeholder === 'skeleton' && (
          <>
            <Skeleton className="h-28 rounded-md" />
            <Skeleton className="h-28 rounded-md" />
          </>
        )}
        {column.cards.length === 0 && placeholder === 'empty' && (
          <p className="rounded-md border border-dashed px-3 py-4 text-[13px] text-muted-foreground">
            Nothing here.
          </p>
        )}
      </div>
    </section>
  )
}

function ColumnHeader({ label, count }: { label: string; count: number }) {
  return (
    <header className="mb-3 flex items-center justify-between gap-3 border-b pb-3">
      <h2 className="text-sm font-semibold tracking-tight">{label}</h2>
      <Chip color="default" variant="tertiary" size="sm" aria-label={`${count} runs`}>
        <Chip.Label>{count}</Chip.Label>
      </Chip>
    </header>
  )
}

registerRoute('board', Board)

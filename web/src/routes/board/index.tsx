import { CheckCheck, Rocket } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Chip, Tooltip } from '@/components/ui/heroui'
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
            <Tooltip>
              <Tooltip.Trigger<'button'>
                render={(triggerProps) => (
                  <Button
                    {...triggerProps}
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      ackAll()
                    }}
                  >
                    <CheckCheck />
                    Mark all seen
                  </Button>
                )}
              />
              <Tooltip.Content>Mark every run seen</Tooltip.Content>
            </Tooltip>
          </>
        }
      />

      <div className="flex min-h-0 flex-1 flex-col overflow-x-hidden overflow-y-auto">
        <div className="min-h-24 min-w-0 flex-1 overflow-y-auto">
          {unreachable && total === 0 ? (
            <div
              role="alert"
              className="border-b border-state-failed/35 bg-state-failed/10 px-4 py-2 text-[13px] leading-5 text-state-failed"
            >
              <span className="break-words whitespace-pre-wrap">
                {dead ? error : 'Cannot reach the server. Retrying.'}
              </span>
            </div>
          ) : empty ? (
            <EmptyNotice />
          ) : (
            <div className="grid min-h-0 flex-1 grid-cols-1 overflow-y-auto lg:grid-cols-3 lg:overflow-hidden">
              {columns.map((column) => (
                <Column key={column.key} column={column} placeholder={placeholder} />
              ))}
            </div>
          )}
        </div>
        {caps.hasWS('terminal') && <TerminalDock containment="parent" />}
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
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <Button
            {...triggerProps}
            variant={variant}
            size={size}
            onClick={() => {
              openDialog('launch')
            }}
          >
            <Rocket />
            New run
          </Button>
        )}
      />
      <Tooltip.Content>Launch a run</Tooltip.Content>
    </Tooltip>
  )
}

/** What an empty workspace says, in place of the columns. */
function EmptyNotice() {
  return (
    <div className="flex min-h-0 flex-1 items-start p-4 sm:p-6">
      <div className="w-full max-w-2xl border-y border-border px-4 py-5">
        <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
          Ready for a task
        </p>
        <h2 className="mt-1.5 text-[16px] font-semibold leading-5">No runs yet</h2>
        <p className="mt-1.5 max-w-prose text-[13px] leading-5 text-muted-foreground">
          A run is one agent working on its own branch of this workspace, in its
          own container.
        </p>
        <div className="mt-4">
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
      className="flex min-w-0 flex-col border-b border-border bg-sidebar/35 last:border-b-0 lg:min-h-0 lg:border-b-0 lg:border-r lg:last:border-r-0"
      aria-label={column.label}
    >
      <ColumnHeader label={column.label} count={column.cards.length} />
      <div className="min-h-0 flex-1 lg:overflow-y-auto">
        {column.cards.map((card) => (
          <RunCard key={card.run.id} card={card} />
        ))}
        {column.cards.length === 0 && placeholder === 'skeleton' && (
          <>
            <Skeleton className="h-20 rounded-none border-b" />
            <Skeleton className="h-20 rounded-none border-b" />
          </>
        )}
        {column.cards.length === 0 && placeholder === 'empty' && (
          <p className="border-b border-dashed px-3 py-3 text-[13px] text-muted-foreground">
            Nothing here.
          </p>
        )}
      </div>
    </section>
  )
}

function ColumnHeader({ label, count }: { label: string; count: number }) {
  return (
    <header className="flex min-h-[35px] shrink-0 items-center justify-between gap-3 border-b border-border px-3">
      <h2 className="text-[13px] font-semibold leading-5">{label}</h2>
      <Chip color="default" variant="tertiary" size="sm" aria-label={`${count} runs`}>
        <Chip.Label>{count}</Chip.Label>
      </Chip>
    </header>
  )
}

registerRoute('board', Board)

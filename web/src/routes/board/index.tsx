import { Archive, PackageX, Rocket } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Chip, Tooltip } from '@/components/ui/heroui'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import {
  canLaunch,
  clearDonePlan,
  releaseFinishedPlan,
  runClearDone,
  runReleaseFinished,
  type ClearDonePlan,
  type ReleaseFinishedPlan,
} from '@/lib/commands'
import { useDelayed } from '@/lib/hooks'
import { registerRoute } from '@/routes/registry'
import { ClearDoneConfirm, ReleaseFinishedConfirm } from '@/routes/board/clear-done-dialog'
import { TerminalDock } from '@/routes/board/terminal-dock'
import { RunCard } from '@/routes/board/run-card'
import { allRuns, cardRuns, finishedRuns, useBoard, type BoardColumn } from '@/routes/board/selectors'
import { RunMap } from '@/routes/board/run-map'
import { useBoardTransition } from '@/routes/board/use-board-transition'
import { useStore } from '@/store'
import { useCapability, useSelf, useSelfRole } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import '@/components/palette'

/** The active workspace's runs, as status columns or a spatial workbench. */
export function Board() {
  const data = useBoard()
  const { columns, archivedCards } = data
  const removeRun = useStore((s) => s.removeRun)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const workspaces = useStore((s) => s.workspaces)
  const boardView = useStore((s) => s.boardView)
  const setBoardView = useStore((s) => s.setBoardView)
  const { boardRef, changeView } = useBoardTransition(
    boardView,
    setBoardView,
    activeWorkspace || 'all',
  )
  const caps = useCapability()
  const self = useSelf()
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const unreachable = error !== null
  // An all-archived scope still needs the archive toggle in either layout.
  const total = columns.reduce((n, c) => n + c.cards.length, 0) + archivedCards.length
  const loading = useDelayed(!hydrated && !unreachable && total === 0)
  // Nothing to sort into buckets, and nothing still on its way.
  const empty = hydrated && total === 0
  const placeholder = loading ? 'skeleton' : hydrated ? 'empty' : 'none'

  const donePlan = clearDonePlan(finishedRuns(data, workspaces), caps, self)
  const runClear = (eligible: RunRecord[]) => runClearDone(eligible, { api, removeRun })
  const releasePlan = releaseFinishedPlan(allRuns(data, workspaces), caps, self)

  const [showArchived, setShowArchived] = useState(false)
  // The toggle only exists while there is something behind it; once the
  // last archived run leaves (restored, or later swept), fall back to Finished.
  useEffect(() => {
    if (archivedCards.length === 0) setShowArchived(false)
  }, [archivedCards.length])
  // A workspace switch starts the new board on Finished, not on whatever the
  // previous workspace's toggle was left showing.
  useEffect(() => {
    setShowArchived(false)
  }, [activeWorkspace])
  const visibleColumns = columns.map((column) =>
    column.key === 'finished' && showArchived ? { ...column, cards: archivedCards } : column,
  )
  const archivedToggle = {
    count: archivedCards.length,
    showing: showArchived,
    onToggle: setShowArchived,
  }
  const clearDone = showArchived ? undefined : { plan: donePlan, onRun: runClear }
  const releaseFinished = {
    plan: releasePlan,
    onRun: (eligible: RunRecord[]) => runReleaseFinished(eligible, { api }),
  }
  const mapHeader = (controls?: ReactNode) => (
    <ColumnHeader
      label={showArchived ? 'Runs · archived' : 'Runs'}
      count={visibleColumns.reduce((count, column) => count + column.cards.length, 0)}
      archived={archivedToggle}
      clearDone={clearDone}
      releaseFinished={releaseFinished}
      controls={controls}
    />
  )

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title="Board"
        titleAdornment={
          <>
            <div
              role="group"
              aria-label="Board layout"
              className="inline-flex shrink-0 items-center border border-border p-0.5"
            >
              {(['cards', 'map'] as const).map((view) => (
                <Button
                  key={view}
                  variant="ghost"
                  size="sm"
                  aria-pressed={boardView === view}
                  className="rounded-none px-2 aria-pressed:bg-selection aria-pressed:text-selection-foreground"
                  onClick={() => changeView(view)}
                >
                  {view === 'cards' ? 'Cards' : 'Map'}
                </Button>
              ))}
            </div>
            <Chip color="default" variant="soft" size="sm">
              <Chip.Label>
                {total} {total === 1 ? 'run' : 'runs'}
              </Chip.Label>
            </Chip>
          </>
        }
        subtitle={
          workspace ? `${workspace.name} · base ${workspace.base_branch}` : 'All workspaces'
        }
      />

      <div className="flex min-h-0 flex-1 flex-col overflow-x-hidden overflow-y-auto">
        <div
          ref={boardRef}
          className={`flex min-h-24 min-w-0 flex-1 flex-col overflow-x-hidden ${
            empty || (unreachable && total === 0) ? 'overflow-y-auto' : 'overflow-y-hidden'
          }`}
        >
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
          ) : boardView === 'map' ? (
            <div className="flex min-h-0 min-w-0 flex-1 flex-col">
              {loading ? (
                <>
                  {mapHeader()}
                  <div role="status" aria-label="Loading runs" className="flex min-h-0 flex-1 gap-4 overflow-hidden p-4">
                    <Skeleton className="h-48 w-80 max-w-full shrink-0 rounded-none" />
                    <Skeleton className="h-48 w-80 shrink-0 rounded-none" />
                  </div>
                </>
              ) : (
                <RunMap
                  cards={cardRuns(visibleColumns.flatMap((column) => column.cards))}
                  scope={activeWorkspace || 'all'}
                  renderHeader={mapHeader}
                />
              )}
            </div>
          ) : (
            <div className="grid min-h-0 flex-1 grid-cols-1 overflow-y-auto lg:grid-cols-3 lg:grid-rows-[auto_minmax(0,1fr)] lg:overflow-hidden">
              {visibleColumns.map((column) =>
                column.key === 'finished' ? (
                  <Column
                    key={column.key}
                    column={column}
                    placeholder={placeholder}
                    archived={archivedToggle}
                    clearDone={clearDone}
                    releaseFinished={releaseFinished}
                  />
                ) : (
                  <Column key={column.key} column={column} placeholder={placeholder} />
                ),
              )}
            </div>
          )}
        </div>
        {caps.hasWS('terminal') && <TerminalDock containment="parent" />}
      </div>
    </div>
  )
}

/** The empty-board CTA opens the app-wide launch form when permitted. */
function NewRunButton() {
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
            variant="default"
            size="default"
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
  const navigate = useStore((s) => s.navigate)
  const caps = useCapability()
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  return (
    <div className="flex min-h-0 flex-1 items-start p-4 sm:p-6">
      <div className="w-full max-w-2xl border-y border-border px-4 py-5">
        <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
          {workspace ? 'Workspace setup and runs' : 'Add your repository'}
        </p>
        <h2 className="mt-1.5 text-[16px] font-semibold leading-5">{workspace ? 'No runs yet' : 'No workspace selected'}</h2>
        <p className="mt-1.5 max-w-prose text-[13px] leading-5 text-muted-foreground">
          A run is one agent working on its own branch of this workspace, in its
          own container.
        </p>
        <div className="mt-4 flex flex-wrap gap-2">
          {workspace && <NewRunButton />}
          {caps.hasMethod('workspace.list') && <Button variant="outline" onClick={() => navigate('workspaces')}>Add or manage workspaces</Button>}
          {caps.hasMethod('agent.list') && <Button variant="outline" onClick={() => navigate('onboarding')}>Set up repository and agents</Button>}
        </div>
      </div>
    </div>
  )
}

/** The Finished header's toggle between finished runs and its archived ones. */
interface ArchivedToggle {
  count: number
  showing: boolean
  onToggle: (showing: boolean) => void
}

/** The Finished header's bulk-archive action, hidden while archived.showing. */
interface ClearDoneAction {
  plan: ClearDonePlan
  onRun: (eligible: RunRecord[]) => Promise<void>
}

interface ReleaseFinishedAction {
  plan: ReleaseFinishedPlan
  onRun: (eligible: RunRecord[]) => Promise<void>
}

function Column({
  column,
  placeholder,
  archived,
  clearDone,
  releaseFinished,
}: {
  column: BoardColumn
  placeholder: 'skeleton' | 'empty' | 'none'
  archived?: ArchivedToggle
  clearDone?: ClearDoneAction
  releaseFinished?: ReleaseFinishedAction
}) {
  return (
    <section
      className="flex min-w-0 flex-col border-b border-border bg-sidebar/35 last:border-b-0 lg:row-span-2 lg:grid lg:min-h-0 lg:grid-rows-subgrid lg:border-b-0 lg:border-r lg:last:border-r-0"
      aria-label={column.label}
    >
      <ColumnHeader
        label={column.label}
        count={column.cards.length}
        archived={archived}
        clearDone={clearDone}
        releaseFinished={releaseFinished}
      />
      <div className="min-h-0 flex-1 lg:overflow-y-auto">
        {column.cards.map((card) => (
          <RunCard key={card.run.id} run={card.run} state={card.state} reason={card.reason} swarm={card.swarm} />
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

function ColumnHeader({
  label,
  count,
  archived,
  clearDone,
  controls,
  releaseFinished,
}: {
  label: string
  count: number
  archived?: ArchivedToggle
  clearDone?: ClearDoneAction
  releaseFinished?: ReleaseFinishedAction
  controls?: ReactNode
}) {
  const heading = useRef<HTMLHeadingElement>(null)
  const actions = useRef<HTMLDivElement>(null)
  // A completed bulk action may remove its own button. Once Radix closes its
  // dialog, restore focus to the Archived toggle or this header.
  const takeFocus = () => {
    if (document.activeElement !== document.body) return
    const toggle = actions.current?.querySelector<HTMLElement>('[aria-pressed]')
    ;(toggle ?? heading.current)?.focus()
  }

  return (
    <header className="flex min-h-[35px] shrink-0 flex-wrap items-center justify-between gap-x-2 gap-y-1 border-b border-border px-3 py-1 coarse:min-h-[53px]">
      <h2
        ref={heading}
        tabIndex={-1}
        className="min-w-0 truncate text-[13px] font-semibold leading-5"
      >
        {label}
      </h2>
      {controls}
      <div ref={actions} className="flex max-w-full flex-wrap items-center gap-1.5">
        {clearDone && <ClearDoneButton {...clearDone} onClosed={takeFocus} />}
        {releaseFinished && <ReleaseFinishedButton {...releaseFinished} onClosed={takeFocus} />}
        {archived && archived.count > 0 && (
          <Tooltip>
            <Tooltip.Trigger<'button'>
              render={(triggerProps) => (
                <Button
                  {...triggerProps}
                  variant={archived.showing ? 'secondary' : 'ghost'}
                  size="sm"
                  className="h-[22px] min-h-[22px] px-1.5 text-xs"
                  aria-pressed={archived.showing}
                  onClick={() => archived.onToggle(!archived.showing)}
                >
                  <Archive className="size-3" aria-hidden />
                  Archived {archived.count}
                </Button>
              )}
            />
            <Tooltip.Content>
              {archived.showing ? 'Back to Finished' : 'Show archived runs'}
            </Tooltip.Content>
          </Tooltip>
        )}
        <Chip color="default" variant="tertiary" size="sm" aria-label={`${count} runs`}>
          <Chip.Label>{count}</Chip.Label>
        </Chip>
      </div>
    </header>
  )
}

/**
 * Archives eligible Finished runs behind a snapshot confirmation. `plan` tracks
 * Finished while idle; the open dialog remains stable if runs change beneath it.
 */
function ClearDoneButton({
  plan,
  onRun,
  onClosed,
}: ClearDoneAction & { onClosed: () => void }) {
  const [open, setOpen] = useState(false)
  const [running, setRunning] = useState(false)
  const [snapshot, setSnapshot] = useState<ClearDonePlan | null>(null)
  const wasOpen = useRef(false)

  // Fires once the render that closes the dialog has committed, so
  // `onClosed` finds the DOM - the Archived toggle included - already
  // reflecting whatever this run archived.
  useEffect(() => {
    if (wasOpen.current && !open) onClosed()
    wasOpen.current = open
  }, [open, onClosed])

  const openConfirm = () => {
    setSnapshot(plan)
    setOpen(true)
  }

  const confirm = async () => {
    if (!snapshot) return
    setRunning(true)
    await onRun(snapshot.eligible)
    setRunning(false)
    setOpen(false)
  }

  return (
    <>
      {plan.eligible.length > 0 && (
        <Tooltip>
          <Tooltip.Trigger<'button'>
            render={(triggerProps) => (
              <Button
                {...triggerProps}
                variant="ghost"
                size="sm"
                className="h-[22px] min-h-[22px] px-1.5 text-xs"
                onClick={openConfirm}
              >
                <Archive className="size-3" aria-hidden />
                Archive closed runs...
              </Button>
            )}
          />
          <Tooltip.Content>Archive hides runs and schedules deletion; it does not free memory</Tooltip.Content>
        </Tooltip>
      )}
      {open && snapshot && (
        <ClearDoneConfirm
          plan={snapshot}
          running={running}
          onConfirm={() => void confirm()}
          onCancel={() => setOpen(false)}
        />
      )}
    </>
  )
}

/** Releases eligible retained containers without hiding their run records. */
function ReleaseFinishedButton({
  plan,
  onRun,
  onClosed,
}: ReleaseFinishedAction & { onClosed: () => void }) {
  const [open, setOpen] = useState(false)
  const [running, setRunning] = useState(false)
  const [snapshot, setSnapshot] = useState<ReleaseFinishedPlan | null>(null)
  const wasOpen = useRef(false)

  useEffect(() => {
    if (wasOpen.current && !open) onClosed()
    wasOpen.current = open
  }, [open, onClosed])

  const confirm = async () => {
    if (!snapshot) return
    setRunning(true)
    await onRun(snapshot.eligible)
    setRunning(false)
    setOpen(false)
  }

  return (
    <>
      {plan.eligible.length > 0 && (
        <Tooltip>
          <Tooltip.Trigger<'button'>
            render={(triggerProps) => (
              <Button
                {...triggerProps}
                variant="ghost"
                size="sm"
                className="h-[22px] min-h-[22px] px-1.5 text-xs"
                onClick={() => {
                  setSnapshot(plan)
                  setOpen(true)
                }}
              >
                <PackageX className="size-3" aria-hidden />
                Release finished resources...
              </Button>
            )}
          />
          <Tooltip.Content>Free retained containers without archiving run history</Tooltip.Content>
        </Tooltip>
      )}
      {open && snapshot && (
        <ReleaseFinishedConfirm
          plan={snapshot}
          running={running}
          onConfirm={() => void confirm()}
          onCancel={() => setOpen(false)}
        />
      )}
    </>
  )
}

registerRoute('board', Board)

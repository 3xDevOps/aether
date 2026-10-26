import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Columns3, Ellipsis, List, Network } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { SysboxNudge } from '@/components/sysbox-nudge'
import { Callout } from '@/components/ui/callout'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ViewHeader } from '@/components/view-header'
import { clearDonePlan, releaseFinishedPlan } from '@/lib/commands'
import { useDelayed } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { BoardMap } from '@/routes/board/board-map'
import { Column, type Placeholder } from '@/routes/board/column'
import { DeviceCard } from '@/routes/board/device-card'
import { useCardGlide, useMoveGlide } from '@/routes/board/glide'
import { MergeProposalCard } from '@/routes/board/merge-card'
import { ProposalsReadError, useOpenProposals } from '@/routes/members/sign-ins'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { EmptyBoard } from '@/routes/board/empty-board'
import { registerRoute } from '@/routes/registry'
import { finishedRuns, useBoard, workspaceRuns, type BoardColumn } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { useCapability, useDeviceNeeds, useIsAdmin, useSelf, useStateContext } from '@/store/hooks'
import type { BoardView } from '@/store/ui'

/** Three 200px columns, their two 8px gaps and the 16px page gutters. */
const boardMinWidth = 648

export function Board() {
  const { columns, archivedCards, hiddenByMine } = useBoard()
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const mineOnly = useStore((s) => s.mineOnly)
  const view = useStore((s) => s.boardViews[s.activeWorkspace] ?? 'board')
  const setView = useStore((s) => s.setBoardView)
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const { agents, error: agentsError } = useAgentList()
  const [showArchived, setShowArchived] = useState(false)
  const [focusedCard, setFocusedCard] = useState<Element | null>(null)
  const area = useRef<HTMLDivElement>(null)
  const listScroll = useRef<HTMLDivElement>(null)
  const [measure, fits] = useFitsColumns()
  const areaRef = useCallback((node: HTMLDivElement | null) => {
    area.current = node
    measure(node)
  }, [measure])

  useEffect(() => {
    setShowArchived(false)
  }, [activeWorkspace])
  useEffect(() => {
    if (archivedCards.length === 0) setShowArchived(false)
  }, [archivedCards.length])

  const devices = useDeviceNeeds()
  const { toAccept, unplanned } = useOpenProposals()
  const mergeError = useStore((s) => s.mergeProposalsError)
  const merges = useMemo(() => [...toAccept, ...unplanned], [toAccept, unplanned])
  const total = columns.reduce((n, c) => n + c.cards.length, 0) + archivedCards.length + devices.length + merges.length
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable && total === 0)
  const placeholder: Placeholder = loading ? 'skeleton' : hydrated ? 'empty' : 'none'
  const agentNames = agentDisplayNames(agents)

  const tab: BoardView = view === 'list' && !fits ? 'board' : view
  const layout = view === 'map' ? 'map' : fits && view === 'board' ? 'board' : 'list'
  const column = (key: BoardColumn['key']) => columns.find((c) => c.key === key)!
  const finished = showArchived ? { ...column('finished'), cards: archivedCards } : column('finished')
  const needsYou = column('needs-you').cards.length + devices.length + merges.length

  const captureLayout = useCardGlide(area, layout)
  useMoveGlide(captureLayout, layout === 'map' || !hydrated ? [] : [column('needs-you'), column('working'), finished], [activeWorkspace, mineOnly, showArchived, layout].join(' '))
  const choose = (value: string) => {
    const next = value as BoardView
    if (next !== 'map' && view !== 'map') captureLayout()
    setView(activeWorkspace, next)
  }

  let body: ReactNode
  if (layout === 'map') {
    body = (
      <>
        {merges.length > 0 && (
          <div aria-label="Merge proposals waiting for you" role="region" className="grid max-h-[40%] shrink-0 grid-cols-1 gap-2 overflow-y-auto border-b border-seam p-4 md:grid-cols-2 xl:grid-cols-3">
            {merges.map((proposal) => <MergeProposalCard key={proposal.id} proposal={proposal} />)}
          </div>
        )}
        {devices.length > 0 && (
          <div aria-label="Devices waiting for approval" role="region" className="grid max-h-[40%] shrink-0 grid-cols-1 gap-2 overflow-y-auto border-b border-seam p-4 md:grid-cols-2 xl:grid-cols-3">
            {devices.map((need) => <DeviceCard key={need.device.id} need={need} />)}
          </div>
        )}
        <BoardMap agentNames={agentNames} />
      </>
    )
  } else if (unreachable && total === 0) {
    body = (
      <div className="p-4">
        <Callout role="alert" tone="failed" title="Cannot reach the server">
          {dead ? error : 'Retrying.'}
        </Callout>
      </div>
    )
  } else if (hydrated && total === 0) {
    body = (
      <div className="min-h-0 flex-1 overflow-y-auto">
        <EmptyBoard agents={agentsError === null ? agents : 'unknown'} hiddenByMine={hiddenByMine} />
      </div>
    )
  } else {
    const list = layout === 'list' ? listScroll : undefined
    const groups = (
      <>
        <Column column={column('needs-you')} devices={devices} merges={merges} placeholder={placeholder} agentNames={agentNames} listScroll={list} />
        <Column column={column('working')} placeholder={placeholder} agentNames={agentNames} listScroll={list} />
        <Column
          column={finished}
          label={showArchived ? 'Archived' : undefined}
          placeholder={placeholder}
          agentNames={agentNames}
          listScroll={list}
          collapsible={layout === 'list'}
          actions={!showArchived && <FinishedMenu />}
          footer={
            <FinishedFooter
              archived={archivedCards.length}
              showingArchived={showArchived}
              onToggleArchived={() => setShowArchived((shown) => !shown)}
            />
          }
        />
      </>
    )
    body = layout === 'board' ? (
      <div className="flex min-h-0 flex-1 items-start gap-2 p-4">{groups}</div>
    ) : (
      <div ref={listScroll} className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto p-4">
        <div className="flex flex-col gap-2">{groups}</div>
      </div>
    )
  }

  return (
    <Tabs value={tab} onValueChange={choose} asChild>
      <div ref={areaRef} className="flex h-full min-w-0 flex-col">
        <ViewHeader title="Board" titleAdornment={
          <TabsList look="segmented" aria-label="Board view">
            <TabsTrigger value="board"><Columns3 />Board</TabsTrigger>
            {fits && <TabsTrigger value="list"><List />List</TabsTrigger>}
            <TabsTrigger value="map"><Network />Map</TabsTrigger>
          </TabsList>
        } />
        <TabsContent
          key={tab}
          value={tab}
          className="flex min-w-0 flex-1 flex-col"
          onFocus={(event) => setFocusedCard(event.target.closest('[data-run-id]'))}
          onBlur={(event) => {
            if (!(event.relatedTarget instanceof Element && event.currentTarget.contains(event.relatedTarget))) setFocusedCard(null)
          }}
        >
          {mergeError && <div className="shrink-0 px-4 pt-4"><ProposalsReadError /></div>}
          <div className="shrink-0 px-4 pt-4 empty:hidden"><SysboxNudge /></div>
          {body}
          {focusedCard?.isConnected && <CardKeys card={focusedCard} />}
        </TabsContent>
        <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">
          {needsYou === 1 ? '1 needs you' : `${needsYou} need you`}
        </span>
      </div>
    </Tabs>
  )
}

/** Whether three columns fit the board's width; re-renders only when that flips. */
function useFitsColumns(): [(node: HTMLDivElement | null) => void, boolean] {
  const [node, setNode] = useState<HTMLDivElement | null>(null)
  const [fits, setFits] = useState(true)
  useLayoutEffect(() => {
    if (!node) return
    // Zero until laid out, as in jsdom: assume the columns fit.
    const measure = (width: number) => setFits(width === 0 || width >= boardMinWidth)
    measure(node.getBoundingClientRect().width)
    const observer = new ResizeObserver(([entry]) => measure(entry.contentRect.width))
    observer.observe(node)
    return () => observer.disconnect()
  }, [node])
  return [setNode, fits]
}

function CardKeys({ card }: { card: Element }) {
  const control = (selector: string) => card.querySelector<HTMLElement>(selector)
  const press = (selector: string) => () => control(selector)?.click()
  useKeybindings('card', {
    ...(control('[data-card-action="reply"]') && { 'card-reply': press('[data-card-action="reply"]') }),
    'card-open': press('[data-card-open]'),
  })
  return null
}

function FinishedFooter({
  archived,
  showingArchived,
  onToggleArchived,
}: {
  archived: number
  showingArchived: boolean
  onToggleArchived: () => void
}) {
  if (archived === 0 && !showingArchived) return null
  return (
    <div className="flex h-8 items-center px-3">
      <Button variant="link" size="sm" aria-pressed={showingArchived} onClick={onToggleArchived}>
        {showingArchived ? 'Back to Finished' : `Archived (${archived})`}
      </Button>
    </div>
  )
}

function FinishedMenu() {
  const ctx = useStateContext()
  const caps = useCapability()
  const self = useSelf()
  const admin = useIsAdmin()
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const canArchive = clearDonePlan(finishedRuns(activeWorkspace, ctx), caps, self).eligible.length > 0
  const canFree = admin && releaseFinishedPlan(workspaceRuns(activeWorkspace, ctx), caps, self).eligible.length > 0
  if (!canArchive && !canFree) return null

  return (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" label="More finished-run actions">
          <Ellipsis />
        </Button>
      </MenuTrigger>
      <MenuContent align="end">
        {canArchive && <MenuItem onSelect={() => openDialog('clear-done')}>Archive closed runs…</MenuItem>}
        {canFree && <MenuItem onSelect={() => openDialog('release-finished')}>Free retained containers…</MenuItem>}
      </MenuContent>
    </Menu>
  )
}

registerRoute('board', Board)

import { useEffect, useState, type ReactNode } from 'react'
import { Ellipsis } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import { clearDonePlan, releaseFinishedPlan } from '@/lib/commands'
import { belowLg, useDelayed, useMediaQuery } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { Column, type Placeholder } from '@/routes/board/column'
import { EmptyBoard, type AgentsState } from '@/routes/board/empty-board'
import { TerminalDock } from '@/routes/board/terminal-dock'
import { registerRoute } from '@/routes/registry'
import { finishedRuns, useBoard, workspaceRuns, type BoardColumn } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useSelf, useStateContext } from '@/store/hooks'

function focusedCard(selector: string) {
  const card = document.activeElement?.closest('[data-run-id]')
  card?.querySelector<HTMLElement>(selector)?.click()
}

export function Board() {
  const { columns, archivedCards } = useBoard()
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const stacked = useMediaQuery(belowLg)
  const agents = useAgents()
  const caps = useCapability()
  const [showArchived, setShowArchived] = useState(false)

  useEffect(() => {
    setShowArchived(false)
  }, [activeWorkspace])
  useEffect(() => {
    if (archivedCards.length === 0) setShowArchived(false)
  }, [archivedCards.length])

  useKeybindings('card', {
    'card-approve': () => focusedCard('[data-card-action="approve"]'),
    'card-reply': () => focusedCard('[data-card-action="reply"]'),
    'card-open': () => focusedCard('[data-card-open]'),
  })

  const total = columns.reduce((n, c) => n + c.cards.length, 0) + archivedCards.length
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable && total === 0)
  const placeholder: Placeholder = loading ? 'skeleton' : hydrated ? 'empty' : 'none'
  const agentNames = Array.isArray(agents)
    ? Object.fromEntries(agents.map((agent) => [agent.name, agent.display_name ?? agent.name]))
    : {}

  const column = (key: BoardColumn['key']) => columns.find((c) => c.key === key)!
  const finished = showArchived ? { ...column('finished'), cards: archivedCards } : column('finished')

  let body: ReactNode
  if (unreachable && total === 0) {
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
        <EmptyBoard agents={agents} />
      </div>
    )
  } else {
    body = (
      <div
        className="grid min-h-0 flex-1 grid-cols-1 content-start gap-6 overflow-y-auto p-4 lg:grid-cols-3 lg:content-stretch lg:gap-4 lg:overflow-hidden lg:pb-0"
      >
        <Column column={column('needs-you')} placeholder={placeholder} agentNames={agentNames} />
        <Column column={column('working')} placeholder={placeholder} agentNames={agentNames} />
        <Column
          key={stacked ? 'stacked' : 'wide'}
          column={finished}
          label={showArchived ? 'Archived' : undefined}
          placeholder={placeholder}
          agentNames={agentNames}
          collapsed={stacked ? true : undefined}
          footer={
            <FinishedFooter
              archived={archivedCards.length}
              showingArchived={showArchived}
              onToggleArchived={() => setShowArchived((shown) => !shown)}
            />
          }
        />
      </div>
    )
  }

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Board" />
      <div className="flex min-h-0 flex-1 flex-col overflow-x-hidden overflow-y-auto">
        <div className="flex min-h-24 min-w-0 flex-1 flex-col overflow-hidden">{body}</div>
        {caps.hasWS('terminal') && <TerminalDock containment="parent" />}
      </div>
    </div>
  )
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
  const ctx = useStateContext()
  const caps = useCapability()
  const self = useSelf()
  const admin = useIsAdmin()
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const canArchive = clearDonePlan(finishedRuns(activeWorkspace, ctx), caps, self).eligible.length > 0
  const canFree = admin && releaseFinishedPlan(workspaceRuns(activeWorkspace, ctx), caps, self).eligible.length > 0
  if (archived === 0 && !showingArchived && !canArchive && !canFree) return null

  return (
    <div className="flex min-h-7 items-center justify-between gap-2 px-1">
      {(archived > 0 || showingArchived) && (
        <Button variant="link" size="sm" aria-pressed={showingArchived} onClick={onToggleArchived}>
          {showingArchived ? 'Back to Finished' : `Archived (${archived})`}
        </Button>
      )}
      {!showingArchived && (canArchive || canFree) && (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" label="More finished-run actions" className="ml-auto">
              <Ellipsis />
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            {canArchive && <MenuItem onSelect={() => openDialog('clear-done')}>Archive closed runs…</MenuItem>}
            {canFree && <MenuItem onSelect={() => openDialog('release-finished')}>Free retained containers…</MenuItem>}
          </MenuContent>
        </Menu>
      )}
    </div>
  )
}

function useAgents(): AgentsState {
  const caps = useCapability()
  const listable = caps.hasMethod('agent.list')
  const [agents, setAgents] = useState<AgentsState>('loading')
  useEffect(() => {
    if (!listable) return
    let live = true
    api
      .agentList()
      .then((list) => live && setAgents(list))
      .catch(() => live && setAgents('unknown'))
    return () => {
      live = false
    }
  }, [listable])
  return listable ? agents : 'unknown'
}

registerRoute('board', Board)

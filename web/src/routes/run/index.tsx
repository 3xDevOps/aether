import { useCallback, useEffect, useRef, useState } from 'react'
import { MissingRun } from '@/components/missing-run'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Tabs, TabsContent } from '@/components/ui/tabs'
import { useIsMobile } from '@/lib/breakpoints'
import { useElementSize, useMediaQuery } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { cn } from '@/lib/utils'
import { BrowserView } from '@/routes/browser'
import { BrowserDock, minBesideWidth } from '@/routes/browser/dock'
import { hasBrowser } from '@/routes/browser/open'
import { ChangesView } from '@/routes/diff'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useAgentTerminal } from '@/routes/run/agent-terminal'
import { CapturesDialog } from '@/routes/run/captures'
import { RawEventsDialog } from '@/routes/run/raw-events'
import { RunDetails } from '@/routes/run/details'
import { RunHeader, type RunNavigation } from '@/routes/run/header'
import { MultiplayerControls } from '@/routes/run/multiplayer-controls'
import { useRunRoom } from '@/routes/run/room'
import { dockedRequestID } from '@/routes/run/session-requests'
import { SessionView } from '@/routes/run/session-view'
import { useRunShells } from '@/routes/run/shells'
import { TerminalView } from '@/routes/run/terminal-view'
import { defaultView, isRunView, runViewLabel, runViews, type RunView } from '@/routes/run/views'
import { TakeoverDialog } from '@/routes/terminal/takeover-dialog'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** Inactive views stay laid out but invisible: a terminal hidden with
 * display:none measures zero and would resize the shared PTY. */
const panel = 'absolute inset-0 outline-none'

const inlineDetails = '(min-width: 1280px)'

function RunRoute({ params }: RouteProps) {
  const run = useStore((s) => s.runs[params.runId])
  const identityKey = useStore((s) => s.identityKey)
  const epoch = useStore((s) => s.terminalCacheEpoch)
  if (!run) return <MissingRun />
  return <RunFrame key={JSON.stringify([identityKey, epoch, run.id, run.created_at])} run={run} params={params} />
}

function useVisited(view: RunView) {
  const visited = useRef(new Set<RunView>())
  visited.current.add(view)
  return visited.current
}

function RunFrame({ run, params }: { run: RunRecord; params: RouteProps['params'] }) {
  const cap = useCapability()
  const mobile = useIsMobile()
  const inline = useMediaQuery(inlineDetails)
  const navigate = useStore((s) => s.navigate)
  const remembered = useStore((s) => s.runViewMemory[run.id])
  const detailsPreference = useStore((s) => s.detailsOpen)
  const setDetailsPreference = useStore((s) => s.setDetailsOpen)
  const members = useStore((s) => s.members)
  const [overlayDetails, setOverlayDetails] = useState(false)
  const [composing, setComposing] = useState(false)
  const [dialog, setDialog] = useState<'captures' | 'events' | null>(null)
  const returnTo = useRef<HTMLElement | null>(null)
  const [noteDraft, setNoteDraft] = useState<{ text: string } | null>(null)
  const textarea = useRef<HTMLTextAreaElement>(null)

  const browser = hasBrowser(run, cap)
  const beside = useStore((s) => s.browserBeside)
  const setBeside = useStore((s) => s.setBrowserBeside)
  const browserRequest = useStore((s) => s.browserRequests[run.id])
  const [frameRef, frame] = useElementSize<HTMLDivElement>()
  const splittable = browser && frame.width >= 2 * minBesideWidth
  const docked = splittable && beside
  const views = runViews.filter((view) => view !== 'browser' || (browser && !docked))
  const asked = isRunView(params.view) ? params.view : remembered
  const view = asked && views.includes(asked) ? asked : defaultView(run)
  const visited = useVisited(view)

  const agent = useAgentTerminal(run, visited.has(defaultView(run)))
  const { agents } = useAgentList()
  const agentName = agentDisplayNames(agents)[run.harness] ?? run.harness
  const agentEntry = agents?.find((entry) => entry.name === run.harness)
  const switchable = agentEntry?.switchable === true
  const shells = useRunShells(run.id)
  const room = useRunRoom(run, agent.roomControl)

  const detailsOpen = inline ? detailsPreference : overlayDetails
  const setDetails = useCallback((open: boolean) => {
    if (inline) setDetailsPreference(open)
    else setOverlayDetails(open)
  }, [inline, setDetailsPreference])

  const go = useCallback((next: RunView) => navigate('run', { runId: run.id, view: next }), [navigate, run.id])
  useEffect(() => {
    if (browserRequest && browser && !docked) go('browser')
  }, [browserRequest, browser, docked, go])
  const browserView = (
    <BrowserView
      runID={run.id}
      split={splittable ? {
        beside: docked,
        view: runViewLabel[defaultView(run)],
        toggle: () => {
          setBeside(!beside)
          go(beside ? 'browser' : view === 'browser' ? defaultView(run) : view)
        },
      } : undefined}
    />
  )
  const nav: RunNavigation = {
    go,
    reveal: (cardID) => {
      returnTo.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      setDetails(true)
      requestAnimationFrame(() => requestAnimationFrame(() => {
        const card = document.getElementById(cardID)
        card?.scrollIntoView({ block: 'nearest' })
        ;(card?.querySelector<HTMLElement>('textarea') ?? card)?.focus()
      }))
    },
    focusComposer: () => {
      go('session')
      requestAnimationFrame(() => textarea.current?.focus())
    },
    focusRequest: () => {
      if (!inline) setOverlayDetails(false)
      go('session')
      requestAnimationFrame(() => requestAnimationFrame(() => document.getElementById(dockedRequestID)?.focus()))
    },
  }

  const [composeRequested, setComposeRequested] = useState(false)
  useEffect(() => {
    if (params.focus === 'composer') setComposeRequested(true)
  }, [params])

  const step = (delta: number) => go(views[(views.indexOf(view) + delta + views.length) % views.length]!)
  useKeybindings('run', {
    'leave-run': () => navigate('board'),
    'run-view-next': () => step(1),
    'run-view-previous': () => step(-1),
    'run-details': (event) => {
      event.preventDefault()
      returnTo.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      setDetails(!detailsOpen)
    },
    'focus-composer': nav.focusComposer,
  })

  const openDialog = (kind: 'captures' | 'events') => (opener: HTMLElement | null) => {
    returnTo.current = opener
    setDialog(kind)
  }
  const openCaptures = openDialog('captures')
  const details = (inset: boolean) => (
    <RunDetails run={run} view={view} agent={agent} agentName={agentName} room={room} nav={nav} inset={inset} noteDraft={noteDraft} />
  )
  const requesterID = agent.takeover.review?.requester_member_id
  const holding = !agent.localControl ? agent.takeover.interaction.phase : undefined

  return (
    <Tabs value={view} onValueChange={(next) => isRunView(next) && go(next)} asChild>
    <div className="flex h-full min-w-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <RunHeader
          run={run}
          view={view}
          views={views}
          agent={agent}
          nav={nav}
          composing={mobile && composing}
          detailsOpen={detailsOpen}
          onDetails={(opener) => {
            returnTo.current = opener
            setDetails(!detailsOpen)
          }}
          onCaptures={openCaptures}
          onEvents={openDialog('events')}
          agentName={agentName}
          agentEntry={agentEntry}
        />
        {run.mode === 'acp' && view !== 'browser' && <MultiplayerControls run={run} agent={agent} onPeople={() => nav.reveal('details-people')} />}
        <div ref={frameRef} className="flex min-h-0 flex-1">
          <div className="relative min-h-0 min-w-0 flex-1">
            {visited.has('session') && (
              <TabsContent value="session" forceMount inert={view !== 'session'} className={cn(panel, view !== 'session' && 'invisible')}>
                <SessionView
                  run={run}
                  agent={agent}
                  agentName={agentName}
                  room={room}
                  nav={nav}
                  active={view === 'session'}
                  textarea={textarea}
                  focusComposer={composeRequested}
                  onComposing={(focused) => {
                    setComposing(focused)
                    if (focused) setComposeRequested(false)
                  }}
                  shells={shells}
                  switchable={switchable}
                />
              </TabsContent>
            )}
            <TabsContent value="terminal" forceMount inert={view !== 'terminal'} className={cn(panel, view !== 'terminal' && 'invisible')}>
              <TerminalView run={run} agent={agent} shells={shells} onCaptures={openCaptures} />
            </TabsContent>
            {visited.has('changes') && (
              <TabsContent value="changes" forceMount inert={view !== 'changes'} className={cn(panel, view !== 'changes' && 'invisible')}>
                <ChangesView runID={run.id} />
              </TabsContent>
            )}
            {view === 'browser' && (
              <TabsContent value="browser" className={panel}>
                {browserView}
              </TabsContent>
            )}
          </div>
          {docked && <BrowserDock available={frame.width}>{browserView}</BrowserDock>}
        </div>
      </div>
      {inline && detailsOpen && (
        <aside id="run-details" aria-label="Run details" className="w-80 shrink-0 overflow-y-auto border-l border-seam bg-canvas">
          {details(true)}
        </aside>
      )}
      {!inline && (
        <Dialog open={detailsOpen} onOpenChange={setDetails}>
          <DialogContent
            id="run-details"
            variant={mobile ? 'bottom' : 'side'}
            aria-describedby={undefined}
            onOpenAutoFocus={(event) => {
              event.preventDefault()
              ;(event.currentTarget as HTMLElement).focus()
            }}
            onCloseAutoFocus={(event) => {
              if (!returnTo.current?.isConnected) return
              event.preventDefault()
              returnTo.current.focus()
            }}
            className={mobile ? undefined : 'w-80'}
          >
            <div className="flex min-h-0 flex-col gap-1 overflow-y-auto px-4 pt-4 max-md:p-0">
              <DialogTitle>Run details</DialogTitle>
              {details(false)}
            </div>
          </DialogContent>
        </Dialog>
      )}
      <CapturesDialog
        runID={run.id}
        workspaceID={run.workspace_id}
        open={dialog === 'captures'}
        returnTo={returnTo.current}
        onOpenChange={(open) => setDialog(open ? 'captures' : null)}
        onAnswer={(fact) => {
          setDialog(null)
          setNoteDraft({ text: fact })
          setDetails(true)
        }}
      />
      <RawEventsDialog
        run={run}
        open={dialog === 'events'}
        onOpenChange={(open) => setDialog(open ? 'events' : null)}
        returnTo={returnTo.current}
      />
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">
        {holding === 'holding'
          ? `Keep holding to request control. ${agent.takeover.interaction.seconds} seconds remaining.`
          : holding === 'review'
            ? `Control requested. Waiting for the controller's decision. ${agent.takeover.interaction.seconds} seconds remaining.`
            : ''}
      </span>
      <TakeoverDialog
        open={Boolean(agent.takeover.review)}
        requesterName={requesterID ? members[requesterID]?.display_name || requesterID : ''}
        seconds={agent.takeover.interaction.seconds}
        pending={agent.takeover.decisionPending}
        error={agent.session.takeoverError}
        onDecide={agent.takeover.decide}
      />
    </div>
    </Tabs>
  )
}

registerRoute('run', RunRoute)

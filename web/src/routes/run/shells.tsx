import { useCallback, useEffect, useRef, useState } from 'react'
import { Check, ChevronDown, Plus, X } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { onTabListKeyDown } from '@/lib/keys'
import { message } from '@/lib/format'
import type { DevControlFence, DevController, DevTerminal } from '@/lib/types'
import { cn, focusRingInset } from '@/lib/utils'
import { useStore } from '@/store'
import { getShellSocket, initialRunShellDock, shellEnded } from '@/store/terminal'

/** Mirrors maxRunShells in internal/scheduler/run_shell.go. */
const maxOwnShells = 4
const limitReason = `People can run ${maxOwnShells} shells in a run; the agent’s are counted apart. Stop one to open another.`
const workerReason = 'Take control first. This run is a swarm worker, so control is never taken for you.'

export const shellPollMs = (visible: boolean) => (visible ? 2_000 : 10_000)

export const shellControlKey = (runID: string, terminal: Pick<DevTerminal, 'terminal_id' | 'incarnation'>) =>
  `${runID}:${terminal.terminal_id}:${terminal.incarnation}`

/** The server names an unnamed shell `tab-N`, and keeps that id for its life. */
export function shellName(terminal: DevTerminal): string {
  const numbered = /^tab-(\d+)$/.exec(terminal.name)
  return numbered ? `Shell ${numbered[1]}` : terminal.name
}

function shellLabel(terminal: DevTerminal): string {
  const name = shellName(terminal)
  return terminal.process.state === 'running' ? name : `${name} · ${terminal.process.state}`
}

export function controllerName(controller: DevController, members: Record<string, { display_name: string } | undefined>): string {
  if (controller.kind === 'run_agent') return 'The agent'
  return controller.member_id ? members[controller.member_id]?.display_name ?? controller.member_id : 'Someone'
}

export function useRunShells(runID: string, visible: boolean) {
  const run = useStore((s) => s.runs[runID])
  const dock = useStore((s) => s.shellDocks[runID] ?? initialRunShellDock)
  const paused = useStore((s) => s.pausedRuns[runID] ?? run?.paused)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [stopRequest, setStopRequest] = useState<{ terminal: DevTerminal; controller: DevController } | null>(null)
  const revision = useRef(0)
  // Keyed by shellControlKey. They outlive the mounted shell so returning to a
  // tab reclaims the lease this page held instead of reading it as occupied.
  const writeIntent = useRef<Record<string, boolean>>({})
  const controlSessions = useRef<Record<string, string>>({})
  // Set by an action that asks for a shell; its terminal takes the keyboard
  // once it has drawn, because the control that asked is gone by then.
  const focusShell = useRef(false)
  const canOpen = paused === false && (run?.status === 'running' || run?.status === 'needs-attention')
  // Shell control on a swarm worker also sets its durable orchestration hold
  // (docs/terminal.md), so there it stays a deliberate click.
  const implicitControl = run?.mission_role !== 'worker'

  const refresh = useCallback(async () => {
    const current = ++revision.current
    const result = await api.devTerminalList({ run_id: runID })
    if (revision.current === current) useStore.getState().syncShellTerminals(runID, result.terminals)
  }, [runID])

  useEffect(() => {
    let cancelled = false
    let timer: number | undefined
    const poll = async () => {
      if (document.visibilityState === 'visible') {
        try {
          await refresh()
        } catch (cause) {
          if (!cancelled) setError(message(cause))
        }
      }
      if (!cancelled) timer = window.setTimeout(() => void poll(), shellPollMs(visible))
    }
    void poll()
    return () => {
      cancelled = true
      revision.current++
      clearTimeout(timer)
    }
  }, [refresh, visible])

  // Closing a tab removes the control that held focus. Hand the keyboard to
  // what the dock selected next instead of leaving it on `body`.
  const tabCount = useRef(dock.tabs.length)
  useEffect(() => {
    const closed = dock.tabs.length < tabCount.current
    tabCount.current = dock.tabs.length
    if (!closed || (document.activeElement && document.activeElement !== document.body)) return
    Array.from(document.querySelectorAll<HTMLElement>('[data-shell-focus]')).find((target) => !target.closest('[inert]'))?.focus()
  }, [dock.tabs.length])

  const atLimit = dock.terminals.filter((item) => item.started_by !== 'run_agent' && !shellEnded(item)).length >= maxOwnShells
  const open = useCallback(async (): Promise<DevTerminal | undefined> => {
    setBusy(true)
    setError(null)
    try {
      const result = await api.devTerminalStart({ run_id: runID })
      revision.current++
      if (implicitControl) writeIntent.current[shellControlKey(runID, result.terminal)] = true
      const s = useStore.getState()
      const current = s.shellDocks[runID]?.terminals ?? []
      s.syncShellTerminals(runID, [...current.filter((item) => item.terminal_id !== result.terminal.terminal_id), result.terminal])
      focusShell.current = true
      s.selectShellTab(runID, result.terminal.terminal_id)
      return result.terminal
    } catch (cause) {
      setError(message(cause))
      return undefined
    } finally {
      setBusy(false)
    }
  }, [runID, implicitControl])

  /** `from` is the controller the person agreed to take the shell from. */
  const stop = useCallback(async (terminal: DevTerminal, from?: DevController) => {
    const surface = { kind: 'terminal' as const, id: terminal.terminal_id, incarnation: terminal.incarnation }
    const held = getShellSocket(runID, terminal.terminal_id)?.controlMetadata?.()
    let acquired: DevControlFence | undefined
    setStopRequest(null)
    setBusy(true)
    setError(null)
    try {
      let fence: DevControlFence | undefined = held?.has_control
        ? { control_session_id: held.control_session_id, control_generation: held.control_generation }
        : undefined
      if (!fence) {
        const controller = from ?? (await api.devControlStatus({ run_id: runID, surface })).controller
        if (controller && !from && controller.control_session_id !== controlSessions.current[shellControlKey(runID, terminal)]) {
          setStopRequest({ terminal, controller })
          return
        }
        const result = await api.devControlAcquire({
          run_id: runID, surface, control_session_id: crypto.randomUUID(),
          ...(controller ? { takeover: true, expected_generation: controller.control_generation } : {}),
        })
        if (!result.controller) throw new Error('dev.control.acquire returned no controller')
        fence = acquired = {
          control_session_id: result.controller.control_session_id,
          control_generation: result.controller.control_generation,
        }
      }
      const result = await api.devTerminalStop({
        run_id: runID, terminal_id: terminal.terminal_id, incarnation: terminal.incarnation, ...fence, timeout_ms: 3000,
      })
      if (result.timed_out) setError('Terminal stop timed out; refresh the process state before another explicit stop.')
      else useStore.getState().closeShellTab(runID, terminal.terminal_id)
      await refresh()
    } catch (cause) {
      setError(message(cause))
    } finally {
      if (acquired) await api.devControlRelease({ run_id: runID, surface, ...acquired }).catch((cause) => setError(message(cause)))
      setBusy(false)
    }
  }, [runID, refresh])

  return {
    runID,
    dock,
    visible,
    canOpen,
    atLimit,
    canAdd: canOpen && !busy && !atLimit,
    busy,
    implicitControl,
    writeIntent,
    controlSessions,
    focusShell,
    open,
    show: (tab: string) => {
      focusShell.current = !dock.shellShown || dock.activeTab !== tab
      useStore.getState().selectShellTab(runID, tab)
    },
    stop,
    stopRequest,
    cancelStop: () => setStopRequest(null),
    refresh,
    error,
    setError,
  }
}

export type RunShells = ReturnType<typeof useRunShells>

export const terminalPanelID = { agent: 'run-agent-panel', shell: 'run-shell-panel' }

/** Where focus lands after a tab closes: the selected tab, else the control that opens one. */
const focusTarget = { 'data-shell-focus': '' }

/** What closing a shell's tab can mean. Stopping needs no control beforehand:
 * `shells.stop` takes it, and asks first when someone else holds it. */
export function ShellCloseItems({ shells, terminal, onClose }: { shells: RunShells; terminal: DevTerminal; onClose?: () => void }) {
  const hide = () => {
    onClose?.()
    useStore.getState().closeShellTab(shells.runID, terminal.terminal_id)
  }
  if (shellEnded(terminal)) return <MenuItem onSelect={hide}>Close</MenuItem>
  const needsControl = !shells.implicitControl &&
    getShellSocket(shells.runID, terminal.terminal_id)?.controlMetadata?.().has_control !== true
  return (
    <>
      <MenuItem onSelect={hide}>Hide, keep running</MenuItem>
      <MenuItem
        tone="danger"
        disabled={shells.busy || needsControl}
        description={needsControl ? workerReason : undefined}
        onSelect={() => {
          onClose?.()
          void shells.stop(terminal)
        }}
      >
        Stop shell
      </MenuItem>
    </>
  )
}

export function StopShellDialog({ shells }: { shells: RunShells }) {
  const members = useStore((s) => s.members)
  const shown = useRef(shells.stopRequest)
  if (shells.stopRequest) shown.current = shells.stopRequest
  const request = shown.current
  return (
    <Dialog open={shells.stopRequest !== null} onOpenChange={(open) => { if (!open) shells.cancelStop() }}>
      <DialogContent>
        {request && (
          <>
            <DialogHeader>
              <DialogTitle>Stop {shellName(request.terminal)}?</DialogTitle>
              <DialogDescription>
                {controllerName(request.controller, members)} controls this shell. Stopping it ends its process for everyone using it.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="secondary" onClick={shells.cancelStop}>Cancel</Button>
              <Button variant="danger" disabled={shells.busy} onClick={() => void shells.stop(request.terminal, request.controller)}>
                Stop shell
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

export function TerminalTabs({ shells, agent }: { shells: RunShells; agent: boolean }) {
  const mobile = useIsMobile()
  const { runID, dock } = shells
  const harness = useStore((s) => s.runs[runID]?.harness ?? '')
  const [closing, setClosing] = useState<string | null>(null)
  const showAgent = () => useStore.getState().setShellShown(runID, false)
  const open = shells.canOpen ? dock.tabs.flatMap((id) => dock.terminals.find((item) => item.terminal_id === id) ?? []) : []
  const hidden = shells.canOpen ? dock.terminals.filter((item) => !dock.tabs.includes(item.terminal_id) && !shellEnded(item)) : []
  const ids = [...(agent ? [''] : []), ...open.map((item) => item.terminal_id)]
  const active = dock.shellShown ? dock.activeTab ?? '' : ''
  const index = Math.max(0, ids.indexOf(active))
  const [focused, setFocused] = useState(index)
  useEffect(() => setFocused(index), [index])

  const agentStarted = (terminal: DevTerminal) => terminal.started_by === 'run_agent'

  if (mobile) {
    const current = open.find((item) => item.terminal_id === active)
    const label = current ? shellLabel(current) : agent ? 'Agent' : 'Terminal'
    const check = (id: string) => <Check className={id === active ? undefined : 'invisible'} />
    return (
      <Menu>
        <MenuTrigger asChild>
          <Button variant="ghost" size="sm" aria-label={`Terminal: ${label}`} {...focusTarget}>
            {label}
            <ChevronDown />
          </Button>
        </MenuTrigger>
        <MenuContent align="start">
          {agent && <MenuItem icon={check('')} onSelect={showAgent}>Agent</MenuItem>}
          {open.map((item) => (
            <MenuItem
              key={item.terminal_id}
              icon={check(item.terminal_id)}
              description={agentStarted(item) ? 'Started by the agent' : undefined}
              onSelect={() => shells.show(item.terminal_id)}
            >
              {shellLabel(item)}
            </MenuItem>
          ))}
          {shells.canOpen && (
            <>
              <MenuSeparator />
              <MenuItem
                icon={<Plus />}
                disabled={!shells.canAdd}
                description={shells.atLimit ? limitReason : undefined}
                onSelect={() => void shells.open()}
              >
                New shell
              </MenuItem>
              {hidden.length > 0 && <MenuLabel>Hidden, still running</MenuLabel>}
              {hidden.map((item) => (
                <MenuItem key={item.terminal_id} icon={<Check className="invisible" />} onSelect={() => shells.show(item.terminal_id)}>
                  Show {shellName(item)}
                </MenuItem>
              ))}
            </>
          )}
        </MenuContent>
      </Menu>
    )
  }

  const tab = (id: string, i: number) => ({
    role: 'tab',
    ...(id === active ? focusTarget : {}),
    'aria-selected': id === active,
    'aria-controls': id ? terminalPanelID.shell : terminalPanelID.agent,
    tabIndex: i === focused ? 0 : -1,
    onFocus: () => setFocused(i),
    onClick: () => (id ? shells.show(id) : showAgent()),
  }) as const

  return (
    <>
      {open.length > 0 && (
        <div role="tablist" aria-label="Terminals" className="flex min-w-0 items-center gap-0.5 overflow-x-auto">
          {agent && (
            <Button
              {...tab('', 0)}
              variant={active === '' ? 'secondary' : 'ghost'}
              size="sm"
              className={focusRingInset}
              onKeyDown={(event) => onTabListKeyDown(event, ids.length, focused, setFocused)}
            >
              Agent
            </Button>
          )}
          {open.map((item, position) => {
            const id = item.terminal_id
            const i = position + (agent ? 1 : 0)
            const ended = shellEnded(item)
            const close = (
              <Button
                variant="ghost"
                size="icon-sm"
                label={`Close ${shellName(item)}`}
                tabIndex={-1}
                className="size-5 coarse:size-11 [&_svg]:size-3"
                onClick={ended ? () => useStore.getState().closeShellTab(runID, id) : undefined}
              >
                <X />
              </Button>
            )
            return (
              <div
                key={id}
                className={cn('flex shrink-0 items-center rounded-control border pr-0.5', id === active ? 'border-seam bg-raised' : 'border-transparent')}
              >
                <Button
                  {...tab(id, i)}
                  variant="ghost"
                  size="sm"
                  hint={agentStarted(item) ? 'Started by the agent' : undefined}
                  aria-label={agentStarted(item) ? `${shellLabel(item)}, started by the agent` : undefined}
                  aria-keyshortcuts="Delete Backspace"
                  className={cn(focusRingInset, 'pr-1 coarse:pr-1', id === active && 'text-text')}
                  onKeyDown={(event) => {
                    const bare = !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey
                    if (bare && (event.key === 'Delete' || event.key === 'Backspace')) {
                      event.preventDefault()
                      if (ended) useStore.getState().closeShellTab(runID, id)
                      else setClosing(id)
                      return
                    }
                    onTabListKeyDown(event, ids.length, focused, setFocused)
                  }}
                >
                  {agentStarted(item) && <AgentGlyph agent={harness} />}
                  {shellLabel(item)}
                </Button>
                {ended ? close : (
                  <Menu open={closing === id} onOpenChange={(next) => setClosing(next ? id : null)}>
                    <MenuTrigger asChild>{close}</MenuTrigger>
                    <MenuContent align="start">
                      <ShellCloseItems shells={shells} terminal={item} />
                    </MenuContent>
                  </Menu>
                )}
              </div>
            )
          })}
        </div>
      )}
      {shells.canOpen && (
        <>
          <Button
            {...focusTarget}
            variant="ghost"
            size="sm"
            aria-label="New shell"
            hint={shells.atLimit ? limitReason : 'Open a shell in this run’s container'}
            aria-disabled={!shells.canAdd || undefined}
            onClick={() => {
              if (shells.canAdd) void shells.open()
            }}
          >
            <Plus />
            Shell
          </Button>
          {shells.atLimit && (
            <span role="status" className="shrink-0 px-1 text-ui-sm text-muted">
              At most {maxOwnShells} shells{dock.terminals.some(agentStarted) && ' besides the agent’s'}
            </span>
          )}
          {hidden.length > 0 && (
            <Menu>
              <MenuTrigger asChild>
                <Button variant="ghost" size="sm" aria-label={`${hidden.length} hidden ${hidden.length === 1 ? 'shell' : 'shells'}`}>
                  {hidden.length} hidden
                  <ChevronDown />
                </Button>
              </MenuTrigger>
              <MenuContent align="start">
                <MenuLabel>Still running</MenuLabel>
                {hidden.map((item) => (
                  <MenuItem key={item.terminal_id} onSelect={() => shells.show(item.terminal_id)}>Show {shellName(item)}</MenuItem>
                ))}
              </MenuContent>
            </Menu>
          )}
        </>
      )}
    </>
  )
}

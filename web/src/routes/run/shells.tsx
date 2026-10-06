import { useCallback, useEffect, useRef, useState } from 'react'
import { Check, ChevronDown, Plus } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { onTabListKeyDown } from '@/lib/keys'
import { message } from '@/lib/format'
import type { DevTerminal } from '@/lib/types'
import { useStore } from '@/store'
import { initialRunShellDock } from '@/store/terminal'

const maxShellTabs = 4
const listPollMs = 10_000

function shellLabel(terminal: DevTerminal | undefined, position: number): string {
  const name = terminal?.name || `Shell ${position}`
  return !terminal || terminal.process.state === 'running' ? name : `${name} · ${terminal.process.state}`
}

export function useRunShells(runID: string) {
  const run = useStore((s) => s.runs[runID])
  const dock = useStore((s) => s.shellDocks[runID] ?? initialRunShellDock)
  const paused = useStore((s) => s.pausedRuns[runID] ?? run?.paused)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const revision = useRef(0)
  const canOpen = paused === false && (run?.status === 'running' || run?.status === 'needs-attention')

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
      if (!cancelled) timer = window.setTimeout(() => void poll(), listPollMs)
    }
    void poll()
    return () => {
      cancelled = true
      revision.current++
      clearTimeout(timer)
    }
  }, [refresh])

  const running = dock.terminals.filter((terminal) => terminal.process.state === 'running').length
  const open = useCallback(async (): Promise<DevTerminal | undefined> => {
    setBusy(true)
    setError(null)
    try {
      const result = await api.devTerminalStart({ run_id: runID })
      revision.current++
      const s = useStore.getState()
      const current = s.shellDocks[runID]?.terminals ?? []
      s.syncShellTerminals(runID, [...current.filter((item) => item.terminal_id !== result.terminal.terminal_id), result.terminal])
      s.selectShellTab(runID, result.terminal.terminal_id)
      return result.terminal
    } catch (cause) {
      setError(message(cause))
      return undefined
    } finally {
      setBusy(false)
    }
  }, [runID])

  return {
    runID,
    dock,
    canOpen,
    canAdd: canOpen && !busy && running < maxShellTabs,
    open,
    refresh,
    error,
    setError,
  }
}

export type RunShells = ReturnType<typeof useRunShells>

export const terminalPanelID = { agent: 'run-agent-panel', shell: 'run-shell-panel' }

export function TerminalTabs({ shells, agent }: { shells: RunShells; agent: boolean }) {
  const mobile = useIsMobile()
  const { runID, dock } = shells
  const showAgent = () => useStore.getState().setShellShown(runID, false)
  const select = (tab: string) => useStore.getState().selectShellTab(runID, tab)
  const tabs = [
    ...(agent ? [{ id: '', label: 'Agent' }] : []),
    ...(shells.canOpen ? dock.tabs.map((id, position) => {
      const terminal = dock.terminals.find((item) => item.terminal_id === id)
      return { id, label: shellLabel(terminal, (terminal ? dock.terminals.indexOf(terminal) : position) + 1) }
    }) : []),
  ]
  const active = dock.shellShown ? dock.activeTab ?? '' : ''
  const index = Math.max(0, tabs.findIndex((tab) => tab.id === active))
  const [focused, setFocused] = useState(index)
  useEffect(() => setFocused(index), [index])
  const hidden = shells.canOpen ? dock.terminals.filter((item) => !dock.tabs.includes(item.terminal_id)) : []
  const choose = (id: string) => (id ? select(id) : showAgent())

  const add = shells.canOpen && (hidden.length > 0 ? (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="ghost" size="sm" aria-label="Add a shell">
          <Plus />
          Shell
        </Button>
      </MenuTrigger>
      <MenuContent align="start">
        <MenuItem disabled={!shells.canAdd} onSelect={() => void shells.open()}>New shell</MenuItem>
        <MenuSeparator />
        {hidden.map((item) => (
          <MenuItem key={item.terminal_id} onSelect={() => select(item.terminal_id)}>Show {shellLabel(item, dock.terminals.indexOf(item) + 1)}</MenuItem>
        ))}
      </MenuContent>
    </Menu>
  ) : (
    <Button
      variant="ghost"
      size="sm"
      aria-label="New shell"
      hint={shells.canAdd ? 'Open a shell in this run’s container' : `At most ${maxShellTabs} shells`}
      aria-disabled={!shells.canAdd || undefined}
      onClick={() => {
        if (shells.canAdd) void shells.open()
      }}
    >
      <Plus />
      Shell
    </Button>
  ))

  if (mobile) {
    const current = tabs.find((tab) => tab.id === active) ?? tabs[0]
    return (
      <Menu>
        <MenuTrigger asChild>
          <Button variant="ghost" size="sm" aria-label={`Terminal: ${current?.label ?? 'none'}`}>
            {current?.label ?? 'Terminal'}
            <ChevronDown />
          </Button>
        </MenuTrigger>
        <MenuContent align="start">
          {tabs.map((tab) => (
            <MenuItem key={tab.id || 'agent'} onSelect={() => choose(tab.id)}>
              <Check className={tab.id === active ? undefined : 'invisible'} />
              {tab.label}
            </MenuItem>
          ))}
          {shells.canOpen && (
            <>
              <MenuSeparator />
              <MenuItem disabled={!shells.canAdd} onSelect={() => void shells.open()}>New shell</MenuItem>
              {hidden.map((item) => (
                <MenuItem key={item.terminal_id} onSelect={() => select(item.terminal_id)}>Show {shellLabel(item, dock.terminals.indexOf(item) + 1)}</MenuItem>
              ))}
            </>
          )}
        </MenuContent>
      </Menu>
    )
  }

  return (
    <>
      {tabs.length > 1 && (
        <div role="tablist" aria-label="Terminals" className="flex min-w-0 items-center gap-0.5 overflow-x-auto">
          {tabs.map((tab, i) => (
            <Button
              key={tab.id || 'agent'}
              role="tab"
              variant={tab.id === active ? 'secondary' : 'ghost'}
              size="sm"
              aria-selected={tab.id === active}
              aria-controls={tab.id ? terminalPanelID.shell : terminalPanelID.agent}
              tabIndex={i === focused ? 0 : -1}
              onFocus={() => setFocused(i)}
              onKeyDown={(event) => onTabListKeyDown(event, tabs.length, focused, setFocused)}
              onClick={() => choose(tab.id)}
            >
              {tab.label}
            </Button>
          ))}
        </div>
      )}
      {add}
    </>
  )
}

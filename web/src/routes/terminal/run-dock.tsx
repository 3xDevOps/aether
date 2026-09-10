import { useEffect, useRef } from 'react'
import { Dock } from '@/components/dock'
import { TerminalPane } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { cn, focusRing } from '@/lib/utils'
import { type Attachment, connectAttach, replayGate } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import {
  emitShellSocketData,
  getShellSocket,
  initialRunShellDock,
  registerShellSocket,
  subscribeShellSocket,
  unregisterShellSocket,
} from '@/store/terminal'

const maxShellTabs = 4
const shellRefusal = 'You can view this run but not open a shell in it'

export function RunDock({ runID }: { runID: string }) {
  const run = useStore((s) => s.runs[runID])
  const dock = useStore((s) => s.shellDocks[runID] ?? initialRunShellDock)
  const runDockHeight = useStore((s) => s.runDockHeight)
  const openShellTab = useStore((s) => s.openShellTab)
  const closeShellTab = useStore((s) => s.closeShellTab)
  const selectShellTab = useStore((s) => s.selectShellTab)
  const setDockCollapsed = useStore((s) => s.setDockCollapsed)
  const setRunDockHeight = useStore((s) => s.setRunDockHeight)
  const setShellRefused = useStore((s) => s.setShellRefused)
  const removeShellTab = useStore((s) => s.removeShellTab)

  const activeTab = dock.activeTab
  const paused = useStore((s) => s.pausedRuns[runID] ?? run?.paused)
  const pauseKnown = paused !== undefined
  const canOpenShell =
    pauseKnown &&
    (run?.status === 'running' || run?.status === 'needs-attention') &&
    paused === false
  const activeTabRef = useRef(activeTab)
  activeTabRef.current = activeTab
  const terminalRef = useRef<XtermController['terminal']>(null)
  const gate = useRef(replayGate((chunk, done) => terminalRef.current?.write(chunk, done)))
  // Which of the dock's four bodies is on screen. The three that are not the
  // terminal replace one that may have been holding the keyboard: the server
  // refuses a shell, the agent exits the last one, the run stops running.
  // Disposing it leaves focus on <body>, where the next keystroke reaches the
  // shell's shortcuts, so whatever took its place takes the keyboard too.
  const showing = !canOpenShell
    ? 'unavailable'
    : dock.refusedMessage !== null
      ? 'refused'
      : activeTab === null
        ? 'closed'
        : 'terminal'
  const placeholder = useRef<HTMLDivElement>(null)
  const takesFocus = { ref: placeholder, tabIndex: -1 }
  useEffect(() => {
    if (showing === 'terminal') return
    if (document.activeElement === document.body) placeholder.current?.focus()
  }, [showing])

  const controller = useXterm({
    enabled:
      canOpenShell &&
      activeTab !== null &&
      !dock.collapsed &&
      dock.refusedMessage === null,
    onData: (data) => {
      if (gate.current.muted()) return
      const tab = activeTabRef.current
      if (tab) getShellSocket(runID, tab)?.send(data)
    },
    onResize: (cols, rows) => {
      const tab = activeTabRef.current
      if (tab) getShellSocket(runID, tab)?.resize(cols, rows)
    },
  })
  const terminal = controller.terminal
  terminalRef.current = terminal
  const setFindOpen = controller.setFindOpen
  useEffect(() => {
    setFindOpen(false)
  }, [activeTab, setFindOpen])

  useEffect(() => {
    if (!canOpenShell || !activeTab || !terminal || dock.refusedMessage !== null) return

    const socketKey = activeTab
    const refuse = (message: string) => {
      setShellRefused(runID, message)
      unregisterShellSocket(runID, socketKey)
    }
    const existing = getShellSocket(runID, socketKey)
    let attachment: Attachment | null = existing ?? null
    if (!attachment) {
      attachment = connectAttach(() => api.attachShellSocket(runID, socketKey), {
        onData: (chunk, kind) =>
          emitShellSocketData(runID, socketKey, chunk, kind),
        onAttached: () => {
          // Reattach replay restores the tab's full history, so a tab switch
          // may remount its xterm instead of preserving old instances. A
          // background tab reconnecting must never wipe the active tab or
          // unmute its replay.
          if (activeTabRef.current === socketKey) {
            gate.current.unmute()
            terminalRef.current?.reset()
          }
          setShellRefused(runID, null)
        },
        onState: (connection) => {
          if (connection === 'offline' && activeTabRef.current === socketKey) {
            gate.current.unmute()
          }
        },
        // The server's message names the actual limit (steer, tab cap,
        // paused run); a lost steer capability always means the fixed
        // refusal sentence.
        onRefused: refuse,
        onWriteDenied: () => refuse(shellRefusal),
        onExit: () => removeShellTab(runID, socketKey),
        geometry: () => ({
          cols: terminalRef.current?.cols ?? 80,
          rows: terminalRef.current?.rows ?? 24,
        }),
        wantsWrite: () => true,
      })
      registerShellSocket(runID, socketKey, attachment)
    }

    const unsubscribe = subscribeShellSocket(runID, socketKey, gate.current.write)
    if (existing) attachment.reopen()
    return unsubscribe
  }, [
    activeTab,
    canOpenShell,
    dock.refusedMessage,
    removeShellTab,
    runID,
    setShellRefused,
    terminal,
  ])

  const tabs = canOpenShell ? dock.tabs.map((tab) => ({ id: tab, label: tab })) : []
  // The header strip stays live while the dock is collapsed, so a tab control
  // has to open the dock it belongs to; otherwise it would add a tab with no
  // terminal mounted to attach it.
  const open = () => {
    setDockCollapsed(runID, false)
    openShellTab(runID)
  }

  return (
    <Dock
      tabs={tabs}
      activeTab={canOpenShell ? activeTab ?? '' : ''}
      onSelectTab={(tab) => {
        setDockCollapsed(runID, false)
        selectShellTab(runID, tab)
      }}
      onAddTab={canOpenShell ? open : undefined}
      maxTabs={maxShellTabs}
      onCloseTab={(tab) => closeShellTab(runID, tab)}
      height={runDockHeight}
      onHeightChange={setRunDockHeight}
      collapsed={dock.collapsed}
      onToggleCollapse={() => setDockCollapsed(runID, !dock.collapsed)}
    >
      {showing === 'unavailable' ? (
        <div
          {...takesFocus}
          className={cn(focusRing, 'bg-muted/10 p-4 text-[13px] leading-5 text-muted-foreground')}
        >
          {pauseKnown
            ? 'Run shell unavailable: this run has no live container. The Terminal tab replays its recorded output.'
            : 'Run shell unavailable: waiting for the run pause state.'}
        </div>
      ) : showing === 'refused' ? (
        <div
          {...takesFocus}
          className={cn(
            focusRing,
            'bg-state-failed/10 p-4 text-[13px] leading-5 text-state-failed',
          )}
        >
          {dock.refusedMessage}
        </div>
      ) : showing === 'closed' ? (
        <div {...takesFocus} className={cn(focusRing, 'flex items-center bg-muted/10 p-4')}>
          <Button type="button" size="sm" onClick={open}>
            Open shell
          </Button>
        </div>
      ) : (
        <TerminalPane controller={controller} />
      )}
    </Dock>
  )
}

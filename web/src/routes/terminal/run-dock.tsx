import { useEffect, useRef } from 'react'
import { Dock } from '@/components/dock'
import { TerminalPane } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
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
  const canOpenShell = run?.status === 'running'
  const activeTabRef = useRef(activeTab)
  activeTabRef.current = activeTab
  const terminalRef = useRef<XtermController['terminal']>(null)
  const gate = useRef(replayGate((chunk, done) => terminalRef.current?.write(chunk, done)))
  const controller = useXterm({
    enabled: activeTab !== null && !dock.collapsed && dock.refusedMessage === null,
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
    if (!activeTab || !terminal || dock.refusedMessage !== null) return

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
  }, [activeTab, dock.refusedMessage, removeShellTab, runID, setShellRefused, terminal])

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
      {!canOpenShell ? (
        <div className="p-3 text-sm text-muted-foreground">
          Run shell unavailable: this run has no live container. The Terminal
          tab replays its recorded output.
        </div>
      ) : dock.refusedMessage !== null ? (
        <div className="p-3 text-sm text-muted-foreground">{dock.refusedMessage}</div>
      ) : activeTab === null ? (
        <div className="p-3">
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

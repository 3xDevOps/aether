import { useEffect, useRef } from 'react'
import { Dock } from '@/components/dock'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { type Attachment, connectAttach } from '@/routes/terminal/attach'
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
  const activeTabRef = useRef(activeTab)
  activeTabRef.current = activeTab
  const terminalRef = useRef<XtermController['terminal']>(null)
  const { hostRef, terminal } = useXterm({
    enabled: activeTab !== null && dock.refusedMessage === null,
    onData: (data) => {
      const tab = activeTabRef.current
      if (tab) getShellSocket(runID, tab)?.send(data)
    },
    onResize: (cols, rows) => {
      const tab = activeTabRef.current
      if (tab) getShellSocket(runID, tab)?.resize(cols, rows)
    },
  })
  terminalRef.current = terminal

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
        onData: (chunk) => emitShellSocketData(runID, socketKey, chunk),
        onAttached: () => {
          // Reattach replay restores the tab's full history, so a tab switch
          // may remount its xterm instead of preserving old instances. A
          // background tab reconnecting must never wipe the active tab.
          if (activeTabRef.current === socketKey) terminalRef.current?.reset()
          setShellRefused(runID, null)
        },
        onState: () => {},
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

    const unsubscribe = subscribeShellSocket(runID, socketKey, (chunk) => {
      terminalRef.current?.write(chunk)
    })
    if (existing) attachment.reopen()
    return unsubscribe
  }, [activeTab, dock.refusedMessage, removeShellTab, runID, setShellRefused, terminal])

  const tabs = dock.tabs.map((tab) => ({ id: tab, label: tab }))
  const open = () => openShellTab(runID)
  const addDisabled = dock.tabs.length >= maxShellTabs

  return (
    <Dock
      tabs={tabs}
      activeTab={activeTab ?? ''}
      onSelectTab={(tab) => selectShellTab(runID, tab)}
      onAddTab={open}
      addDisabled={addDisabled}
      onCloseTab={(tab) => closeShellTab(runID, tab)}
      height={runDockHeight}
      onHeightChange={setRunDockHeight}
      collapsed={dock.collapsed}
      onToggleCollapse={() => setDockCollapsed(runID, !dock.collapsed)}
    >
      {dock.refusedMessage !== null ? (
        <div className="p-3 text-sm text-muted-foreground">{dock.refusedMessage}</div>
      ) : activeTab === null ? (
        <div className="p-3">
          <Button type="button" size="sm" onClick={open}>
            Open shell
          </Button>
        </div>
      ) : (
        <div ref={hostRef} className="h-full min-h-0 bg-background p-2 text-foreground" />
      )}
    </Dock>
  )
}

import { useEffect, useRef, useState } from 'react'
import { Dock } from '@/components/dock'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { type Attachment, connectAttach } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import {
  emitEnvTerminalSocketData,
  getEnvTerminalSocket,
  hasEnvTerminalLineSent,
  initialEnvTerminal,
  markEnvTerminalLineSent,
  registerEnvTerminalSocket,
  setEnvTerminalSocketReady,
  subscribeEnvTerminalSocket,
  unregisterEnvTerminalSocket,
} from '@/store/env-terminal'

const maxTabs = 6

export interface TerminalDockProps {
  /** API client used by a caller that owns a test or embedded surface. */
  client?: Api
  /** Open the member's main terminal as soon as this dock mounts. */
  openOnMount?: boolean
  /** A line to type into the main tab after its first attach. */
  initialLine?: string
}

export function TerminalDock({
  client = api,
  openOnMount = false,
  initialLine,
}: TerminalDockProps) {
  const rpc = client
  const dock = useStore((s) => s.envTerminal ?? initialEnvTerminal)
  const terminalDockHeight = useStore((s) => s.terminalDockHeight)
  const openTab = useStore((s) => s.openEnvTerminalTab)
  const closeTab = useStore((s) => s.closeEnvTerminalTab)
  const selectTab = useStore((s) => s.selectEnvTerminalTab)
  const setCollapsed = useStore((s) => s.setEnvTerminalCollapsed)
  const setStatus = useStore((s) => s.setEnvTerminalStatus)
  const reset = useStore((s) => s.resetEnvTerminal)
  const setHeight = useStore((s) => s.setTerminalDockHeight)
  const sendLine = useStore((s) => s.sendLine)
  const [confirmingStop, setConfirmingStop] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [statusAttempt, setStatusAttempt] = useState(0)
  const activeTab = dock.activeTab
  const activeTabRef = useRef(activeTab)
  activeTabRef.current = activeTab
  const terminalRef = useRef<XtermController['terminal']>(null)

  const { hostRef, terminal } = useXterm({
    enabled: activeTab !== null,
    onData: (data) => {
      const tab = activeTabRef.current
      if (tab) getEnvTerminalSocket(tab)?.send(data)
    },
    onResize: (cols, rows) => {
      const tab = activeTabRef.current
      if (tab) getEnvTerminalSocket(tab)?.resize(cols, rows)
    },
  })
  terminalRef.current = terminal

  useEffect(() => {
    let live = true
    rpc
      .terminalStatus()
      .then((status) => {
        if (!live) return
        setStatus(status)
        if (status.running && useStore.getState().envTerminal.tabs.length === 0) {
          const tabs = ['main', ...(status.tabs ?? []).filter((tab) => tab !== 'main')]
          useStore.setState((s) => ({
            envTerminal: {
              ...s.envTerminal,
              tabs,
              activeTab: 'main',
            },
          }))
        }
      })
      .catch((err) => {
        if (live) setStatus(null, message(err))
      })
    return () => {
      live = false
    }
  }, [rpc, setStatus, statusAttempt])

  useEffect(() => {
    if (!openOnMount) return
    if (!dock.tabs.includes('main')) {
      openTab()
    } else if (dock.activeTab !== 'main') {
      selectTab('main')
    }
  }, [dock.activeTab, dock.tabs, openOnMount, openTab, selectTab])

  useEffect(() => {
    if (!openOnMount || !initialLine || hasEnvTerminalLineSent('main', initialLine)) return
    sendLine('main', initialLine)
    markEnvTerminalLineSent('main', initialLine)
  }, [initialLine, openOnMount, sendLine])

  useEffect(() => {
    if (!activeTab || !terminal) return

    const socketKey = activeTab
    const existing = getEnvTerminalSocket(socketKey)
    let attachment: Attachment
    if (existing) {
      attachment = existing
    } else {
      attachment = connectAttach(() => rpc.terminalSocket(socketKey), {
        onData: (chunk) => emitEnvTerminalSocketData(socketKey, chunk),
        onAttached: () => {
          setEnvTerminalSocketReady(socketKey, true)
          if (activeTabRef.current === socketKey) terminalRef.current?.reset()
          setStatus(useStore.getState().envTerminal.status, null)
        },
        onState: () => {},
        onRefused: (detail) => setStatus(useStore.getState().envTerminal.status, detail),
        onWriteDenied: () =>
          setStatus(useStore.getState().envTerminal.status, 'Terminal input was denied'),
        onExit: () => {
          setEnvTerminalSocketReady(socketKey, false)
          if (socketKey === 'main') {
            reset()
            setStatus({ running: false, tabs: [] })
          } else {
            closeTab(socketKey)
          }
        },
        geometry: () => ({
          cols: terminalRef.current?.cols ?? 80,
          rows: terminalRef.current?.rows ?? 24,
        }),
        wantsWrite: () => true,
      })
      registerEnvTerminalSocket(socketKey, attachment)
    }

    setEnvTerminalSocketReady(socketKey, false)
    const unsubscribe = subscribeEnvTerminalSocket(socketKey, (chunk) => {
      terminalRef.current?.write(chunk)
    })
    if (existing) attachment.reopen()
    return () => {
      unsubscribe()
      if (activeTabRef.current !== socketKey) unregisterEnvTerminalSocket(socketKey)
    }
  }, [activeTab, closeTab, reset, rpc, setStatus, terminal])

  const stop = async () => {
    if (stopping) return
    setStopping(true)
    try {
      await rpc.terminalStop()
      setConfirmingStop(false)
      reset()
      setStatus({ running: false, tabs: [] })
    } catch (err) {
      setStatus(dock.status, message(err))
    } finally {
      setStopping(false)
    }
  }

  const tabs = dock.tabs.map((tab) => ({ id: tab, label: tab, permanent: tab === 'main' }))
  const empty = dock.tabs.length === 0 && dock.status?.running !== true
  const loading = dock.status === null && dock.statusError === null
  const addDisabled = dock.tabs.length >= maxTabs

  return (
    <>
      <Dock
        tabs={tabs}
        activeTab={activeTab ?? ''}
        onSelectTab={selectTab}
        onAddTab={openTab}
        addDisabled={addDisabled}
        onCloseTab={closeTab}
        height={terminalDockHeight}
        onHeightChange={setHeight}
        collapsed={dock.collapsed}
        onToggleCollapse={() => setCollapsed(!dock.collapsed)}
        actions={
          (dock.status?.running || dock.tabs.length > 0) && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => setConfirmingStop(true)}
              disabled={stopping}
            >
              Stop environment
            </Button>
          )
        }
      >
        {loading ? (
          <p className="p-3 text-sm text-muted-foreground">Checking environment...</p>
        ) : dock.statusError ? (
          <div className="space-y-2 p-3 text-sm">
            <p className="text-state-failed">{dock.statusError}</p>
            {empty && (
              <Button
                type="button"
                size="sm"
                onClick={() => {
                  setStatus(null)
                  setStatusAttempt((attempt) => attempt + 1)
                }}
              >
                Retry
              </Button>
            )}
          </div>
        ) : empty ? (
          <div className="space-y-2 p-3 text-sm">
            <p>Your environment starts on first open</p>
            <Button type="button" size="sm" onClick={openTab}>
              Open
            </Button>
          </div>
        ) : activeTab === null ? (
          <div className="p-3">
            <Button type="button" size="sm" onClick={openTab}>
              Open
            </Button>
          </div>
        ) : (
          <div ref={hostRef} className="h-full min-h-0 bg-background p-2 text-foreground" />
        )}
      </Dock>
      {confirmingStop && (
        <Dialog open onOpenChange={setConfirmingStop}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Stop your environment?</DialogTitle>
              <DialogDescription>
                The environment container stops now. Your home files remain and a later open starts it again.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setConfirmingStop(false)} disabled={stopping}>
                Cancel
              </Button>
              <Button type="button" variant="destructive" onClick={() => void stop()} disabled={stopping}>
                {stopping ? 'Stopping...' : 'Stop environment'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}

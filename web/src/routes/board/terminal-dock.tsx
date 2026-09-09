import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Dock } from '@/components/dock'
import { TerminalPane, TerminalSpinner } from '@/components/terminal-pane'
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
import { openOAuthLink } from '@/lib/oauth-forward'
import { type Attachment, connectAttach, replayGate } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
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
  const openForwardDialog = useStore((s) => s.openForwardDialog)
  const capability = useCapability()
  const openTab = useStore((s) => s.openEnvTerminalTab)
  const closeTab = useStore((s) => s.closeEnvTerminalTab)
  const selectTab = useStore((s) => s.selectEnvTerminalTab)
  const setCollapsed = useStore((s) => s.setEnvTerminalCollapsed)
  const setStatus = useStore((s) => s.setEnvTerminalStatus)
  const reset = useStore((s) => s.resetEnvTerminal)
  const setHeight = useStore((s) => s.setTerminalDockHeight)
  const sendLine = useStore((s) => s.sendLine)
  const [confirmingStop, setConfirmingStop] = useState(false)
  const [confirmingReset, setConfirmingReset] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [savedConfirmation, setSavedConfirmation] = useState(false)
  const [stopError, setStopError] = useState<string | null>(null)
  const [resetError, setResetError] = useState<string | null>(null)
  const [statusAttempt, setStatusAttempt] = useState(0)
  const [attachedTab, setAttachedTab] = useState<string | null>(null)
  const activeTab = dock.activeTab
  const activeTabRef = useRef(activeTab)
  activeTabRef.current = activeTab
  const terminalRef = useRef<XtermController['terminal']>(null)
  const gate = useRef(replayGate((chunk, done) => terminalRef.current?.write(chunk, done)))

  const controller = useXterm({
    enabled: activeTab !== null && !dock.collapsed,
    onData: (data) => {
      if (gate.current.muted()) return
      const tab = activeTabRef.current
      if (tab) getEnvTerminalSocket(tab)?.send(data)
    },
    onResize: (cols, rows) => {
      const tab = activeTabRef.current
      if (tab) getEnvTerminalSocket(tab)?.resize(cols, rows)
    },
    onLink: (uri) => {
      if (!capability.hasLocal('forward.start')) return false
      return openOAuthLink(
        rpc,
        'terminal',
        uri,
        (port) => toast.success(`OAuth callback ready on localhost:${port}`),
        (err) => toast.error(`OAuth callback forward failed: ${message(err)}`),
      )
    },
  })
  const terminal = controller.terminal
  terminalRef.current = terminal
  const setFindOpen = controller.setFindOpen
  useEffect(() => {
    setFindOpen(false)
  }, [activeTab, setFindOpen])

  useEffect(() => {
    if (!savedConfirmation) return
    const timer = window.setTimeout(() => setSavedConfirmation(false), 4000)
    return () => window.clearTimeout(timer)
  }, [savedConfirmation])

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

  // The dock is collapsed by default, but a caller that opens it on mount
  // means to show the terminal - the Agents and GitHub steps type into it.
  // Once only: re-running this whenever `collapsed` changed would undo the
  // member's own press of the collapse chevron on the same tick.
  const expandedOnMount = useRef(false)
  useEffect(() => {
    if (!openOnMount || expandedOnMount.current) return
    expandedOnMount.current = true
    setCollapsed(false)
  }, [openOnMount, setCollapsed])

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
        onData: (chunk, kind) =>
          emitEnvTerminalSocketData(socketKey, chunk, kind),
        onAttached: () => {
          setEnvTerminalSocketReady(socketKey, true)
          setAttachedTab(socketKey)
          if (activeTabRef.current === socketKey) {
            gate.current.unmute()
            terminalRef.current?.reset()
          }
          const status = useStore.getState().envTerminal.status
          setStatus({ ...(status ?? { running: false, tabs: [] }), running: true }, null)
        },
        onState: (connection) => {
          if (connection === 'offline' && activeTabRef.current === socketKey) {
            gate.current.unmute()
          }
        },
        onRefused: (detail) => setStatus(useStore.getState().envTerminal.status, detail),
        onWriteDenied: () =>
          setStatus(useStore.getState().envTerminal.status, 'Terminal input was denied'),
        onExit: () => {
          setEnvTerminalSocketReady(socketKey, false)
          if (socketKey === 'main') {
            const status = useStore.getState().envTerminal.status
            reset()
            setStatus({ ...status, running: false, tabs: [] })
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
    setAttachedTab(null)
    const unsubscribe = subscribeEnvTerminalSocket(socketKey, gate.current.write)
    if (existing) attachment.reopen()
    return () => {
      unsubscribe()
      if (activeTabRef.current !== socketKey) unregisterEnvTerminalSocket(socketKey)
    }
  }, [activeTab, closeTab, reset, rpc, setStatus, terminal])

  const save = async () => {
    if (saving) return
    setSaving(true)
    try {
      const result = await rpc.envSave()
      const status = useStore.getState().envTerminal.status
      setStatus({ ...(status ?? { running: true, tabs: [] }), saved_image: result.image })
      setSavedConfirmation(true)
    } catch (err) {
      setStatus(useStore.getState().envTerminal.status, message(err))
    } finally {
      setSaving(false)
    }
  }

  const resetEnvironment = async () => {
    if (resetting) return
    setResetting(true)
    setResetError(null)
    try {
      await rpc.envReset()
      setConfirmingReset(false)
      reset()
      setStatus({ running: false, tabs: [], saved_image: '' })
    } catch (err) {
      setResetError(message(err))
    } finally {
      setResetting(false)
    }
  }

  const stop = async () => {
    if (stopping) return
    setStopping(true)
    setStopError(null)
    try {
      await rpc.terminalStop()
      const status = useStore.getState().envTerminal.status
      setConfirmingStop(false)
      reset()
      setStatus({ ...status, running: false, tabs: [] })
    } catch (err) {
      setStopError(message(err))
    } finally {
      setStopping(false)
    }
  }

  const tabs = dock.tabs.map((tab) => ({ id: tab, label: tab, permanent: tab === 'main' }))
  const empty = dock.tabs.length === 0 && dock.status?.running !== true
  const loading = dock.status === null && dock.statusError === null

  return (
    <>
      <Dock
        tabs={tabs}
        activeTab={activeTab ?? ''}
        onSelectTab={(tab) => {
          setCollapsed(false)
          selectTab(tab)
        }}
        onAddTab={() => {
          setCollapsed(false)
          openTab()
        }}
        maxTabs={maxTabs}
        onCloseTab={closeTab}
        height={terminalDockHeight}
        onHeightChange={setHeight}
        collapsed={dock.collapsed}
        onToggleCollapse={() => setCollapsed(!dock.collapsed)}
        actions={
          (!empty || !!dock.status?.saved_image) && (
            <div className="flex items-center gap-1">
              {dock.status?.running && (
                <>
                {capability.hasLocal('forward.start') && (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => openForwardDialog('terminal')}
                  >
                    Forward port
                  </Button>
                )}
                <Button
                  type="button"
                  size="sm"
                  variant="default"
                  onClick={() => void save()}
                  disabled={saving}
                >
                  {saving ? 'Saving...' : 'Save environment'}
                </Button>
                </>
              )}
              {!empty && (
                <Button
                  type="button"
                  size="sm"
                  onClick={() => {
                    setStopError(null)
                    setConfirmingStop(true)
                  }}
                  disabled={stopping}
                >
                  Stop environment
                </Button>
              )}
              {dock.status?.saved_image && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setResetError(null)
                    setConfirmingReset(true)
                  }}
                  disabled={resetting}
                >
                  Reset to standard
                </Button>
              )}
              {savedConfirmation && (
                <span className="text-xs text-muted-foreground">
                  Saved - new runs use this environment
                </span>
              )}
            </div>
          )
        }
      >
        <div className="flex h-full min-h-0 flex-col">
          {dock.status?.running && !dock.status.saved_image && (
            <p className="text-xs text-muted-foreground px-3 pt-1">
              Installs here reach agents after you save.
            </p>
          )}
          <div className="min-h-0 flex-1">
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
              <TerminalPane controller={controller}>
                {attachedTab !== activeTab && (
                  // Only a terminal the dock has not seen running is starting
                  // a container. A second tab, a tab switch or an expanded
                  // dock is reattaching to one that is up.
                  <TerminalSpinner
                    label={
                      dock.status?.running
                        ? 'Connecting to your environment'
                        : 'Starting your environment container'
                    }
                  />
                )}
              </TerminalPane>
            )}
          </div>
        </div>
      </Dock>
      {confirmingStop && (
        <Dialog
          open
          onOpenChange={(open) => {
            // The dialog is where a failed stop is reported, so Escape and an
            // outside click stay shut off until the call settles.
            if (!stopping) setConfirmingStop(open)
          }}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Stop your environment?</DialogTitle>
              <DialogDescription>
                The environment container stops now. Your home files and your
                saved image remain, and a later open starts it again.
              </DialogDescription>
            </DialogHeader>
            {stopError && <p className="text-sm text-state-failed">{stopError}</p>}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setConfirmingStop(false)}
                disabled={stopping}
              >
                Cancel
              </Button>
              <Button
                type="button"
                onClick={() => void stop()}
                disabled={stopping}
              >
                {stopping ? 'Stopping...' : 'Stop environment'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
      {confirmingReset && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!resetting) setConfirmingReset(open)
          }}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Reset to the standard image?</DialogTitle>
              <DialogDescription>
                Your saved image {dock.status?.saved_image} is deleted
                {dock.status?.running && ' and the environment container stops'}.
                Your home files remain, and the next open starts from the
                standard image.
              </DialogDescription>
            </DialogHeader>
            {resetError && <p className="text-sm text-state-failed">{resetError}</p>}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setConfirmingReset(false)}
                disabled={resetting}
              >
                Cancel
              </Button>
              <Button
                type="button"
                variant="destructive"
                onClick={() => void resetEnvironment()}
                disabled={resetting}
              >
                {resetting ? 'Resetting...' : 'Reset to standard'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}

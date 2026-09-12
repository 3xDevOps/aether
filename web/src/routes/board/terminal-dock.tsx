import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Dock, type DockContainment } from '@/components/dock'
import { TerminalPane, TerminalSpinner } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { openOAuthLink, remoteOAuthInstructions } from '@/lib/oauth-forward'
import type { ConnectionState } from '@/lib/stream'
import {
  type AttachDataKind,
  type Attachment,
  connectAttach,
  replayGate,
  standardGeometry,
} from '@/routes/terminal/attach'
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
  /**
   * Whether the dock is bounded by its immediate parent. Embedded, intrinsic
   * sections use the viewport cap; fixed flex layouts opt into their boundary.
   */
  containment?: DockContainment
}

export function TerminalDock({
  client = api,
  openOnMount = false,
  initialLine,
  containment = 'viewport',
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

  // A member can have this terminal open on more than one screen; a phone
  // follows what the others made it rather than shrinking it for them.
  const phone = useMediaQuery(phoneScreen)
  const controller = useXterm({
    enabled: activeTab !== null && !dock.collapsed,
    follow: phone,
    onData: (data) => {
      if (!activeTab || activeTabRef.current !== activeTab || gate.current.muted()) return
      getEnvTerminalSocket(activeTab)?.send(data)
    },
    onResize: (cols, rows) => {
      if (!activeTab || activeTabRef.current !== activeTab) return
      getEnvTerminalSocket(activeTab)?.resize(cols, rows)
    },
    onLink: (uri) => {
      if (!capability.hasLocal('forward.start')) {
        const remote = remoteOAuthInstructions('terminal', uri)
        if (remote === null) return false
        toast.info(`Run ${remote.command}, then open this link on that machine.`, {
          description: uri,
          action: { label: 'Copy link', onClick: () => void copyText(uri, null) },
        })
        return true
      }
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
  const { geometry, setGeometry } = controller
  // Keep persistent socket callbacks from reaching a disposed xterm while a
  // route remount is between hosts.
  useEffect(() => {
    terminalRef.current = terminal
    return () => {
      if (terminalRef.current === terminal) terminalRef.current = null
    }
  }, [terminal])
  const setFindOpen = controller.setFindOpen
  const focusTerminal = controller.focusTerminal
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
    const isCurrent = () => activeTabRef.current === socketKey
    const handlers = {
      onData: (chunk: Uint8Array, kind: AttachDataKind) =>
        emitEnvTerminalSocketData(socketKey, chunk, kind),
      onAttached: (_write: boolean, size: { cols: number; rows: number }) => {
        if (isCurrent()) {
          setEnvTerminalSocketReady(socketKey, true)
          setAttachedTab(socketKey)
          gate.current.unmute()
          setGeometry(size.cols, size.rows, true)
          const status = useStore.getState().envTerminal.status
          setStatus({ ...(status ?? { running: false, tabs: [] }), running: true }, null)
        }
      },
      onState: (connection: ConnectionState) => {
        if (!isCurrent()) return
        if (connection !== 'live') {
          setEnvTerminalSocketReady(socketKey, false)
          setAttachedTab(null)
          if (connection === 'offline') gate.current.unmute()
        }
      },
      onRefused: (detail: string) => {
        if (!isCurrent()) return
        setAttachedTab(null)
        setStatus(useStore.getState().envTerminal.status, detail)
      },
      onWriteDenied: () => {
        if (!isCurrent()) return
        setAttachedTab(null)
        setStatus(useStore.getState().envTerminal.status, 'Terminal input was denied')
      },
      onExit: () => {
        if (isCurrent()) {
          setEnvTerminalSocketReady(socketKey, false)
          setAttachedTab(null)
          if (socketKey === 'main') {
            const status = useStore.getState().envTerminal.status
            reset()
            setStatus({ ...status, running: false, tabs: [] })
          }
        }
        if (socketKey !== 'main') closeTab(socketKey)
      },
      geometry: () => isCurrent() ? geometry() : standardGeometry,
      wantsWrite: () => true,
      follows: () => phone,
      onGeometry: (cols: number, rows: number) => {
        if (isCurrent()) setGeometry(cols, rows)
      },
    }
    const existing = getEnvTerminalSocket(socketKey)
    let attachment: Attachment
    if (existing) {
      attachment = existing
      attachment.rebind(handlers)
    } else {
      attachment = connectAttach(() => rpc.terminalSocket(socketKey), handlers)
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
  }, [activeTab, closeTab, geometry, phone, reset, rpc, setGeometry, setStatus, terminal])

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
  const open = () => {
    setCollapsed(false)
    const opened = openTab()
    if (opened) focusTerminal()
  }

  return (
    <>
      <Dock
        tabs={tabs}
        activeTab={activeTab ?? ''}
        onSelectTab={(tab) => {
          setCollapsed(false)
          selectTab(tab)
          focusTerminal()
        }}
        onAddTab={open}
        maxTabs={maxTabs}
        onCloseTab={closeTab}
        height={terminalDockHeight}
        onHeightChange={setHeight}
        collapsed={dock.collapsed}
        containment={containment}
        onToggleCollapse={() => {
          const expanding = dock.collapsed
          setCollapsed(!dock.collapsed)
          if (expanding && activeTab !== null) focusTerminal()
        }}
        actions={
          (!empty || !!dock.status?.saved_image) && (
            <div className="flex max-w-full flex-wrap items-center justify-end gap-1">
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
                  variant="ghost"
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
        <div className="flex h-full min-h-0 flex-col overflow-hidden">
          {dock.status?.running && !dock.status.saved_image && (
            <p className="shrink-0 border-b border-border bg-sidebar px-3 py-1 text-[12px] text-muted-foreground">
              Installs here reach agents after you save.
            </p>
          )}
          <div className="min-h-0 flex-1 overflow-hidden">
            {loading ? (
              <p className="bg-background p-3 text-[13px] text-muted-foreground">Checking environment...</p>
            ) : dock.statusError ? (
              <div className="h-full min-h-0 min-w-0 space-y-2 overflow-y-auto break-words whitespace-pre-wrap bg-background p-3 text-[13px]">
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
              <div className="space-y-2 bg-background p-3 text-[13px]">
                <p>Your environment starts on first open</p>
                <Button type="button" size="sm" onClick={open}>
                  Open
                </Button>
              </div>
            ) : activeTab === null ? (
              <div className="bg-background p-3">
                <Button type="button" size="sm" onClick={open}>
                  Open
                </Button>
              </div>
            ) : (
              <TerminalPane
                controller={controller}
                className="overflow-auto"
                imageTargetKey={activeTab ?? undefined}
                imageUploadEnabled={attachedTab === activeTab && activeTab !== null}
              >
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
        <AlertDialog
          open
          onOpenChange={(open) => {
            // A failed stop is reported in here, so Escape stays off until
            // the call settles.
            if (!stopping) setConfirmingStop(open)
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Stop your environment?</AlertDialogTitle>
              <AlertDialogDescription>
                The environment container stops now. Your home files and your
                saved image remain, and a later open starts it again.
              </AlertDialogDescription>
            </AlertDialogHeader>
            {stopError && (
              <p role="alert" className="text-sm text-state-failed">
                {stopError}
              </p>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={stopping}>Cancel</AlertDialogCancel>
              <AlertDialogAction
                variant="default"
                onClick={(event) => {
                  event.preventDefault()
                  void stop()
                }}
                disabled={stopping}
              >
                {stopping ? 'Stopping...' : 'Stop environment'}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
      {confirmingReset && (
        <AlertDialog
          open
          onOpenChange={(open) => {
            if (!resetting) setConfirmingReset(open)
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Reset to the standard image?</AlertDialogTitle>
              <AlertDialogDescription>
                Your saved image {dock.status?.saved_image} is deleted
                {dock.status?.running && ' and the environment container stops'}.
                Your home files remain, and the next open starts from the
                standard image.
              </AlertDialogDescription>
            </AlertDialogHeader>
            {resetError && (
              <p role="alert" className="text-sm text-state-failed">
                {resetError}
              </p>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={resetting}>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={(event) => {
                  event.preventDefault()
                  void resetEnvironment()
                }}
                disabled={resetting}
              >
                {resetting ? 'Resetting...' : 'Reset to standard'}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </>
  )
}

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
import {
  Menu,
  MenuContent,
  MenuItem,
  MenuTrigger,
} from '@/components/ui/menu'
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
import { StopEnvironmentDialog } from '@/routes/environment/stop-environment-dialog'
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
const emptyReplay = new Uint8Array()

interface StructuralReplayState {
  attachmentGeneration: number
  controller: XtermController
  controllerGeneration: number
  revision: number
}

export interface TerminalDockProps {
  client?: Api
  openOnMount?: boolean
  /** A line to type into the main tab after its first attach. */
  initialLine?: string
  /** Embedded intrinsic sections use the viewport cap; fixed flex layouts opt into 'parent'. */
  containment?: DockContainment
}

export function TerminalDock({
  client = api,
  openOnMount = false,
  initialLine,
  containment = 'viewport',
}: TerminalDockProps) {

  const rpc = client
  const stored = useStore((s) => s.envTerminal ?? initialEnvTerminal)
  const dock = containment === 'fill' ? { ...stored, collapsed: false } : stored
  const terminalDockHeight = useStore((s) => s.terminalDockHeight)
  const openForwardDialog = useStore((s) => s.openForwardDialog)
  const paletteDialog = useStore((s) => s.paletteDialog)
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
  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [savedConfirmation, setSavedConfirmation] = useState(false)
  const [resetError, setResetError] = useState<string | null>(null)
  const [statusAttempt, setStatusAttempt] = useState(0)
  const [attachedTab, setAttachedTab] = useState<string | null>(null)
  const [replaying, setReplaying] = useState(false)
  const actionsTrigger = useRef<HTMLButtonElement>(null)
  const openTrigger = useRef<HTMLButtonElement>(null)
  const returnToActions = useRef(false)

  useEffect(() => {
    if (!returnToActions.current || confirmingStop || confirmingReset || paletteDialog === 'forward') return
    returnToActions.current = false
    if (paletteDialog === null) (actionsTrigger.current ?? openTrigger.current)?.focus()
  }, [confirmingStop, confirmingReset, paletteDialog])

  const activeTab = dock.activeTab
  const activeTabRef = useRef(activeTab)
  activeTabRef.current = activeTab
  const terminalRef = useRef<XtermController['terminal']>(null)
  const controllerRef = useRef<XtermController | null>(null)
  const attachmentGenerationRef = useRef(0)
  const replayRevisionRef = useRef(0)
  const structuralReplayRef = useRef<StructuralReplayState | null>(null)
  const fullReplaySettlingGenerationRef = useRef<number | null>(null)
  const gate = useRef(
    replayGate(
      (chunk, done) => terminalRef.current?.write(chunk, done),
      (nextReplaying, full = true) => {
        if (nextReplaying) {
          setReplaying(full)
          return
        }
        if (!full) {
          setReplaying(false)
          return
        }
        const replay = structuralReplayRef.current
        if (!replay) {
          if (
            fullReplaySettlingGenerationRef.current !== null &&
            attachmentGenerationRef.current === fullReplaySettlingGenerationRef.current
          ) {
            fullReplaySettlingGenerationRef.current = null
          }
          setReplaying(false)
          return
        }
        void (async () => {
          try {
            await replay.controller.finishStructuralReplay?.(replay.controllerGeneration)
          } catch {
            // A failed viewport restore must not leave the replay hidden.
          }
          if (
            structuralReplayRef.current !== replay ||
            replayRevisionRef.current !== replay.revision ||
            attachmentGenerationRef.current !== replay.attachmentGeneration
          ) return
          if (fullReplaySettlingGenerationRef.current === replay.attachmentGeneration) {
            fullReplaySettlingGenerationRef.current = null
          }
          structuralReplayRef.current = null
          setReplaying(false)
        })()
      },
    ),
  )
  // A member can have this terminal open on more than one screen; a phone
  // follows what the others made it rather than shrinking it for them.
  const phone = useMediaQuery(phoneScreen)
  const controller = useXterm({
    enabled: activeTab !== null && !dock.collapsed,
    follow: phone,
    onData: (data) => {
      if (
        !activeTab ||
        activeTabRef.current !== activeTab ||
        fullReplaySettlingGenerationRef.current !== null ||
        gate.current.muted()
      ) return
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
  controllerRef.current = controller
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

  // Once only: re-running on every `collapsed` change would undo the member's
  // own press of the collapse chevron on the same tick.
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
    const attachmentGeneration = ++attachmentGenerationRef.current
    let replayAccepted = false
    const isCurrent = () =>
      attachmentGenerationRef.current === attachmentGeneration &&
      activeTabRef.current === socketKey
    const cancelStructuralReplay = () => {
      const replay = structuralReplayRef.current
      if (replay?.attachmentGeneration === attachmentGeneration) {
        structuralReplayRef.current = null
        replayRevisionRef.current++
        void replay.controller.cancelStructuralReplay?.(replay.controllerGeneration)
      }
      if (fullReplaySettlingGenerationRef.current === attachmentGeneration) {
        fullReplaySettlingGenerationRef.current = null
      }
    }
    const handlers = {
      onData: (chunk: Uint8Array, kind: AttachDataKind, settled?: () => void) =>
        emitEnvTerminalSocketData(socketKey, chunk, kind, settled),
      onAttached: (_write: boolean, size: { cols: number; rows: number }, resumed = false) => {
        if (isCurrent()) {
          setEnvTerminalSocketReady(socketKey, true)
          setAttachedTab(socketKey)
          replayAccepted = true
          if (resumed) {
            cancelStructuralReplay()
          } else {
            fullReplaySettlingGenerationRef.current = attachmentGeneration
            const replayController = controllerRef.current
            const controllerGeneration = replayController?.beginStructuralReplay?.()
            const revision = ++replayRevisionRef.current
            structuralReplayRef.current =
              replayController === null || controllerGeneration === undefined
                ? null
                : {
                    attachmentGeneration,
                    controller: replayController,
                    controllerGeneration,
                    revision,
                  }
          }
          setGeometry(size.cols, size.rows, !resumed)
          const status = useStore.getState().envTerminal.status
          setStatus({ ...(status ?? { running: false, tabs: [] }), running: true }, null)
        }
      },
      onReplayAbort: (full = true) => {
        if (!isCurrent()) return
        replayAccepted = false
        if (full) cancelStructuralReplay()
        gate.current.cancel(full)
      },
      onReplayStart: (bytes: number, full = true) => {
        if (!isCurrent()) return
        if (!replayAccepted) return
        replayAccepted = false
        if (full) fullReplaySettlingGenerationRef.current = attachmentGeneration
        gate.current.start(full ? 'full' : 'delta')
        if (bytes === 0) void gate.current.write(emptyReplay, 'replay-end')
      },
      onState: (connection: ConnectionState) => {
        if (!isCurrent()) return
        if (connection !== 'live') {
          setEnvTerminalSocketReady(socketKey, false)
          setAttachedTab(null)
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
    return () => {
      unsubscribe()
      cancelStructuralReplay()
      if (attachmentGenerationRef.current === attachmentGeneration) {
        attachmentGenerationRef.current++
        gate.current.cancel(true)
      }
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
                <Button
                  type="button"
                  size="sm"
                  variant="primary"
                  onClick={() => void save()}
                  disabled={saving}
                >
                  {saving ? 'Saving...' : 'Save environment'}
                </Button>
              )}
              <Menu>
                <MenuTrigger asChild>
                  <Button ref={actionsTrigger} type="button" size="sm" variant="ghost" aria-label="Environment actions">
                    More
                  </Button>
                </MenuTrigger>
                <MenuContent align="end" onCloseAutoFocus={(event) => {
                  if (returnToActions.current) event.preventDefault()
                }}>
                  {dock.status?.running && capability.hasLocal('forward.start') && (
                    <MenuItem onSelect={() => {
                      returnToActions.current = true
                      openForwardDialog('terminal')
                    }}>
                      Forward port
                    </MenuItem>
                  )}
                  {!empty && (
                    <MenuItem onSelect={() => {
                      returnToActions.current = true
                      setConfirmingStop(true)
                    }}>
                      Stop environment
                    </MenuItem>
                  )}
                  {dock.status?.saved_image && (
                    <MenuItem
                      onSelect={() => {
                        returnToActions.current = true
                        setResetError(null)
                        setConfirmingReset(true)
                      }}
                      disabled={resetting}
                    >
                      Reset to standard
                    </MenuItem>
                  )}
                </MenuContent>
              </Menu>
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
              <p className="h-full bg-background p-3 text-[13px] text-muted-foreground">Checking environment...</p>
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
              <div className="h-full space-y-2 bg-background p-3 text-[13px]">
                <p>Your environment starts on first open</p>
                <Button ref={openTrigger} type="button" size="sm" onClick={open}>
                  Open
                </Button>
              </div>
            ) : activeTab === null ? (
              <div className="h-full bg-background p-3">
                <Button ref={openTrigger} type="button" size="sm" onClick={open}>
                  Open
                </Button>
              </div>
            ) : (
              <TerminalPane
                controller={controller}
                writable={!replaying}
                className="overflow-auto"
                replaying={replaying}
                imageTargetKey={activeTab ?? undefined}
                imageUploadEnabled={attachedTab === activeTab && activeTab !== null}
              >
                {attachedTab !== activeTab && (
                  // Only a terminal the dock has not seen running is starting a
                  // container; anything else is reattaching to one that is up.
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
        <StopEnvironmentDialog client={rpc} onClose={() => setConfirmingStop(false)} />
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

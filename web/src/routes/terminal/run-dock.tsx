import { useEffect, useRef, useState } from 'react'
import { Dock } from '@/components/dock'
import { TerminalPane } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { cn, focusRing } from '@/lib/utils'
import { type ConnectionState } from '@/lib/stream'
import {
  type AttachDataKind,
  type Attachment,
  type ControlMetadata,
  connectAttach,
  replayGate,
  standardGeometry,
} from '@/routes/terminal/attach'
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
const shellControlMoved = 'Read-only shell. Another session controls this run.'
const emptyReplay = new Uint8Array()

interface ShellAttachmentIdentity {
  runID: string
  tab: string
}

interface StructuralReplayState {
  attachmentGeneration: number
  controllerGeneration: number
  revision: number
}

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
  const [attachedIdentity, setAttachedIdentity] = useState<ShellAttachmentIdentity | null>(null)
  const [replaying, setReplaying] = useState(false)
  const currentAttachmentRef = useRef<ShellAttachmentIdentity | null>(null)
  const terminalRef = useRef<XtermController['terminal']>(null)
  const controllerRef = useRef<XtermController | null>(null)
  const attachmentGenerationRef = useRef(0)
  const replayRevisionRef = useRef(0)
  const structuralReplayRef = useRef<StructuralReplayState | null>(null)
  const writeRequested = useRef<Record<string, boolean>>({})
  const controlHeld = useRef<Record<string, boolean>>({})
  const [controlState, setControlState] = useState<{ key: string; held: boolean } | null>(null)
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
          setReplaying(false)
          return
        }
        const controller = controllerRef.current
        void (async () => {
          try {
            await controller?.finishStructuralReplay?.(replay.controllerGeneration)
          } catch {
            // The parsed replay is still authoritative. A failed viewport
            // restore must not leave it permanently hidden.
          }
          if (
            structuralReplayRef.current !== replay ||
            replayRevisionRef.current !== replay.revision ||
            attachmentGenerationRef.current !== replay.attachmentGeneration
          ) return
          structuralReplayRef.current = null
          setReplaying(false)
        })()
      },
    ),
  )
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

  // A shell tab is one session shared by everyone on that tab, so a phone
  // follows it for the same reason it follows the agent's terminal.
  const phone = useMediaQuery(phoneScreen)
  const controller = useXterm({
    enabled:
      canOpenShell &&
      activeTab !== null &&
      !dock.collapsed &&
      dock.refusedMessage === null,
    follow: phone,
    onData: (data) => {
      if (!activeTab) return
      const current = currentAttachmentRef.current
      const key = `${runID}:${activeTab}`
      if (
        current?.runID !== runID ||
        current.tab !== activeTab ||
        controlHeld.current[key] !== true ||
        gate.current.muted()
      ) return
      getShellSocket(runID, activeTab)?.send(data)
    },
    onResize: (cols, rows) => {
      if (!activeTab) return
      const current = currentAttachmentRef.current
      if (current?.runID !== runID || current.tab !== activeTab) return
      getShellSocket(runID, activeTab)?.resize(cols, rows)
    },
  })
  controllerRef.current = controller
  const terminal = controller.terminal
  terminalRef.current = terminal
  const { geometry, setGeometry } = controller
  currentAttachmentRef.current =
    canOpenShell &&
    activeTab !== null &&
    !dock.collapsed &&
    dock.refusedMessage === null &&
    terminal
      ? { runID, tab: activeTab }
      : null
  // The socket can outlive this dock, but its old callback must not keep a
  // disposed xterm reachable during the gap before a remount rebinds it.
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
    if (!canOpenShell || !activeTab || !terminal || dock.refusedMessage !== null) return

    const socketKey = activeTab
    const identity: ShellAttachmentIdentity = { runID, tab: socketKey }
    const controlKey = `${runID}:${socketKey}`
    const attachmentGeneration = ++attachmentGenerationRef.current
    let replayAccepted = false
    if (writeRequested.current[controlKey] === undefined) {
      writeRequested.current[controlKey] = true
    }
    const isCurrent = () => {
      const current = currentAttachmentRef.current
      return (
        attachmentGenerationRef.current === attachmentGeneration &&
        current?.runID === identity.runID &&
        current.tab === identity.tab
      )
    }
    const cancelStructuralReplay = () => {
      const replay = structuralReplayRef.current
      if (!replay || replay.attachmentGeneration !== attachmentGeneration) return
      structuralReplayRef.current = null
      replayRevisionRef.current++
      void controllerRef.current?.cancelStructuralReplay?.(replay.controllerGeneration)
    }
    const clearAttached = () => {
      if (isCurrent()) setAttachedIdentity(null)
    }
    const refuse = (message: string) => {
      if (isCurrent()) {
        clearAttached()
        setShellRefused(runID, message)
      }
      unregisterShellSocket(runID, socketKey)
    }
    const handlers = {
      onData: (chunk: Uint8Array, kind: AttachDataKind, settled?: () => void) =>
        emitShellSocketData(runID, socketKey, chunk, kind, settled),
      onAttached: (_write: boolean, size: { cols: number; rows: number }, resumed = false) => {
        // Reattach replay restores the tab's full history, so a tab switch
        // may remount its xterm instead of preserving old instances. A
        // background tab reconnecting must never wipe the active tab or
        // unmute its replay.
        if (isCurrent()) {
          setAttachedIdentity(identity)
          replayAccepted = true
          if (resumed) {
            cancelStructuralReplay()
          } else {
            const controllerGeneration = controllerRef.current?.beginStructuralReplay?.()
            const revision = ++replayRevisionRef.current
            structuralReplayRef.current =
              controllerGeneration === undefined
                ? null
                : { attachmentGeneration, controllerGeneration, revision }
          }
          setGeometry(size.cols, size.rows, !resumed)
          setShellRefused(runID, null)
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
        gate.current.start(full ? 'full' : 'delta')
        if (bytes === 0) void gate.current.write(emptyReplay, 'replay-end')
      },
      onState: (connection: ConnectionState) => {
        if (isCurrent()) {
          if (connection !== 'live') setAttachedIdentity(null)
        }
      },
      // The server's message names the actual limit (steer, tab cap,
      // paused run); a lost steer capability always means the fixed
      // refusal sentence.
      onRefused: refuse,
      onControl: (metadata: ControlMetadata) => {
        controlHeld.current[controlKey] = metadata.has_control
        if (isCurrent()) setControlState({ key: controlKey, held: metadata.has_control })
      },
      onControlLost: () => {
        writeRequested.current[controlKey] = false
        controlHeld.current[controlKey] = false
        if (isCurrent()) setControlState({ key: controlKey, held: false })
      },
      onWriteDenied: () => refuse(shellRefusal),
      onExit: () => {
        clearAttached()
        removeShellTab(runID, socketKey)
      },
      geometry: () => isCurrent() ? geometry() : standardGeometry,
      wantsWrite: () => writeRequested.current[controlKey] !== false,
      follows: () => phone,
      onGeometry: (cols: number, rows: number) => {
        if (isCurrent()) setGeometry(cols, rows)
      },
    }
    const existing = getShellSocket(runID, socketKey)
    let attachment: Attachment | null = existing ?? null
    if (!attachment) {
      attachment = connectAttach(() => api.attachShellSocket(runID, socketKey), handlers)
      registerShellSocket(runID, socketKey, attachment)
    } else {
      attachment.rebind(handlers)
    }

    clearAttached()
    const unsubscribe = subscribeShellSocket(runID, socketKey, gate.current.write)
    return () => {
      unsubscribe()
      clearAttached()
      cancelStructuralReplay()
      if (attachmentGenerationRef.current === attachmentGeneration) {
        attachmentGenerationRef.current++
        gate.current.cancel(true)
      }
    }
  }, [
    activeTab,
    geometry,
    setGeometry,
    canOpenShell,
    dock.refusedMessage,
    phone,
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
    const opened = openShellTab(runID)
    if (opened) focusTerminal()
  }

  const activeControlKey = activeTab ? `${runID}:${activeTab}` : ''
  const activeHasControl =
    controlState?.key === activeControlKey
      ? controlState.held
      : controlHeld.current[activeControlKey] === true
  const takeShellControl = () => {
    if (!activeTab) return
    writeRequested.current[activeControlKey] = true
    getShellSocket(runID, activeTab)?.reopen({ takeover: true })
  }

  return (
    <Dock
      tabs={tabs}
      activeTab={canOpenShell ? activeTab ?? '' : ''}
      onSelectTab={(tab) => {
        setDockCollapsed(runID, false)
        selectShellTab(runID, tab)
        focusTerminal()
      }}
      onAddTab={canOpenShell ? open : undefined}
      maxTabs={maxShellTabs}
      onCloseTab={(tab) => closeShellTab(runID, tab)}
      height={runDockHeight}
      onHeightChange={setRunDockHeight}
      collapsed={dock.collapsed}
      onToggleCollapse={() => {
        const expanding = dock.collapsed
        setDockCollapsed(runID, !dock.collapsed)
        if (expanding && canOpenShell) focusTerminal()
      }}
      containment="parent"
    >
      {showing === 'unavailable' ? (
        <div
          {...takesFocus}
          className={cn(focusRing, 'bg-background px-3 py-2 text-[13px] leading-5 text-muted-foreground')}
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
            'h-full min-h-0 min-w-0 break-words whitespace-pre-wrap overflow-y-auto bg-state-failed/10 px-3 py-2 text-[13px] leading-5 text-state-failed',
          )}
        >
          {dock.refusedMessage}
        </div>
      ) : showing === 'closed' ? (
        <div {...takesFocus} className={cn(focusRing, 'flex items-center bg-background px-3 py-2')}>
          <Button type="button" size="sm" onClick={open}>
            Open shell
          </Button>
        </div>
      ) : (
        <div className="flex h-full min-h-0 flex-1 flex-col">
          {!activeHasControl && (
            <div
              role="status"
              className="flex shrink-0 items-center justify-between gap-2 border-b border-border bg-toolbar px-3 py-1.5 text-[12px] text-muted-foreground"
            >
              <span>{shellControlMoved}</span>
              <Button type="button" size="sm" onClick={takeShellControl}>
                Take shell control
              </Button>
            </div>
          )}
          <TerminalPane
            controller={controller}
            writable={activeHasControl}
            replaying={replaying}
            className="min-h-0 flex-1 overflow-auto"
            imageTarget={runID}
            imageTargetKey={activeTab ?? undefined}
            imageUploadEnabled={
              activeHasControl &&
              attachedIdentity !== null &&
              attachedIdentity.runID === runID &&
              attachedIdentity.tab === activeTab &&
              activeTab !== null
            }
          />
        </div>
      )}
    </Dock>
  )
}
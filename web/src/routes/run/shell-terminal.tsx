import { useCallback, useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { Ellipsis } from '@/components/icons'
import { TerminalPane } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import type { DevController, DevControlFence, DevTerminalTarget } from '@/lib/types'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { type ConnectionState } from '@/lib/stream'
import { type RunShells, ShellCloseItems, controllerName, shellPollMs } from '@/routes/run/shells'
import {
  type AttachDataKind,
  type Attachment,
  type AttachTerminalIdentity,
  type ControlMetadata,
  connectAttach,
  replayGate,
  standardGeometry,
} from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'
import {
  emitShellSocketData,
  getShellSocket,
  registerShellSocket,
  subscribeShellSocket,
  unregisterShellSocket,
} from '@/store/terminal'

const shellRefusal = 'You can view this run but not open a shell in it'
// xterm reports focus changes and mouse events for an application that asked
// for them. Looking at a shell is not typing in it.
const terminalReport = /^\x1b\[(?:[IO]|<\d+;\d+;\d+[Mm])$/
const emptyReplay = new Uint8Array()

interface ShellAttachmentIdentity {
  runID: string
  tab: string
  incarnation: string
}

interface StructuralReplayState {
  attachmentGeneration: number
  controller: XtermController
  controllerGeneration: number
  revision: number
}

export function ShellTerminal({ shells, tabs, onCaptures }: {
  shells: RunShells
  tabs: React.ReactNode
  onCaptures: (returnTo: HTMLElement | null) => void
}) {
  const { runID, dock } = shells
  const mobile = useIsMobile()
  const self = useSelf()
  const setShellRefused = useStore((s) => s.setShellRefused)
  const members = useStore((s) => s.members)
  const activeTab = dock.activeTab
  const activeProcess = dock.terminals.find((item) => item.terminal_id === activeTab)
  const incarnation = activeProcess?.incarnation
  const processRunning = activeProcess?.process.state === 'running'
  const [error, setError] = useState<string | null>(null)
  const [captureMessage, setCaptureMessage] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [owner, setOwner] = useState<DevController | null>(null)
  const [confirmingTake, setConfirmingTake] = useState(false)
  const moreTrigger = useRef<HTMLButtonElement>(null)
  const takeTrigger = useRef<HTMLButtonElement>(null)
  const hidFromMenu = useRef(false)
  const takeoverGeneration = useRef(0)
  const refresh = shells.refresh
  const reportError = useCallback((cause: unknown) => {
    setError(cause instanceof Error ? cause.message : String(cause))
  }, [])
  // Who controls the active tab. Read from a ref by the interval below, so a
  // tab switch reads it once without re-listing shells or restarting the
  // interval; an answer for a target the dock has left is dropped.
  const ownerTarget = useRef<{ runID: string; tab: string; incarnation?: string }>({ runID: '', tab: '' })
  const readOwner = useCallback(async () => {
    const target = ownerTarget.current
    if (!target.tab || !target.incarnation) return
    const result = await api.devControlStatus({
      run_id: target.runID, surface: { kind: 'terminal', id: target.tab, incarnation: target.incarnation },
    })
    if (ownerTarget.current === target) setOwner(result.controller)
  }, [])
  useEffect(() => {
    ownerTarget.current = { runID, tab: activeTab ?? '', incarnation }
    setOwner(null)
    readOwner().catch(reportError)
    return () => { ownerTarget.current = { runID: '', tab: '' } }
  }, [runID, activeTab, incarnation, readOwner, reportError])
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') readOwner().catch(reportError)
    }, shellPollMs(shells.visible))
    return () => clearInterval(timer)
  }, [readOwner, reportError, shells.visible])
  const canOpenShell = shells.canOpen
  const [attachedIdentity, setAttachedIdentity] = useState<ShellAttachmentIdentity | null>(null)
  const [replaying, setReplaying] = useState(false)
  const currentAttachmentRef = useRef<ShellAttachmentIdentity | null>(null)
  const terminalRef = useRef<XtermController['terminal']>(null)
  const controllerRef = useRef<XtermController | null>(null)
  const attachmentGenerationRef = useRef(0)
  const replayRevisionRef = useRef(0)
  const structuralReplayRef = useRef<StructuralReplayState | null>(null)
  const fullReplaySettlingGenerationRef = useRef<number | null>(null)
  const writeRequested = shells.writeIntent
  const controlHeld = useRef<Record<string, boolean>>({})
  const sessions = shells.controlSessions
  const pendingInput = useRef('')
  const [controlState, setControlState] = useState<{ key: string; held: boolean } | null>(null)
  const activeControlKey = activeTab && incarnation ? `${runID}:${activeTab}:${incarnation}` : ''
  const activeHasControl = controlState?.key === activeControlKey && controlState.held
  const writes = useRef(Promise.resolve())
  const writeGeneration = useRef(0)
  const lastMouseButton = useRef('left')
  const currentFence = useCallback(() => {
    if (!activeTab || !incarnation || !controlHeld.current[activeControlKey]) return null
    const metadata = getShellSocket(runID, activeTab)?.controlMetadata?.()
    if (!metadata?.has_control) return null
    return {
      run_id: runID, terminal_id: activeTab, incarnation,
      control_session_id: metadata.control_session_id,
      control_generation: metadata.control_generation,
    }
  }, [activeTab, incarnation, activeControlKey, runID])
  const enqueueWrite = useCallback((operation: () => Promise<unknown>, fence: DevTerminalTarget & DevControlFence) => {
    const attachment = attachmentGenerationRef.current
    const generation = writeGeneration.current
    const key = `${fence.run_id}:${fence.terminal_id}:${fence.incarnation}`
    writes.current = writes.current.then(async () => {
      const current = currentAttachmentRef.current
      const metadata = getShellSocket(fence.run_id, fence.terminal_id)?.controlMetadata?.()
      if (writeGeneration.current !== generation || attachmentGenerationRef.current !== attachment || current?.runID !== fence.run_id ||
        current.tab !== fence.terminal_id || current.incarnation !== fence.incarnation ||
        !controlHeld.current[key] || !metadata?.has_control ||
        metadata.control_session_id !== fence.control_session_id || metadata.control_generation !== fence.control_generation) return
      await operation()
    }).catch((cause) => {
      if (writeGeneration.current !== generation) return
      writeGeneration.current++
      reportError(cause)
    })
  }, [reportError])
  const resizeTerminal = useCallback((cols: number, rows: number) => {
    const fence = currentFence()
    if (!fence) return
    enqueueWrite(() => api.devTerminalResize({ ...fence, cols, rows }), fence)
  }, [currentFence, enqueueWrite])
  const sendText = useCallback((data: string) => {
    const fence = currentFence()
    if (!fence) return
    // Bound UTF-8 payloads without splitting surrogate pairs. Queued input
    // remains tied to this attachment and its acknowledged control fence.
    for (let offset = 0; offset < data.length;) {
      let end = Math.min(offset + 2048, data.length)
      const last = data.charCodeAt(end - 1)
      if (end < data.length && last >= 0xd800 && last < 0xdc00) end--
      const text = data.slice(offset, end)
      enqueueWrite(() => api.devTerminalInput({ ...fence, kind: 'text', text }), fence)
      offset = end
    }
  }, [currentFence, enqueueWrite])
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
            // The parsed replay is still authoritative. A failed viewport
            // restore must not leave it permanently hidden.
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
  // A lease this page's own session still holds is reclaimed by attaching.
  // One a closed page of the same member left behind is theirs to take back.
  const ownSession = owner?.control_session_id === sessions.current[activeControlKey]
  const abandoned = owner !== null && !ownSession && !owner.connected && owner.kind === 'member' && owner.member_id === self.id
  const controlledByOther = owner !== null && !ownSession && !abandoned
  const typeToControl = shells.implicitControl && processRunning && !controlledByOther && dock.refusedMessage === null
  // A shell tab is one session shared by everyone on that tab, so a phone
  // follows it for the same reason it follows the agent's terminal.
  const phone = useMediaQuery(phoneScreen)
  const controller = useXterm({
    enabled:
      canOpenShell &&
      activeTab !== null &&
      processRunning &&
      dock.shellShown &&
      dock.refusedMessage === null,
    follow: phone || !activeHasControl,
    onData: (data) => {
      if (!activeTab) return
      const current = currentAttachmentRef.current
      const key = activeControlKey
      if (current?.runID !== runID || current.tab !== activeTab || current.incarnation !== incarnation) return
      if (controlHeld.current[key] !== true) {
        // Typing in a shell nobody else drives takes its lease. What is typed
        // until the server grants it waits here, and is sent under that lease.
        if (!typeToControl || terminalReport.test(data)) return
        pendingInput.current += data
        if (writeRequested.current[key]) return
        writeRequested.current[key] = true
        shells.focusShell.current = true
        takeoverGeneration.current = abandoned ? owner.control_generation : 0
        getShellSocket(runID, activeTab)?.reopen({ resume: true, takeover: abandoned })
        return
      }
      if (!gate.current.muted()) sendText(data)
    },
    onBinary: (data) => {
      if (gate.current.muted()) return
      const fence = currentFence()
      if (!fence) return
      // xterm's DEFAULT mouse encoder emits exactly CSI M plus three bytes.
      // Decode that documented input event so the server can re-encode it;
      // sending this binary string as UTF-8 text corrupts coordinates >= 95.
      if (data.length !== 6 || !data.startsWith('\x1b[M')) {
        setError('Unsupported binary terminal input')
        return
      }
      const code = data.charCodeAt(3) - 32
      const button = ['left', 'middle', 'right', 'none'][code & 3]
      const action = code & 64 ? 'wheel' : code & 32 ? 'move' : button === 'none' ? 'release' : 'press'
      if (action === 'press') lastMouseButton.current = button
      const modifiers = [
        ...(code & 4 ? ['shift'] : []), ...(code & 8 ? ['alt'] : []), ...(code & 16 ? ['ctrl'] : []),
      ]
      const mouseButton = action === 'release' ? lastMouseButton.current : button
      enqueueWrite(() => api.devTerminalInput({
        ...fence, kind: 'mouse', modifiers,
        mouse: {
          action, button: mouseButton,
          x: data.charCodeAt(4) - 33, y: data.charCodeAt(5) - 33,
          ...(action === 'wheel' ? { delta: code & 1 ? 1 : -1 } : {}),
        },
      }), fence)
    },
    onResize: (cols, rows) => {
      if (!activeTab) return
      const current = currentAttachmentRef.current
      if (current?.runID !== runID || current.tab !== activeTab) return
      resizeTerminal(cols, rows)
    },
  })
  controllerRef.current = controller
  const terminal = controller.terminal
  terminalRef.current = terminal
  const { geometry, setGeometry } = controller
  currentAttachmentRef.current =
    canOpenShell &&
    activeTab !== null &&
    incarnation !== undefined &&
    processRunning &&
    dock.shellShown &&
    dock.refusedMessage === null &&
    terminal
      ? { runID, tab: activeTab, incarnation }
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
    focusTerminal()
  }, [activeTab, setFindOpen, focusTerminal])

  useEffect(() => {
    if (!canOpenShell || !activeTab || !incarnation || !processRunning || !terminal || !dock.shellShown || dock.refusedMessage !== null) return

    const socketKey = activeTab
    const identity: ShellAttachmentIdentity = { runID, tab: socketKey, incarnation }
    const controlKey = `${runID}:${socketKey}:${incarnation}`
    const attachmentGeneration = ++attachmentGenerationRef.current
    let replayAccepted = false
    if (writeRequested.current[controlKey] === undefined) writeRequested.current[controlKey] = false
    const isCurrent = () => {
      const current = currentAttachmentRef.current
      return (
        attachmentGenerationRef.current === attachmentGeneration &&
        current?.runID === identity.runID &&
        current.tab === identity.tab
        && current.incarnation === identity.incarnation
      )
    }
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
      onAttached: (_write: boolean, size: { cols: number; rows: number }, resumed = false, acknowledged?: AttachTerminalIdentity) => {
        // Reattach replay restores the tab's full history, so a tab switch
        // may remount its xterm instead of preserving old instances. A
        // background tab reconnecting must never wipe the active tab or
        // unmute its replay.
        if (isCurrent()) {
          controllerRef.current?.setServerOwnedResponder?.(acknowledged?.server_owned_responder === true)
          setAttachedIdentity(identity)
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
        if (full) fullReplaySettlingGenerationRef.current = attachmentGeneration
        gate.current.start(full ? 'full' : 'delta')
        if (bytes === 0) void gate.current.write(emptyReplay, 'replay-end')
      },
      onState: (connection: ConnectionState) => {
        // Keys typed before a dropped connection must not arrive after it.
        if (connection === 'reconnecting' || connection === 'offline') pendingInput.current = ''
        if (isCurrent()) {
          if (connection !== 'live') setAttachedIdentity(null)
          if (connection !== 'live') {
            writeGeneration.current++
            controlHeld.current[controlKey] = false
            setControlState({ key: controlKey, held: false })
          }
        }
      },
      // The server's message names the actual limit (steer, tab cap,
      // paused run); a lost steer capability always means the fixed
      // refusal sentence.
      onRefused: refuse,
      onControl: (metadata: ControlMetadata) => {
        controlHeld.current[controlKey] = metadata.has_control
        sessions.current[controlKey] = metadata.control_session_id
        if (!isCurrent()) return
        setControlState({ key: controlKey, held: metadata.has_control })
        if (!metadata.has_control) return
        const typed = pendingInput.current
        pendingInput.current = ''
        sendText(typed)
      },
      onControlLost: () => {
        writeGeneration.current++
        writeRequested.current[controlKey] = false
        controlHeld.current[controlKey] = false
        pendingInput.current = ''
        if (!isCurrent()) return
        setControlState({ key: controlKey, held: false })
        readOwner().catch(reportError)
      },
      onControlResult: (result: { ok: boolean; error?: string }) => {
        if (!result.ok && result.error && isCurrent()) setError(result.error)
      },
      onWriteDenied: (message?: string) => refuse(message ?? shellRefusal),
      onExit: () => {
        clearAttached()
        controlHeld.current[controlKey] = false
        if (isCurrent()) setControlState({ key: controlKey, held: false })
        void refresh().catch(reportError)
      },
      geometry: () => isCurrent() ? geometry() : standardGeometry,
      wantsWrite: () => writeRequested.current[controlKey] === true,
      // Geometry is always an explicit fenced mutation, never an attach side effect.
      follows: () => true,
      screen: () => true,
      developmentTerminal: () => ({ terminal_id: socketKey, incarnation }),
      takeoverGeneration: () => takeoverGeneration.current,
      onGeometry: (cols: number, rows: number) => {
        if (isCurrent()) setGeometry(cols, rows)
      },
    }
    const existing = getShellSocket(runID, socketKey)
    let attachment: Attachment | null = existing ?? null
    if (!attachment) {
      attachment = connectAttach(() => api.attachShellSocket(runID, socketKey), handlers, sessions.current[controlKey])
      registerShellSocket(runID, socketKey, attachment)
    } else {
      attachment.rebind(handlers)
    }

    clearAttached()
    const unsubscribe = subscribeShellSocket(runID, socketKey, gate.current.write)
    return () => {
      writeGeneration.current++
      pendingInput.current = ''
      unsubscribe()
      // Hiding a viewer detaches its transport, never its server process.
      unregisterShellSocket(runID, socketKey)
      controlHeld.current[controlKey] = false
      clearAttached()
      cancelStructuralReplay()
      if (attachmentGenerationRef.current === attachmentGeneration) {
        attachmentGenerationRef.current++
        gate.current.cancel(true)
      }
    }
  }, [
    activeTab,
    incarnation,
    processRunning,
    dock.shellShown,
    geometry,
    setGeometry,
    canOpenShell,
    dock.refusedMessage,
    phone,
    readOwner,
    refresh,
    reportError,
    runID,
    sendText,
    sessions,
    setShellRefused,
    terminal,
    writeRequested,
  ])

  const takeShellControl = () => {
    if (!activeTab || !processRunning) return
    setError(null)
    takeoverGeneration.current = controlledByOther ? owner.control_generation : 0
    writeRequested.current[activeControlKey] = true
    getShellSocket(runID, activeTab)?.reopen({ takeover: controlledByOther, resume: true })
    setConfirmingTake(false)
  }
  const releaseShellControl = async () => {
    const fence = currentFence()
    if (!fence || !activeTab) return
    setBusy(true)
    setError(null)
    writeGeneration.current++
    writeRequested.current[activeControlKey] = false
    controlHeld.current[activeControlKey] = false
    setControlState({ key: activeControlKey, held: false })
    try {
      await api.devControlRelease({
        run_id: runID,
        surface: { kind: 'terminal', id: activeTab, incarnation: fence.incarnation },
        control_session_id: fence.control_session_id,
        control_generation: fence.control_generation,
      })
      setOwner(null)
      getShellSocket(runID, activeTab)?.reopen({ resume: true })
    } catch (cause) { reportError(cause) } finally { setBusy(false) }
  }
  // Control taken by typing would otherwise stay held behind another view,
  // where the agent cannot take it back from a person.
  useEffect(() => {
    if (!shells.visible && activeHasControl) void releaseShellControl()
  }, [shells.visible, activeHasControl])
  // A replay blurs the terminal, and the tab or button that asked for this
  // shell sits in a toolbar that is hidden by now.
  const attached = attachedIdentity !== null
  useEffect(() => {
    if (!shells.focusShell.current || !terminal || !attached || replaying) return
    shells.focusShell.current = false
    focusTerminal()
  }, [shells.focusShell, terminal, attached, replaying, focusTerminal])
  const screenshot = async () => {
    if (!activeTab || !incarnation) return
    setBusy(true)
    setError(null)
    try {
      const result = await api.devTerminalScreenshot({ run_id: runID, terminal_id: activeTab, incarnation })
      setCaptureMessage(`Captured ${result.artifact.id}. Open Captures to keep it.`)
    } catch (cause) { reportError(cause) } finally { setBusy(false) }
  }
  const controlledBy = controlledByOther ? controllerName(owner, members) : null
  const shellActions = (
    <>
      {processRunning && !activeHasControl && controlledBy && !mobile && (
        <span className="shrink-0 px-1 text-ui-sm text-muted">{controlledBy} controls</span>
      )}
      {processRunning && activeHasControl && <span className="px-1 text-ui-sm text-muted">You control</span>}
      {processRunning && (activeHasControl
        ? <Button size="sm" variant="ghost" disabled={busy} hint="Let the agent or a teammate type in this shell" onClick={() => void releaseShellControl()}>Release</Button>
        : !typeToControl && <Button ref={takeTrigger} size="sm" variant="secondary" disabled={busy || attachedIdentity === null} onClick={() => {
          if (controlledByOther) setConfirmingTake(true)
          else takeShellControl()
        }}>Take control</Button>)}
      <Menu>
        <MenuTrigger asChild>
          <Button ref={moreTrigger} size="icon-sm" variant="ghost" label="Shell actions"><Ellipsis /></Button>
        </MenuTrigger>
        <MenuContent align="end" onCloseAutoFocus={(event) => {
          if (!hidFromMenu.current) return
          hidFromMenu.current = false
          event.preventDefault()
          const next = useStore.getState().shellDocks[runID]
          const shown = next?.terminals.find((item) => item.terminal_id === next.activeTab)
          if (next?.shellShown && shown?.process.state === 'running') controllerRef.current?.focusTerminal()
          else moreTrigger.current?.focus()
        }}>
          <MenuItem disabled={busy || !incarnation} onSelect={() => void screenshot()}>Take a screenshot</MenuItem>
          {activeProcess && (
            <>
              <MenuSeparator />
              <ShellCloseItems shells={shells} terminal={activeProcess} onClose={() => { hidFromMenu.current = true }} />
            </>
          )}
        </MenuContent>
      </Menu>
    </>
  )
  const notice = dock.refusedMessage ?? error ?? captureMessage ??
    (activeProcess && !processRunning
      ? `This shell ${activeProcess.process.state}${activeProcess.process.exit_code == null ? '' : ` with code ${activeProcess.process.exit_code}`}. ${activeProcess.process.reason ?? ''} Nothing reruns it; close it or open a new shell.`
      : !shells.implicitControl && !activeHasControl && !controlledByOther
        ? 'Take control to type. This run is a swarm worker: taking control keeps the swarm from messaging its agent until you select Release control on the swarm page.'
        : processRunning && !activeHasControl && controlledBy && mobile
          ? `${controlledBy} controls this shell.`
          : null)

  return (
    <>
      <TerminalPane
        controller={controller}
        tabs={tabs}
        toolbarEnd={shellActions}
        onCaptures={onCaptures}
        notice={notice && (
          <p role={dock.refusedMessage || error ? 'alert' : 'status'} className="shrink-0 border-b border-seam px-3 py-1 text-ui-sm text-muted">
            {notice}
          </p>
        )}
        writable={processRunning && (activeHasControl || typeToControl)}
        replaying={processRunning && replaying}
        className="min-h-0 flex-1 overflow-auto"
        imageTarget={runID}
        imageTargetKey={activeControlKey}
        imageUploadEnabled={
          activeHasControl &&
          attachedIdentity !== null &&
          attachedIdentity.runID === runID &&
          attachedIdentity.tab === activeTab &&
          activeTab !== null
        }
      />
      <Dialog open={confirmingTake} onOpenChange={setConfirmingTake}>
        <DialogContent onCloseAutoFocus={(event) => {
          event.preventDefault()
          takeTrigger.current?.focus()
        }}>
          <DialogHeader>
            <DialogTitle>Take control of this shell?</DialogTitle>
            <DialogDescription>
              {controlledBy ?? 'Someone'} controls this shell. Taking control stops their typing here; other terminals are not affected.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setConfirmingTake(false)}>Cancel</Button>
            <Button variant="primary" disabled={busy} onClick={takeShellControl}>Take control</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

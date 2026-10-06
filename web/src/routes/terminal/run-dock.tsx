import { useCallback, useEffect, useRef, useState } from 'react'
import { Ellipsis } from 'lucide-react'
import { Dock } from '@/components/dock'
import { TerminalPane } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { DevController, DevControlFence, DevTerminalTarget } from '@/lib/types'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { EvidenceDrawer } from '@/routes/terminal/evidence-drawer'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { cn, focusRing } from '@/lib/utils'
import { type ConnectionState } from '@/lib/stream'
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
import {
  emitShellSocketData,
  getShellSocket,
  initialRunShellDock,
  registerShellSocket,
  subscribeShellSocket,
  unregisterShellSocket,
} from '@/store/terminal'

const maxShellTabs = 4
const statusPollMs = 10_000
const shellRefusal = 'You can view this run but not open a shell in it'
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

export function RunDock({ runID, onEvidenceAnswer, deferLayout = false }: {
  runID: string
  onEvidenceAnswer: (fact: string) => void
  deferLayout?: boolean
}) {
  const run = useStore((s) => s.runs[runID])
  const dock = useStore((s) => s.shellDocks[runID] ?? initialRunShellDock)
  const runDockHeight = useStore((s) => s.runDockHeight)
  const syncShellTerminals = useStore((s) => s.syncShellTerminals)
  const closeShellTab = useStore((s) => s.closeShellTab)
  const selectShellTab = useStore((s) => s.selectShellTab)
  const setDockCollapsed = useStore((s) => s.setDockCollapsed)
  const setRunDockHeight = useStore((s) => s.setRunDockHeight)
  const setShellRefused = useStore((s) => s.setShellRefused)

  const activeTab = dock.activeTab
  const activeProcess = dock.terminals.find((item) => item.terminal_id === activeTab)
  const incarnation = activeProcess?.incarnation
  const processRunning = activeProcess?.process.state === 'running'
  const [error, setError] = useState<string | null>(null)
  const [captureMessage, setCaptureMessage] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [owner, setOwner] = useState<DevController | null>(null)
  const [confirmation, setConfirmation] = useState<'take' | 'stop' | null>(null)
  const moreTrigger = useRef<HTMLButtonElement>(null)
  const confirmationTrigger = useRef<HTMLButtonElement | null>(null)
  const menuFocusTarget = useRef<'dialog' | 'terminal' | null>(null)
  const takeoverGeneration = useRef(0)
  const refreshRevision = useRef(0)
  const refresh = useCallback(async () => {
    const revision = ++refreshRevision.current
    const result = await api.devTerminalList({ run_id: runID })
    if (refreshRevision.current === revision) syncShellTerminals(runID, result.terminals)
  }, [runID, syncShellTerminals])
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
  // The shell list goes first: it can change the tab whose owner is read.
  useEffect(() => {
    let cancelled = false
    let timer: number | undefined
    const poll = async (owner: boolean) => {
      if (document.visibilityState === 'visible') {
        for (const read of owner ? [refresh, readOwner] : [refresh]) {
          try {
            await read()
          } catch (cause) { if (!cancelled) reportError(cause) }
        }
      }
      if (!cancelled) timer = window.setTimeout(() => void poll(true), statusPollMs)
    }
    void poll(false)
    return () => { cancelled = true; refreshRevision.current++; clearTimeout(timer) }
  }, [refresh, readOwner, reportError])
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
  const fullReplaySettlingGenerationRef = useRef<number | null>(null)
  const writeRequested = useRef<Record<string, boolean>>({})
  const controlHeld = useRef<Record<string, boolean>>({})
  const sessions = useRef<Record<string, string>>({})
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
  // Whatever replaces a terminal that may have held the keyboard takes it too:
  // disposing it leaves focus on <body>, where keystrokes reach the shell's shortcuts.
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
      processRunning &&
      !dock.collapsed &&
      dock.refusedMessage === null,
    follow: phone || !activeHasControl,
    onData: (data) => {
      if (!activeTab || gate.current.muted()) return
      const current = currentAttachmentRef.current
      const key = activeControlKey
      if (
        current?.runID !== runID ||
        current.tab !== activeTab ||
        controlHeld.current[key] !== true ||
        current.incarnation !== incarnation
      ) return
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
    !dock.collapsed &&
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
  }, [activeTab, setFindOpen])

  useEffect(() => {
    if (!canOpenShell || !activeTab || !incarnation || !processRunning || !terminal || dock.collapsed || dock.refusedMessage !== null) return

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
        if (isCurrent()) setControlState({ key: controlKey, held: metadata.has_control })
      },
      onControlLost: () => {
        writeGeneration.current++
        writeRequested.current[controlKey] = false
        controlHeld.current[controlKey] = false
        if (isCurrent()) setControlState({ key: controlKey, held: false })
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
    dock.collapsed,
    geometry,
    setGeometry,
    canOpenShell,
    dock.refusedMessage,
    phone,
    refresh,
    reportError,
    runID,
    setShellRefused,
    terminal,
  ])

  const tabs = canOpenShell ? dock.tabs.map((tab) => {
    const item = dock.terminals.find((item) => item.terminal_id === tab)!
    return { id: tab, label: `${item.name || tab} · ${item.process.state}` }
  }) : []
  // The header strip stays live while the dock is collapsed, so a tab control
  // must open the dock or it would add a tab with no terminal to attach.
  const open = async () => {
    setDockCollapsed(runID, false)
    setBusy(true)
    setError(null)
    try {
      const result = await api.devTerminalStart({ run_id: runID })
      // Invalidate an older in-flight list before publishing the created process.
      refreshRevision.current++
      const current = useStore.getState().shellDocks[runID]?.terminals ?? []
      syncShellTerminals(runID, [...current.filter((item) => item.terminal_id !== result.terminal.terminal_id), result.terminal])
      selectShellTab(runID, result.terminal.terminal_id)
      focusTerminal()
    } catch (cause) { reportError(cause) } finally { setBusy(false) }
  }
  const takeShellControl = () => {
    if (!activeTab || !processRunning) return
    setError(null)
    takeoverGeneration.current = owner?.control_generation ?? 0
    writeRequested.current[activeControlKey] = true
    getShellSocket(runID, activeTab)?.reopen({ takeover: owner !== null })
    setConfirmation(null)
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
      getShellSocket(runID, activeTab)?.reopen()
    } catch (cause) { reportError(cause) } finally { setBusy(false) }
  }
  const stopTerminal = async () => {
    const fence = currentFence()
    if (!fence) return
    setConfirmation(null)
    setBusy(true)
    setError(null)
    try {
      const result = await api.devTerminalStop({ ...fence, timeout_ms: 3000 })
      if (result.timed_out) setError('Terminal stop timed out; refresh the process state before another explicit stop.')
      await refresh()
    } catch (cause) { reportError(cause) } finally { setBusy(false) }
  }
  const screenshot = async () => {
    if (!activeTab || !incarnation) return
    setBusy(true)
    setError(null)
    try {
      const result = await api.devTerminalScreenshot({ run_id: runID, terminal_id: activeTab, incarnation })
      setCaptureMessage(`Captured ${result.artifact.id}. Open Evidence to select and retain it.`)
    } catch (cause) { reportError(cause) } finally { setBusy(false) }
  }
  const controllerName = owner?.kind === 'run_agent' ? `Run agent ${owner.run_id}` : owner?.member_id ?? 'Nobody'

  return (
    <>
    <Dock
      tabs={tabs}
      activeTab={canOpenShell ? activeTab ?? '' : ''}
      onSelectTab={(tab) => {
        setDockCollapsed(runID, false)
        selectShellTab(runID, tab)
        focusTerminal()
      }}
      onAddTab={canOpenShell && !busy && dock.terminals.filter((item) => item.process.state === 'running').length < maxShellTabs ? () => void open() : undefined}
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
      persistentActions={run && <EvidenceDrawer runID={runID} workspaceID={run.workspace_id} onAnswer={onEvidenceAnswer} deferLayout={deferLayout} />}
      actions={canOpenShell && dock.tabs.length >= maxShellTabs && dock.terminals.some((item) => item.process.state !== 'running') &&
        <Button size="sm" disabled={busy} onClick={() => void open()}>New terminal</Button>}
    >
      {error && <div role="alert" className="px-3 py-1 text-sm text-state-failed">{error}</div>}
      {captureMessage && <p role="status" className="px-3 py-1 text-xs">{captureMessage}</p>}
      {dock.hidden.length > 0 && (
        <div className="flex flex-wrap gap-1 px-3 py-1">
          {dock.terminals.filter((item) => !dock.tabs.includes(item.terminal_id)).map((item) => (
            <Button key={item.terminal_id} size="sm" variant="secondary" onClick={() => {
              selectShellTab(runID, item.terminal_id); setDockCollapsed(runID, false)
            }}>Show {item.name || item.terminal_id} · {item.process.state}</Button>
          ))}
        </div>
      )}
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
          <Button type="button" size="sm" disabled={busy || dock.terminals.filter((item) => item.process.state === 'running').length >= maxShellTabs} onClick={() => void open()}>
            Open shell
          </Button>
        </div>
      ) : (
        <div className="flex h-full min-h-0 flex-1 flex-col">
          <div className="flex min-w-0 shrink-0 items-center gap-2 border-b border-border bg-toolbar px-3 py-1.5 text-xs text-muted-foreground">
            <span role="status" className="min-w-0 flex-1 truncate" title={`${activeProcess?.name} · ${activeProcess?.process.state}${activeProcess?.process.exit_code !== undefined ? ` (${activeProcess.process.exit_code})` : ''} · Controller: ${activeHasControl ? 'You (this terminal)' : controllerName}`}>{activeProcess?.name} · {activeProcess?.process.state}{activeProcess?.process.exit_code !== undefined ? ` (${activeProcess.process.exit_code})` : ''} · Controller: {activeHasControl ? 'You (this terminal)' : controllerName}</span>
            {processRunning && (activeHasControl
              ? <Button size="sm" variant="secondary" className="shrink-0" disabled={busy} onClick={() => void releaseShellControl()}>Release shell control</Button>
              : <Button size="sm" variant="secondary" className="shrink-0" disabled={busy || attachedIdentity === null} onClick={(event) => {
                confirmationTrigger.current = event.currentTarget
                if (owner) setConfirmation('take')
                else takeShellControl()
              }}>Take shell control</Button>)}
            <Menu>
              <MenuTrigger asChild>
                <Button ref={moreTrigger} size="sm" variant="secondary" className="shrink-0" aria-label="More terminal actions"><Ellipsis className="size-3" aria-hidden />More</Button>
              </MenuTrigger>
              <MenuContent align="end" onCloseAutoFocus={(event) => {
                const target = menuFocusTarget.current
                menuFocusTarget.current = null
                if (!target) return
                event.preventDefault()
                if (target === 'terminal') {
                  if (placeholder.current) placeholder.current.focus()
                  else if (processRunning) controllerRef.current?.focusTerminal()
                  else moreTrigger.current?.focus()
                }
              }}>
                <MenuItem disabled={busy || !incarnation} onSelect={() => void screenshot()}>Screenshot</MenuItem>
                <MenuItem onSelect={() => {
                  menuFocusTarget.current = 'terminal'
                  if (activeTab) closeShellTab(runID, activeTab)
                }}>Hide terminal</MenuItem>
                <MenuSeparator />
                <MenuItem className="text-destructive" disabled={busy || !activeHasControl || !processRunning} onSelect={() => {
                  menuFocusTarget.current = 'dialog'
                  confirmationTrigger.current = moreTrigger.current
                  setConfirmation('stop')
                }}>Stop terminal</MenuItem>
              </MenuContent>
            </Menu>
          </div>
          {!processRunning && <p className="p-3 text-sm">This process {activeProcess?.process.state}. {activeProcess?.process.reason} Opening or showing it never reruns it. Use + to start a new terminal.</p>}
          <TerminalPane
            controller={controller}
            writable={activeHasControl && processRunning}
            replaying={replaying}
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
        </div>
      )}
    </Dock>
    <Dialog open={confirmation !== null} onOpenChange={(open) => { if (!open) setConfirmation(null) }}>
      <DialogContent onCloseAutoFocus={(event) => {
        event.preventDefault()
        confirmationTrigger.current?.focus()
      }}>
        <DialogHeader>
          <DialogTitle>{confirmation === 'take' ? 'Take shell control?' : 'Stop this terminal process?'}</DialogTitle>
          <DialogDescription>{confirmation === 'take'
            ? `${controllerName} controls this terminal. Taking over fences that writer, not other terminals or the primary harness. Shell control is scoped to this terminal. Releasing it does not clear durable mission holds.`
            : 'Stop ends this exact terminal incarnation. Hiding or detaching instead leaves it running.'}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="secondary" onClick={() => setConfirmation(null)}>Cancel</Button>
          <Button variant={confirmation === 'stop' ? 'danger' : 'primary'} disabled={busy} onClick={() => confirmation === 'take' ? takeShellControl() : void stopTerminal()}>{confirmation === 'take' ? 'Confirm takeover' : 'Confirm stop'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
    </>
  )
}
import { useEffect, useRef, useState } from 'react'
import { Dock } from '@/components/dock'
import { TerminalPane } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { cn, focusRing } from '@/lib/utils'
import { type ConnectionState } from '@/lib/stream'
import {
  type AttachDataKind,
  type Attachment,
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

interface ShellAttachmentIdentity {
  runID: string
  tab: string
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
  const currentAttachmentRef = useRef<ShellAttachmentIdentity | null>(null)
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
      if (!activeTab) return
      const current = currentAttachmentRef.current
      if (current?.runID !== runID || current.tab !== activeTab || gate.current.muted()) return
      getShellSocket(runID, activeTab)?.send(data)
    },
    onResize: (cols, rows) => {
      if (!activeTab) return
      const current = currentAttachmentRef.current
      if (current?.runID !== runID || current.tab !== activeTab) return
      getShellSocket(runID, activeTab)?.resize(cols, rows)
    },
  })
  const terminal = controller.terminal
  terminalRef.current = terminal
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
    const isCurrent = () => {
      const current = currentAttachmentRef.current
      return current?.runID === identity.runID && current.tab === identity.tab
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
      onData: (chunk: Uint8Array, kind: AttachDataKind) =>
        emitShellSocketData(runID, socketKey, chunk, kind),
      onAttached: () => {
        // Reattach replay restores the tab's full history, so a tab switch
        // may remount its xterm instead of preserving old instances. A
        // background tab reconnecting must never wipe the active tab or
        // unmute its replay.
        if (isCurrent()) {
          setAttachedIdentity(identity)
          gate.current.unmute()
          terminalRef.current?.reset()
          setShellRefused(runID, null)
        }
      },
      onState: (connection: ConnectionState) => {
        if (isCurrent()) {
          if (connection !== 'live') setAttachedIdentity(null)
          if (connection === 'offline') gate.current.unmute()
        }
      },
      // The server's message names the actual limit (steer, tab cap,
      // paused run); a lost steer capability always means the fixed
      // refusal sentence.
      onRefused: refuse,
      onWriteDenied: () => refuse(shellRefusal),
      onExit: () => {
        clearAttached()
        removeShellTab(runID, socketKey)
      },
      geometry: () => {
        if (!isCurrent()) return standardGeometry
        return {
          cols: terminalRef.current?.cols ?? standardGeometry.cols,
          rows: terminalRef.current?.rows ?? standardGeometry.rows,
        }
      },
      wantsWrite: () => true,
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
    if (existing) attachment.reopen()
    return () => {
      unsubscribe()
      clearAttached()
    }
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
    const opened = openShellTab(runID)
    if (opened) focusTerminal()
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
        <TerminalPane
          controller={controller}
          imageTarget={runID}
          imageTargetKey={activeTab ?? undefined}
          imageUploadEnabled={
            attachedIdentity !== null &&
            attachedIdentity.runID === runID &&
            attachedIdentity.tab === activeTab &&
            activeTab !== null
          }
        />
      )}
    </Dock>
  )
}

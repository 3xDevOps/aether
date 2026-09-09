import { useEffect, useRef } from 'react'
import { toast } from 'sonner'
import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { TerminalPane, TerminalSpinner } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { openOAuthLink } from '@/lib/oauth-forward'
import type { RunStatus } from '@/lib/types'
import { cn } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { type Attachment, connectAttach, replayGate } from '@/routes/terminal/attach'
import { RunDock } from '@/routes/terminal/run-dock'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { initialTerminal } from '@/store/terminal'

const connectionLabel: Record<string, string> = {
  connecting: 'Connecting',
  live: 'Attached',
  reconnecting: 'Reconnecting',
  offline: 'Offline',
}

/** Statuses that can still gain a live terminal session. Mirrors
 * `replayableStatus` in internal/sshd/attach.go, which serves a transcript
 * instead only once `domain.RunStatus.Terminal()` is true. */
const liveStatuses: readonly RunStatus[] = [
  'queued',
  'provisioning',
  'running',
  'needs-attention',
]

/** Statuses whose container is still being built, so no PTY session exists
 * to attach to yet. */
const startingStatuses: readonly RunStatus[] = ['queued', 'provisioning']

function TerminalView({ params }: RouteProps) {
  const runID = params.runId
  const run = useStore((s) => s.runs[runID])
  const state = useStore((s) => s.terminals[runID] ?? initialTerminal)
  const setTerminal = useStore((s) => s.setTerminal)
  const capability = useCapability()
  const self = useSelf()
  const controlTaken = useStore((s) => s.terminalControlTaken)
  const markControlTaken = useStore((s) => s.markTerminalControlTaken)

  const known = run !== undefined
  const starting = run !== undefined && startingStatuses.includes(run.status)
  const attachRef = useRef<Attachment | null>(null)
  const gate = useRef(replayGate((chunk, done) => terminalRef.current?.write(chunk, done)))
  const terminalRef = useRef<XtermController['terminal']>(null)
  // Read at connect time by the attachment, so a toggle takes effect on the
  // reattach without re-running the terminal's own effect.
  const writeRef = useRef(state.write)
  writeRef.current = state.write
  // An owner's run attaches with write already asked for, which the server
  // grants without the member touching anything. Only a request they made
  // counts as taking control.
  const askedForControl = useRef(false)
  const controller = useXterm({
    enabled: known,
    onData: (data) => {
      if (!gate.current.muted()) attachRef.current?.send(data)
    },
    onResize: (cols, rows) => attachRef.current?.resize(cols, rows),
    onLink: (uri) => {
      if (!capability.hasLocal('forward.start')) return false
      return openOAuthLink(
        api,
        `run:${runID}`,
        uri,
        (port) => toast.success(`OAuth callback ready on localhost:${port}`),
        (err) => toast.error(`OAuth callback forward failed: ${message(err)}`),
      )
    },
  })
  const terminal = controller.terminal
  terminalRef.current = terminal
  useEffect(() => {
    if (!terminal) return

    // A stalled run still has a live, recoverable agent session. Completed
    // and final-run attaches replay history read-only.
    const ownerSteering =
      (run?.status === 'running' || run?.status === 'needs-attention') &&
      run?.member_id === self.id
    setTerminal(runID, { ...initialTerminal, write: ownerSteering })
    writeRef.current = ownerSteering
    askedForControl.current = false

    // No session exists yet to attach to (internal/ptyhost ErrNoSession),
    // and the pane is cleared here because no ack will arrive to clear it.
    if (starting) {
      gate.current.unmute()
      terminal.reset()
      return
    }

    const attachment = connectAttach(() => api.attachSocket(runID), {
      onData: gate.current.write,
      // Every attach starts with the server's transcript replay, so the pane
      // is never blank - and clearing first keeps a reconnect from stacking a
      // second copy of the scrollback under the first.
      onAttached: (write) => {
        // The ack carries what the server granted, not what was asked: a
        // refused request arrives as onWriteDenied instead. Marking control
        // here rather than on the click keeps a denied first attempt from
        // silencing the mirror hint for good.
        if (write && askedForControl.current) markControlTaken()
        gate.current.unmute()
        terminal.reset()
        setTerminal(runID, { message: null, refused: false })
      },
      onState: (connection) => {
        if (connection === 'offline') gate.current.unmute()
        setTerminal(runID, { connection })
      },
      onRefused: (message) => setTerminal(runID, { message, refused: true }),
      onWriteDenied: () => setTerminal(runID, { steerDenied: true, write: false }),
      geometry: () => ({ cols: terminal.cols, rows: terminal.rows }),
      wantsWrite: () => writeRef.current,
    })
    attachRef.current = attachment

    return () => {
      attachment.close()
      attachRef.current = null
    }
  }, [markControlTaken, run?.member_id, run?.status, runID, self.id, setTerminal, starting, terminal])

  if (!run) {
    return <MissingRun />
  }

  const toggleWrite = () => {
    writeRef.current = !state.write
    if (writeRef.current) askedForControl.current = true
    setTerminal(runID, { write: writeRef.current })
    attachRef.current?.reopen()
  }

  const retry = () => {
    setTerminal(runID, { message: null, refused: false })
    attachRef.current?.reopen()
  }

  return (
    <div className="flex h-full flex-col">
      <RunHeader run={run} subtitle={`${run.harness} · ${run.branch}`} />
      <RunTabs runID={runID} active="terminal" />
      <div className="flex items-center gap-3 border-b px-4 py-1.5 text-xs">
        {!starting && (
          <span
            className={cn(
              'text-muted-foreground',
              state.connection === 'offline' && 'text-state-failed',
            )}
          >
            {connectionLabel[state.connection]}
          </span>
        )}
        <Button
          size="sm"
          variant={state.write ? 'default' : 'outline'}
          disabled={
            state.steerDenied ||
            (run.status !== 'running' && run.status !== 'needs-attention')
          }
          className="relative"
          onClick={toggleWrite}
        >
          {state.write ? 'Steering' : 'Take control'}
          {state.write && (
            <span aria-hidden className="steering-signal">
              <span />
            </span>
          )}
        </Button>
        {state.steerDenied && (
          <span className="text-muted-foreground">
            You cannot steer this run.
          </span>
        )}
        {/* A disabled control shows no tooltip, so the reason is written out
            beside it rather than hidden in a title attribute. */}
        {run.status !== 'running' && !starting && !state.steerDenied && (
          <span className="text-muted-foreground">This run is not running</span>
        )}
        {/* Nothing else on screen separates watching from steering, so the
            attach says what it is until the member has taken control once. */}
        {!controlTaken && !state.write && !state.steerDenied && run.status === 'running' && (
          <span className="text-muted-foreground">
            Read-only mirror. Take control to type into the agent.
          </span>
        )}
        {state.message && (
          <span className="truncate text-muted-foreground">
            {state.refused && !liveStatuses.includes(run.status)
              ? 'This run has ended and left no recorded terminal to replay.'
              : state.message}
          </span>
        )}
        {state.refused && (
          <Button size="sm" variant="ghost" onClick={retry}>
            Retry
          </Button>
        )}
      </div>
      <div className="min-h-0 flex-1">
        <TerminalPane key={runID} controller={controller}>
          {starting && <TerminalSpinner label="Starting the run's container" />}
        </TerminalPane>
      </div>
      <RunDock runID={runID} />
    </div>
  )
}

registerRoute('terminal', TerminalView)

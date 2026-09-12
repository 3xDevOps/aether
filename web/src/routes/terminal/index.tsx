import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { TerminalPane, TerminalSpinner } from '@/components/terminal-pane'
import { type XtermController, useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { openOAuthLink, remoteOAuthInstructions } from '@/lib/oauth-forward'
import type { RunStatus } from '@/lib/types'
import { cn } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import {
  type Attachment,
  codeUnavailable,
  connectAttach,
  replayGate,
} from '@/routes/terminal/attach'
import { RunDock } from '@/routes/terminal/run-dock'
import { runTabPanel } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { initialTerminal } from '@/store/terminal'

const connectionLabel: Record<string, string> = {
  connecting: 'Connecting',
  live: 'Attached',
  reconnecting: 'Reconnecting',
  offline: 'Offline',
}

/**
 * Statuses a run never leaves. Mirrors `replayableStatus` in
 * internal/sshd/attach.go, which is `domain.RunStatus.Terminal()`: past these
 * a run is permanently sessionless, so its refusal is the answer rather than
 * a race worth waiting out, and no retry could ever help. `needs-attention`
 * is not one of them - it is supervised and goes back to running.
 */
const endedStatuses: readonly RunStatus[] = [
  'completed',
  'merged',
  'abandoned',
  'failed',
  'interrupted',
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
  // A stalled run still has a live agent session, so it steers like a
  // running one. The disabled control and the reason beside it read this
  // one answer rather than each testing the status themselves.
  const steerable = run?.status === 'running' || run?.status === 'needs-attention'
  // Only a missing session says anything about the run itself; every other
  // refusal is about this attach and speaks for itself.
  const [sessionMissing, setSessionMissing] = useState(false)
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
  // A phone follows the session instead of sizing it: it renders at the
  // geometry the ack reports and imposes none of its own, so an agent's
  // screen is neither garbled here nor reflowed to 45 columns for everyone
  // else the moment this phone takes control.
  const phone = useMediaQuery(phoneScreen)
  const controller = useXterm({
    enabled: known,
    follow: phone,
    onData: (data) => {
      // A mirror's input frames are ignored by the server; not sending them
      // is what makes the read-only state true on this side as well.
      if (writeRef.current && !gate.current.muted()) attachRef.current?.send(data)
    },
    onResize: (cols, rows) => attachRef.current?.resize(cols, rows),
    onLink: (uri) => {
      if (!capability.hasLocal('forward.start')) {
        const remote = remoteOAuthInstructions(`run:${runID}`, uri)
        if (remote === null) return false
        toast.info(`Run ${remote.command}, then open this link on that machine.`, {
          description: uri,
          action: { label: 'Copy link', onClick: () => void copyText(uri, null) },
        })
        return true
      }
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
  const { geometry, setGeometry } = controller
  useEffect(() => {
    if (!terminal || !known) return

    // A stalled run still has a live, recoverable agent session. Completed
    // and final-run attaches replay history read-only. A phone never steers
    // on its own: typing into an agent from a phone is a choice, and the
    // mirror is what a member opening their own run there wants.
    const ownerSteering =
      !phone &&
      (run?.status === 'running' || run?.status === 'needs-attention') &&
      run?.member_id === self.id
    setTerminal(runID, { ...initialTerminal, write: ownerSteering })
    writeRef.current = ownerSteering
    askedForControl.current = false
    setSessionMissing(false)

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
      onAttached: (write, size, resumed) => {
        // The ack carries what the server granted, not what was asked: a
        // refused request arrives as onWriteDenied instead. Marking control
        // here rather than on the click keeps a denied first attempt from
        // silencing the mirror hint for good.
        if (write && askedForControl.current) markControlTaken()
        gate.current.unmute()
        // A resumed attach brings no replay, so the screen on display is
        // the only copy of it - and the terminal state behind that screen
        // is what makes a paste arrive as a paste.
        setGeometry(size.cols, size.rows, !resumed)
        setTerminal(runID, { message: null, refused: false })
      },
      onState: (connection) => {
        if (connection === 'offline') gate.current.unmute()
        setTerminal(runID, { connection })
      },
      onRefused: (message, code) => {
        setSessionMissing(code === codeUnavailable)
        setTerminal(runID, { message, refused: true })
      },
      onWriteDenied: () => setTerminal(runID, { steerDenied: true, write: false }),
      onGeometry: setGeometry,
      sessionPending: () => run !== undefined && !endedStatuses.includes(run.status),
      geometry,
      wantsWrite: () => writeRef.current,
      follows: () => phone,
    })
    attachRef.current = attachment

    return () => {
      attachment.close()
      attachRef.current = null
    }
  }, [
    known,
    geometry,
    setGeometry,
    markControlTaken,
    phone,
    run?.member_id,
    run?.status,
    runID,
    self.id,
    setTerminal,
    starting,
    terminal,
  ])

  // A read-only mirror must not raise a soft keyboard that types into
  // nothing, and xterm focuses its textarea on any tap. `disableStdin` makes
  // that textarea read-only, which is what keeps the keyboard down.
  useEffect(() => {
    if (!terminal) return
    terminal.options.disableStdin = !state.write
    if (!state.write) terminal.blur()
  }, [state.write, terminal])

  if (!run) {
    return <MissingRun />
  }

  const toggleWrite = () => {
    writeRef.current = !state.write
    if (writeRef.current) askedForControl.current = true
    setTerminal(runID, { write: writeRef.current })
    // Taking control changes what this attach may do, not what it shows,
    // so it keeps the screen rather than redrawing the whole scrollback.
    attachRef.current?.reopen({ resume: true })
  }

  const retry = () => {
    setTerminal(runID, { message: null, refused: false })
    attachRef.current?.reopen()
  }

  const attachmentControls = (
    <div
      role="group"
      aria-label="Terminal attachment controls"
      className="flex min-w-0 max-w-full flex-wrap items-center gap-x-2 gap-y-1 py-1 text-[13px]"
    >
      {!starting && (
        <span
          className={cn(
            'shrink-0 rounded-[2px] border border-border bg-background px-2 py-0.5 text-[12px] font-medium text-muted-foreground',
            state.connection === 'offline' &&
              'border-state-failed/40 bg-state-failed/10 text-[var(--danger-soft-foreground)]',
          )}
        >
          {connectionLabel[state.connection]}
        </span>
      )}
      <Button
        size="sm"
        variant={state.write ? 'default' : 'outline'}
        disabled={state.steerDenied || !steerable}
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
        <span className="min-w-0 flex-[1_1_16rem] break-words text-muted-foreground">
          You cannot steer this run.
        </span>
      )}
      {/* A disabled control shows no tooltip, so the reason is written out
          beside it rather than hidden in a title attribute. */}
      {!steerable && !starting && !state.steerDenied && (
        <span className="min-w-0 flex-[1_1_16rem] break-words text-muted-foreground">
          This run is not running
        </span>
      )}
      {/* Nothing else on screen separates watching from steering, so the
          attach says what it is until the member has taken control once. */}
      {!controlTaken && !state.write && !state.steerDenied && run.status === 'running' && (
        <span className="min-w-0 flex-[1_1_16rem] break-words text-muted-foreground">
          Read-only mirror. Take control to type into the agent.
        </span>
      )}
      {state.message && (
        <span className="min-w-0 flex-[1_1_16rem] break-words whitespace-pre-wrap text-muted-foreground">
          {state.refused && sessionMissing && endedStatuses.includes(run.status)
            ? 'This run has ended and left no recorded terminal to replay.'
            : state.message}
        </span>
      )}
      {state.refused && !endedStatuses.includes(run.status) && (
        <Button size="sm" variant="ghost" className="shrink-0" onClick={retry}>
          Retry
        </Button>
      )}
    </div>
  )

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden">
      <RunHeader run={run} subtitle={run.branch} active="terminal" />
      <div {...runTabPanel('terminal', 'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden')}>
        <div className="relative min-h-0 flex flex-1 flex-col overflow-x-hidden overflow-y-auto">
          <div className="relative min-h-24 flex-1 overflow-hidden bg-background">
            <TerminalPane
              key={runID}
              controller={controller}
              className="overflow-auto"
              writable={state.write && !starting}
              imageTarget={runID}
              toolbarEnd={attachmentControls}
              imageTargetKey={runID}
              imageUploadEnabled={
                !starting && state.connection === 'live' && state.write && !state.steerDenied
              }
            >
              {starting && <TerminalSpinner label="Starting the run's container" />}
            </TerminalPane>
          </div>
          <RunDock runID={runID} />
        </div>
      </div>
    </div>
  )
}

registerRoute('terminal', TerminalView)

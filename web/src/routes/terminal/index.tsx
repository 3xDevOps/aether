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
  type ControlMetadata,
  codeUnavailable,
  connectAttach,
  replayGate,
} from '@/routes/terminal/attach'
import { RunDock } from '@/routes/terminal/run-dock'
import { RunRoom } from '@/routes/terminal/run-room'
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
function terminalBufferWeight(terminal: XtermController['terminal']): number {
  if (!terminal) return 0
  return (terminal.buffer.normal.length + terminal.buffer.alternate.length) * terminal.cols
}

function TerminalView(props: RouteProps) {
  return <TerminalRoute key={props.params.runId} {...props} />
}

function TerminalRoute({
  params,
  active = true,
  onTerminalWeight,
  onTerminalInvalidate,
}: RouteProps) {
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
  // An inactive cache entry does not create a terminal or socket on its
  // first render. Once it has been active, the xterm stays enabled while
  // parked so its parsed screen and measured host remain available.
  const [initialized, setInitialized] = useState(() => active && known)
  useEffect(() => {
    if (active && known) setInitialized(true)
  }, [active, known])

  // A stalled run still has a live agent session, so it steers like a
  // running one. The disabled control and the reason beside it read this
  // one answer rather than each testing the status themselves.
  const steerable = run?.status === 'running' || run?.status === 'needs-attention'
  // Only a missing session says anything about the run itself; every other
  // refusal is about this attach and speaks for itself.
  const [sessionMissing, setSessionMissing] = useState(false)
  const [controlMetadata, setControlMetadata] = useState<ControlMetadata | undefined>()
  const [replaying, setReplaying] = useState(false)
  const runStatusRef = useRef(run?.status)
  runStatusRef.current = run?.status
  // A final run can be relaunched in place. Keep this separate from the
  // attachment's ended bit so a parked view records the transition without
  // opening transport in the background.
  const previousStatusRef = useRef(run?.status)
  const relaunchPendingRef = useRef(false)
  const lifecycleActive = useRef(active)
  const attachRef = useRef<Attachment | null>(null)
  const attachmentAtRenderRef = useRef<Attachment | null>(null)
  attachmentAtRenderRef.current = attachRef.current
  const phone = useMediaQuery(phoneScreen)
  const phoneRef = useRef(phone)
  phoneRef.current = phone
  const weightRef = useRef(onTerminalWeight)
  weightRef.current = onTerminalWeight
  const invalidateRef = useRef(onTerminalInvalidate)
  invalidateRef.current = onTerminalInvalidate
  const automaticWrite =
    !phone &&
    steerable &&
    run?.member_id === self.id
  // A null choice means that the member has not made a control decision for
  // this attachment, so ownership and device changes may update the answer.
  // Once they take or release control, that decision wins over automation.
  const explicitWriteRef = useRef<boolean | null>(null)
  const writeSyncRef = useRef({ automaticWrite, phone })
  const terminalRef = useRef<XtermController['terminal']>(null)
  const gate = useRef(
    replayGate((chunk, done) => {
      const current = terminalRef.current
      if (!current) {
        done?.()
        return
      }
      current.write(chunk, () => {
        done?.()
        // Parking intentionally closes the socket but retains xterm. A write
        // that was already queued can settle after that transition, so report
        // the post-parse weight rather than leaving CenterView's budget stale.
        if (!lifecycleActive.current) {
          weightRef.current?.(terminalBufferWeight(current))
        }
      })
    }, setReplaying),
  )
  // Read at connect time by the attachment, so a toggle takes effect on the
  // reattach without re-running the terminal's own effect.
  const writeRef = useRef(automaticWrite)
  writeRef.current = explicitWriteRef.current ?? automaticWrite
  // An owner's run attaches with write already asked for, which the server
  // grants without the member touching anything. Only a request they made
  // counts as taking control.
  const askedForControl = useRef(false)
  // A phone follows the session instead of sizing it: it renders at the
  // geometry the ack reports and imposes none of its own, so an agent's
  // screen is neither garbled here nor reflowed to 45 columns for everyone
  // else the moment this phone takes control.
  const controller = useXterm({
    enabled: known && initialized,
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
  // Close is irreversible and belongs to this run/xterm lifetime. This
  // effect deliberately does not depend on active: parking suspends the
  // logical attachment rather than disposing it.
  useEffect(() => {
    return () => {
      const attachment = attachRef.current
      if (!attachment) return
      attachment.close()
      attachRef.current = null
    }
  }, [known, initialized, terminal])

  // Setup has no cleanup of its own. That lets active transitions call
  // suspend/resume without tearing down the attachment; the lifetime effect
  // above handles actual unmount, run replacement, and xterm replacement.
  useEffect(() => {
    if (!active || !terminal || !known || !initialized || attachRef.current) return

    // A stalled run still has a live, recoverable agent session. Completed
    // and final-run attaches replay history read-only. A phone never steers
    // on its own: typing into an agent from a phone is a choice, and the
    // mirror is what a member opening their own run there wants.
    explicitWriteRef.current = null
    setTerminal(runID, { ...initialTerminal, write: automaticWrite })
    writeRef.current = automaticWrite
    askedForControl.current = false
    setControlMetadata(undefined)
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
      // is never blank - and clearing first keeps a reconnect from stacking
      // a second copy of the scrollback under the first.
      onAttached: (write, size, resumed) => {
        // The ack carries what the server granted, not what was asked: a
        // refused request arrives as onWriteDenied instead.
        if (write && askedForControl.current) markControlTaken()
        // A resumed attach brings no replay, so the screen on display is the
        // only copy of it.
        setGeometry(size.cols, size.rows, !resumed)
        setTerminal(runID, { message: null, refused: false })
      },
      onReplayAbort: () => gate.current.cancel(),
      onReplayStart: (bytes) => {
        if (bytes > 0) gate.current.start()
        else gate.current.unmute()
      },
      onControl: setControlMetadata,
      onState: (connection) => {
        setTerminal(runID, { connection })
      },
      onRefused: (message, code) => {
        // A final refusal makes the cached transcript untrustworthy. Reset it
        // before notifying CenterView so an invalidated entry cannot flash
        // stale output while it is removed.
        terminalRef.current?.reset()
        invalidateRef.current?.()
        setSessionMissing(code === codeUnavailable)
        setTerminal(runID, { message, refused: true })
      },
      onWriteDenied: () => {
        explicitWriteRef.current = false
        writeRef.current = false
        askedForControl.current = false
        setTerminal(runID, { steerDenied: true, write: false })
      },
      onControlLost: () => {
        // Control is an ephemeral lease, not a permission decision. Drop the
        // writable preference and any pending request so the reconnect is a
        // mirror, while leaving the Take control action available.
        explicitWriteRef.current = false
        writeRef.current = false
        askedForControl.current = false
        setTerminal(runID, { steerDenied: false, write: false })
        setControlMetadata(undefined)
      },
      onGeometry: setGeometry,
      sessionPending: () => runStatusRef.current !== undefined && !endedStatuses.includes(runStatusRef.current),
      geometry,
      wantsWrite: () => writeRef.current,
      follows: () => phoneRef.current,
    })
    attachRef.current = attachment
    // A never-before-mounted active route starts with a fresh full attach
    // already, so consume a relaunch marker without opening a second socket.
    relaunchPendingRef.current = false
  }, [
    active,
    automaticWrite,
    geometry,
    initialized,
    known,
    markControlTaken,
    run?.member_id,
    self.id,
    setGeometry,
    setTerminal,
    starting,
    steerable,
    terminal,
  ])
  useEffect(() => {
    const previous = previousStatusRef.current
    const current = run?.status
    previousStatusRef.current = current
    if (
      previous !== undefined &&
      current !== undefined &&
      endedStatuses.includes(previous) &&
      steerable
    ) {
      // `run.relaunch` keeps the same run ID and cached xterm. The old
      // attachment is ended, so its next connection must be a full replay.
      relaunchPendingRef.current = true
    }
  }, [run?.status, steerable])
  useEffect(() => {
    const previous = writeSyncRef.current
    const automaticChanged = previous.automaticWrite !== automaticWrite
    const followChanged = previous.phone !== phone
    writeSyncRef.current = { automaticWrite, phone }

    if (automaticChanged && explicitWriteRef.current === null) {
      writeRef.current = automaticWrite
      if (state.write !== automaticWrite) {
        setTerminal(runID, { write: automaticWrite })
      }
    }

    // Follow is a wire-level attach option, not just a renderer preference.
    // Replace an active socket even when the write answer did not change (or
    // was explicitly chosen by the member). Combining both transitions here
    // makes a breakpoint crossing one cutover rather than two. A relaunch is
    // stronger than either and consumes the marker so the same render cannot
    // request a second socket.
    if (
      attachmentAtRenderRef.current &&
      active &&
      lifecycleActive.current &&
      (followChanged || (automaticChanged && explicitWriteRef.current === null))
    ) {
      const relaunch = relaunchPendingRef.current
      relaunchPendingRef.current = false
      attachRef.current?.reopen(relaunch ? undefined : { resume: true })
    }
  }, [active, automaticWrite, phone, runID, setTerminal, state.write])
  // A cached view keeps only this primary terminal mounted. Its attachment
  // is suspended while parked, and the parsed terminal reports both buffers
  // to CenterView's bounded cache.
  useEffect(() => {
    const wasActive = lifecycleActive.current
    if (!active && (wasActive || terminal)) {
      if (wasActive) attachRef.current?.suspend()
    } else if (active && !wasActive) {
      const attachment = attachRef.current
      if (relaunchPendingRef.current && attachment) {
        relaunchPendingRef.current = false
        attachment.reopen()
      } else {
        attachment?.resume()
      }
    }
    lifecycleActive.current = active
  }, [active, terminal])
  useEffect(() => {
    if (!active || !lifecycleActive.current || !relaunchPendingRef.current) return
    const attachment = attachRef.current
    if (!attachment) return
    relaunchPendingRef.current = false
    attachment.reopen()
  }, [active, run?.status, terminal])
  useEffect(() => {
    if (active || !terminal) return
    weightRef.current?.(terminalBufferWeight(terminal))
  }, [active, replaying, terminal])
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
  const takeControl = (takeover = false) => {
    explicitWriteRef.current = true
    writeRef.current = true
    askedForControl.current = true
    setTerminal(runID, { write: true, steerDenied: false })
    // The server owns the lease. `takeover` is only sent after the room's
    // confirmation dialog has named the current controller.
    attachRef.current?.reopen({ resume: true, takeover })
  }

  const releaseControl = () => {
    explicitWriteRef.current = false
    writeRef.current = false
    askedForControl.current = false
    setTerminal(runID, { write: false })
    attachRef.current?.reopen({ resume: true, releaseControl: true })
  }

  const toggleWrite = () => {
    if (state.write) {
      releaseControl()
    } else {
      takeControl()
    }
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
  // Cached inactive views retain only the xterm surface. RunHeader owns the
  // fixed run-tab/panel IDs and its action toolbar, so mounting it in every
  // cached entry would create duplicate accessibility targets.
  const panelProps = active
    ? runTabPanel('terminal', 'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden')
    : { className: 'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden' }

  return (
    <div className="relative flex h-full min-w-0 flex-col overflow-hidden pr-8">
      {active && <RunHeader run={run} subtitle={run.branch} active="terminal" />}
      <div {...panelProps}>
        <div className="relative min-h-0 flex flex-1 flex-col overflow-x-hidden overflow-y-auto">
          <div className="relative min-h-24 flex-1 overflow-hidden bg-background">
            <TerminalPane
              key={runID}
              controller={controller}
              className="overflow-auto"
              writable={state.write && !starting}
              imageTarget={runID}
              toolbarEnd={attachmentControls}
              imageUploadEnabled={
                active && !starting && state.connection === 'live' && state.write && !state.steerDenied
              }
              replaying={replaying}
            >
              {starting && <TerminalSpinner label="Starting the run's container" />}
            </TerminalPane>
          </div>
          {active && <RunDock runID={runID} />}
        </div>
      </div>
      {active && (
        <RunRoom
          key={runID}
          run={run}
          selfID={self.id}
          control={controlMetadata}
          onTakeControl={takeControl}
          onReleaseControl={releaseControl}
        />
      )}
    </div>
  )
}

registerRoute('terminal', TerminalView)

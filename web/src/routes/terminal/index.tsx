import { useRef } from 'react'
import { toast } from 'sonner'
import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { TerminalPane, TerminalSpinner } from '@/components/terminal-pane'
import { useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { openOAuthLink, remoteOAuthInstructions } from '@/lib/oauth-forward'
import type { RunStatus } from '@/lib/types'
import { cn } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { RunDock } from '@/routes/terminal/run-dock'
import { RunRoom } from '@/routes/terminal/run-room'
import { TerminalHistory } from '@/routes/terminal/history'
import { runTabPanel } from '@/routes/terminal/tabs'
import { useRunTerminalSession } from '@/routes/terminal/session'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'

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

function TerminalView(props: RouteProps) {
  return <TerminalRoute key={props.params.runId} {...props} />
}

function TerminalRoute({ params }: RouteProps) {
  const runID = params.runId
  const run = useStore((s) => s.runs[runID])
  const workspaceID = run?.workspace_id
  const steerOthers = useStore((s) =>
    workspaceID ? s.workspaces[workspaceID]?.steer_others : undefined,
  )
  const capability = useCapability()
  const self = useSelf()
  const identityKey = useStore((s) => s.identityKey)
  const terminalCacheEpoch = useStore((s) => s.terminalCacheEpoch)
  const controlTaken = useStore((s) => s.terminalControlTaken)
  const phone = useMediaQuery(phoneScreen)
  const known = run !== undefined
  const starting = run !== undefined && startingStatuses.includes(run.status)

  const steerable = run?.status === 'running' || run?.status === 'needs-attention'
  const automaticWrite =
    !phone &&
    steerable &&
    run?.member_id === self.id
  const authorityKey = [
    self.id,
    self.role,
    run?.member_id ?? '',
    run?.protected ? 'protected' : 'open',
    workspaceID ?? '',
    steerOthers === undefined ? '' : String(steerOthers),
  ].join('\u0000')

  // The session is created after xterm, so input and geometry use refs rather
  // than closing over a different hook result on every render.
  const sendRef = useRef<(data: string) => void>(() => {})
  const resizeRef = useRef<(cols: number, rows: number) => void>(() => {})
  const controller = useXterm({
    enabled: known,
    follow: phone,
    scrollback: 5000,
    onData: (data) => sendRef.current(data),
    onResize: (cols, rows) => resizeRef.current(cols, rows),
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
  const session = useRunTerminalSession({
    runID,
    run,
    terminal: controller.terminal,
    geometry: controller.geometry,
    setGeometry: controller.setGeometry,
    phone,
    identityKey,
    terminalCacheEpoch,
    automaticWrite,
    authorityKey,
    beginStructuralReplay: controller.beginStructuralReplay,
    cancelStructuralReplay: controller.cancelStructuralReplay,
    finishStructuralReplay: controller.finishStructuralReplay,
  })
  sendRef.current = session.send
  resizeRef.current = session.resize

  const { state, replaying, controlMetadata, sessionMissing } = session
  const replaySpinner = replaying
  const takeControl = (takeover = false) => session.takeControl(takeover)
  const releaseControl = () => session.releaseControl()
  const toggleWrite = () => {
    if (state.write) releaseControl()
    else takeControl()
  }

  if (!run) {
    return <MissingRun />
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
      <TerminalHistory runID={runID} />
      {state.steerDenied && (
        <span className="min-w-0 flex-[1_1_16rem] break-words text-muted-foreground">
          You cannot steer this run.
        </span>
      )}
      {!steerable && !starting && !state.steerDenied && (
        <span className="min-w-0 flex-[1_1_16rem] break-words text-muted-foreground">
          This run is not running
        </span>
      )}
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
        <Button size="sm" variant="ghost" className="shrink-0" onClick={session.retry}>
          Retry
        </Button>
      )}
    </div>
  )

  const panelProps = runTabPanel(
    'terminal',
    'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden',
  )

  return (
    <div className="relative flex h-full min-w-0 flex-col overflow-hidden pr-8">
      <RunHeader run={run} subtitle={run.branch} active="terminal" />
      <div {...panelProps}>
        <div className="relative min-h-0 flex flex-1 flex-col overflow-hidden">
          <div className="relative min-h-24 flex-1 overflow-hidden bg-background">
            <TerminalPane
              key={runID}
              controller={controller}
              writable={state.write && !starting && !replaying}
              imageTarget={runID}
              toolbarEnd={attachmentControls}
              imageUploadEnabled={
                !starting && state.connection === 'live' && state.write && !state.steerDenied
              }
              replaying={replaySpinner}
            >
              {starting && <TerminalSpinner label="Starting the run's container" />}
            </TerminalPane>
          </div>
          <RunDock runID={runID} />
        </div>
      </div>
      <RunRoom
        key={runID}
        run={run}
        selfID={self.id}
        control={controlMetadata}
        onTakeControl={takeControl}
        onReleaseControl={releaseControl}
      />
    </div>
  )
}

registerRoute('terminal', TerminalView)

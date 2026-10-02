import { useMemo, useRef, useState } from 'react'
import type { Terminal } from '@xterm/xterm'
import { toast } from 'sonner'
import { Eye, KeyRound } from 'lucide-react'
import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { TerminalPane, TerminalSpinner, type TerminalReadSurface } from '@/components/terminal-pane'
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
import { MemberAvatar } from '@/routes/board/member-avatar'
import { RunDock } from '@/routes/terminal/run-dock'
import { RunRoom } from '@/routes/terminal/run-room'
import { TerminalHistory } from '@/routes/terminal/history'
import { getHistoryCache } from '@/routes/terminal/history-cache'
import { runTabPanel } from '@/routes/terminal/tabs'
import { useRunTerminalSession } from '@/routes/terminal/session'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'

const connectionLabel: Record<string, string> = {
  connecting: 'Connecting',
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
  const identityKey = useStore((state) => state.identityKey)
  const epoch = useStore((state) => state.terminalCacheEpoch)
  const createdAt = useStore((state) => state.runs[props.params.runId]?.created_at)
  return <TerminalRoute key={JSON.stringify([identityKey, epoch, props.params.runId, createdAt])} {...props} />
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
  const members = useStore((s) => s.members)
  const roomStatus = useStore((s) => s.roomStatus[runID])
  const roomStatusControl = useStore((s) => s.roomStatusControl[runID])
  const roomStatusError = useStore((s) => s.roomStatusError[runID])
  const phone = useMediaQuery(phoneScreen)
  const known = run !== undefined
  const starting = run !== undefined && startingStatuses.includes(run.status)
  const historyCache = useMemo(() => getHistoryCache({
    identityKey,
    epoch: terminalCacheEpoch,
    runID,
    createdAt: run?.created_at ?? '',
  }), [identityKey, terminalCacheEpoch, runID, run?.created_at])
  const beforeDispose = useRef<((terminal: Terminal) => void) | null>(null)
  const historyTools = useRef<TerminalReadSurface | null>(null)
  const [readingHistory, setReadingHistory] = useState(true)

  const steerable = run?.status === 'running' || run?.status === 'needs-attention'
  const automaticWrite =
    !phone &&
    steerable &&
    run?.mission_role !== 'worker' &&
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
    onBeforeDispose: (terminal) => beforeDispose.current?.(terminal),
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
  const roomControl = state.connection === 'live' && steerable && !state.steerDenied ? controlMetadata : undefined
  const stalePresence = roomStatusControl !== roomControl || Boolean(roomStatusError)
  const localControl = state.connection === 'live' && state.write &&
    controlMetadata?.has_control === true && steerable && !state.steerDenied
  const controllerID = localControl ? self.id : roomStatus?.controller?.member_id
  const controllerMember = controllerID ? members[controllerID] : undefined
  const controllerName = controllerMember?.display_name || controllerID
  const controllerNote = localControl ? 'this tab' : stalePresence ? 'last known' :
    state.connection === 'live' && controlMetadata?.has_control === false && controllerID === self.id ? 'another session' : null
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
      className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 text-[13px]"
    >
      <div className="flex h-[35px] min-w-0 w-full items-center gap-1 coarse:h-[47px] @sm/terminal-pane:gap-2">
        <div role="group" aria-label="Run presence" className="flex h-full min-w-0 flex-1 items-center gap-2 text-[12px]">
          <div
            className="flex min-w-5 max-w-[50%] items-center gap-1 whitespace-nowrap"
            title={controllerID ? `Controller: ${controllerName}${controllerNote ? ` (${controllerNote})` : ''}` : undefined}
          >
            <KeyRound aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground @[56rem]/terminal-pane:hidden" />
            <span className="sr-only text-muted-foreground @[56rem]/terminal-pane:not-sr-only">Controller</span>
            {controllerID ? (
              <span className="inline-flex min-w-0 items-center gap-1">
                <MemberAvatar member={controllerMember} fallback={controllerID} className="size-4 text-[8px]" />
                <span className="min-w-0 truncate font-medium text-foreground">{controllerName}</span>
                {controllerNote && <span className={cn('sr-only shrink-0 @[56rem]/terminal-pane:not-sr-only', localControl ? 'text-[var(--accent-soft-foreground)]' : 'text-muted-foreground')}>({controllerNote})</span>}
              </span>
            ) : (
              <span className="truncate text-muted-foreground">{roomStatus ? stalePresence ? 'Unknown' : 'Nobody' : roomStatusError ? 'Unavailable' : 'Loading…'}</span>
            )}
          </div>
          <div
            role="group"
            aria-label="Run viewers"
            tabIndex={0}
            className="flex h-full min-w-0 flex-1 touch-pan-x items-center gap-2 overflow-x-auto whitespace-nowrap border-l border-border pl-2 coarse:min-w-11"
          >
            <Eye aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground @[56rem]/terminal-pane:hidden" />
            <span className="sr-only shrink-0 text-muted-foreground @[56rem]/terminal-pane:not-sr-only">Viewers</span>
            {!starting && state.connection !== 'live' && (
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
            {roomStatus ? roomStatus.watchers.length > 0 ? roomStatus.watchers.map((id) => (
              <span key={id} className="inline-flex shrink-0 items-center gap-1">
                <MemberAvatar member={members[id]} fallback={id} className="size-4 text-[8px]" />
                <span className="text-foreground">{members[id]?.display_name || id}</span>
              </span>
            )) : <span className="text-muted-foreground">None</span> : (
              <span className="text-muted-foreground">{roomStatusError ? 'Unavailable' : 'Loading…'}</span>
            )}
            {roomStatusError && roomStatus && <span className="shrink-0 text-muted-foreground">Last known presence</span>}
          </div>
        </div>
        <Button
          size="sm"
          variant={state.write ? 'default' : 'outline'}
          disabled={state.steerDenied || !steerable}
          className="shrink-0 px-2 coarse:h-11 coarse:min-h-11 coarse:px-2"
          onClick={toggleWrite}
        >
          {state.write ? 'Release' : 'Take control'}
        </Button>
      </div>
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
      {state.message && (
        <span className="min-w-0 flex-[1_1_16rem] break-words whitespace-pre-wrap text-muted-foreground">
          {state.refused && sessionMissing && endedStatuses.includes(run.status)
            ? 'This run has ended and left no recorded terminal to replay.'
            : state.message}
        </span>
      )}
      {state.refused && !endedStatuses.includes(run.status) && (
        <Button size="sm" variant="ghost" className="shrink-0 coarse:h-11 coarse:min-h-11" onClick={session.retry}>
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
    <div className="relative flex h-full min-w-0 flex-col overflow-hidden pr-8 coarse:pr-11">
      <RunHeader run={run} subtitle={run.branch} active="terminal" />
      <div {...panelProps}>
        <div className="relative min-h-0 flex flex-1 flex-col overflow-hidden">
          <div className="relative min-h-24 flex-1 overflow-hidden bg-background">
            <TerminalPane
              key={runID}
              controller={controller}
              writable={state.write && !starting && !replaying}
              readingSurface={readingHistory ? historyTools : undefined}
              surface={
                <TerminalHistory
                  controller={controller}
                  cache={historyCache}
                  enabled={!starting && !replaying}
                  beforeDispose={beforeDispose}
                  tools={historyTools}
                  onReadingChange={setReadingHistory}
                />
              }
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
        control={roomControl}
        onTakeControl={takeControl}
        onReleaseControl={releaseControl}
      />
    </div>
  )
}

registerRoute('terminal', TerminalView)

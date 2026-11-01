import { ControlMatrix } from '@/components/control-matrix'
import { Shield, Users } from '@/components/icons'
import { connectionLabel } from '@/components/shell/connection'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { clockTime } from '@/lib/format'
import { memberName, resolveMember } from '@/lib/member-name'
import { allowed } from '@/lib/permissions'
import type { RoomController } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { ControlButton } from '@/routes/terminal/control-button'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

function heldElsewhere(controller: RoomController | undefined, selfID: string | null, agent: AgentTerminal): boolean {
  return !agent.localControl && controller?.member_id === selfID && controller.control_session_id !== agent.controlSessionID
}

export function usePresenceSummary(run: RunRecord, agent: AgentTerminal): string {
  const self = useSelf()
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const status = useStore((s) => s.roomStatus[run.id])
  const statusControl = useStore((s) => s.roomStatusControl[run.id])
  const statusError = useStore((s) => s.roomStatusError[run.id])
  const stream = useStore((s) => s.acpSessions[run.id]?.stream)
  if (agent.localControl) return 'You control'
  if (!status) return statusError ? 'Control unknown' : 'Checking control…'
  const holder = status.controller
  const who = !holder ? 'Nobody controls'
    : holder.member_id !== self.id ? `${memberName({ members, mergedInto }, holder.member_id)} controls`
      : heldElsewhere(holder, self.id, agent) ? `You control in another tab since ${clockTime(holder.acquired_at)}`
        : 'You control'
  const connection = run.acp ? stream : agent.session.state.connection
  const stale = connection !== 'live' || statusControl !== agent.roomControl || Boolean(statusError)
  return stale ? `${who} (last known)` : who
}

export function MultiplayerControls({ run, agent, onPeople }: {
  run: RunRecord
  agent: AgentTerminal
  onPeople: () => void
}) {
  const self = useSelf()
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const summary = usePresenceSummary(run, agent)
  const status = useStore((s) => s.roomStatus[run.id])
  const presenceError = useStore((s) => s.roomStatusError[run.id])
  const session = useStore((s) => s.acpSessions[run.id])
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const stream = session?.stream ?? 'connecting'
  const controlError = session?.controlError ?? session?.takeoverError
  const connection = stream === 'refused' ? 'Offline' : connectionLabel[stream]
  const controllerID = agent.localControl ? self.id : status?.controller?.member_id
  const controller = controllerID ? resolveMember({ members, mergedInto }, controllerID).member : undefined
  const controllerName = controllerID ? memberName({ members, mergedInto }, controllerID) : undefined
  const controllerSummary = controllerID === self.id && controllerName ? `${controllerName} · ${summary}` : summary
  const staleWatchers = stream !== 'live' || Boolean(presenceError)

  return (
    <div role="group" aria-label="Multiplayer controls" className="@container/multiplayer relative isolate shrink-0 overflow-clip border-b border-seam bg-chrome px-2 py-1">
      <ControlMatrix
        appearance={agent.localControl ? 'active'
          : agent.controlUnavailable ? 'hidden' : agent.roomControl?.loss ?? 'hidden'}
        takeoverProgress={agent.takeover.holderProgress}
      />
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <Button size="sm" variant="ghost" className="control-matrix-clear shrink-0" hint="See people and notes for this session" onClick={onPeople}>
          <Users />
          <span className="@max-[30rem]/multiplayer:sr-only">Multiplayer</span>
        </Button>
        <div className="flex min-w-24 flex-1">
          <div className="control-matrix-clear flex min-w-0 items-center gap-2 px-1 text-ui-sm">
            <span className="shrink-0 text-muted">Watching{status && staleWatchers && ' (last known)'}</span>
            <div role="group" aria-label="Session watchers" tabIndex={status?.watchers.length ? 0 : undefined} className="flex min-w-0 flex-1 touch-pan-x items-center gap-3 overflow-x-auto whitespace-nowrap">
              {status ? status.watchers.length > 0 ? status.watchers.map((id) => {
                const member = resolveMember({ members, mergedInto }, id).member
                const name = memberName({ members, mergedInto }, id)
                return (
                  <span key={id} className="inline-flex shrink-0 items-center gap-1.5">
                    <Avatar name={name} color={member?.color} />
                    <span className="@max-[34rem]/multiplayer:sr-only">{name}</span>
                  </span>
                )
              }) : <span className="text-muted">Nobody</span> : <span className="text-muted">{presenceError ? 'Unavailable' : 'Checking…'}</span>}
            </div>
          </div>
        </div>
        <div className="control-matrix-clear ml-auto flex min-w-0 items-center justify-end gap-2">
          {run.protected && <span className="inline-flex shrink-0 items-center gap-1 text-ui-sm text-muted"><Shield />Protected</span>}
          <div role="group" aria-label="Session controller" className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
            {agent.steerable && controllerID && <Avatar name={controllerName ?? controllerID} color={controller?.color} />}
            <span className="min-w-0 max-w-48 truncate @[48rem]/multiplayer:max-w-none">{agent.steerable ? controllerSummary : agent.starting ? 'Starting…' : 'Session finished · read-only'}</span>
          </div>
          {agent.steerable && stream !== 'live' && <span className="shrink-0 text-ui-sm text-state-needs-you">{connection}</span>}
          {agent.steerable && maySteer && (
            <ControlButton
              ownsControl={agent.localControl}
              unavailable={agent.controlUnavailable || Boolean(run.switching)}
              onTakeControl={agent.session.takeControl}
              onReleaseControl={agent.session.releaseControl}
              takeover={agent.takeover.interaction}
            />
          )}
          {agent.steerable && !maySteer && <span className="shrink-0 text-ui-sm text-muted">View only</span>}
        </div>
      </div>
      {controlError && <p role="alert" className="control-matrix-clear w-fit px-1 py-1 text-ui-sm text-state-failed">Control request failed: {controlError}</p>}
      {session?.streamError && <p role="status" className="control-matrix-clear w-fit px-1 py-1 text-ui-sm text-state-failed">{session.streamError}</p>}
      {presenceError && <p role="status" className="control-matrix-clear w-fit px-1 py-1 text-ui-sm text-muted">Presence unavailable: {presenceError}</p>}
    </div>
  )
}

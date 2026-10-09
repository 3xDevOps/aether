import { Shield, Users } from '@/components/icons'
import { connectionLabel } from '@/components/shell/connection'
import { Button } from '@/components/ui/button'
import { allowed } from '@/lib/permissions'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { ControlButton } from '@/routes/terminal/control-button'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

export function usePresenceSummary(run: RunRecord, agent: AgentTerminal): string {
  const self = useSelf()
  const members = useStore((s) => s.members)
  const status = useStore((s) => s.roomStatus[run.id])
  const statusControl = useStore((s) => s.roomStatusControl[run.id])
  const statusError = useStore((s) => s.roomStatusError[run.id])
  const stream = useStore((s) => s.acpSessions[run.id]?.stream)
  if (agent.localControl) return 'You control'
  if (!status) return statusError ? 'Control unknown' : 'Checking control…'
  const controllerID = status.controller?.member_id
  const who = !controllerID ? 'Nobody controls'
    : controllerID === self.id ? 'You control in another tab'
      : `${members[controllerID]?.display_name ?? controllerID} controls`
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
  const summary = usePresenceSummary(run, agent)
  const status = useStore((s) => s.roomStatus[run.id])
  const presenceError = useStore((s) => s.roomStatusError[run.id])
  const session = useStore((s) => s.acpSessions[run.id])
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const stream = session?.stream ?? 'connecting'
  const controlError = session?.controlError ?? session?.takeoverError
  const connection = stream === 'refused' ? 'Offline' : connectionLabel[stream]

  return (
    <div role="group" aria-label="Multiplayer controls" className="shrink-0 border-b border-seam bg-chrome px-2 py-1">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <Button size="sm" variant="ghost" hint="See people and notes for this run" onClick={onPeople}>
          <Users />
          Multiplayer
        </Button>
        {status && !presenceError && <span className="text-ui-sm text-muted">{status.watchers.length} watching</span>}
        {run.protected && <span className="inline-flex items-center gap-1 text-ui-sm text-muted"><Shield />Protected</span>}
        <span className="ml-auto min-w-0 break-words text-ui-sm text-muted">
          {agent.steerable ? summary : agent.starting ? 'Starting…' : 'Run finished · read-only'}
        </span>
        {agent.steerable && stream !== 'live' && <span className="text-ui-sm text-state-needs-you">{connection}</span>}
        {agent.steerable && maySteer && (
          <ControlButton
            ownsControl={agent.localControl}
            unavailable={agent.controlUnavailable || Boolean(run.switching)}
            onTakeControl={agent.session.takeControl}
            onReleaseControl={agent.session.releaseControl}
            takeover={agent.takeover.interaction}
          />
        )}
        {agent.steerable && !maySteer && <span className="text-ui-sm text-muted">View only</span>}
      </div>
      {controlError && <p role="alert" className="px-1 py-1 text-ui-sm text-state-failed">Control request failed: {controlError}</p>}
      {session?.streamError && <p role="status" className="px-1 py-1 text-ui-sm text-state-failed">{session.streamError}</p>}
      {presenceError && <p role="status" className="px-1 py-1 text-ui-sm text-muted">Presence unavailable: {presenceError}</p>}
    </div>
  )
}

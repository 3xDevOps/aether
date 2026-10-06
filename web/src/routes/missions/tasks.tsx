import { useState } from 'react'
import { toast } from 'sonner'
import { modeLabel } from '@/components/launch/modes'
import { swarmHandle } from '@/components/messages/handles'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { SectionLabel } from '@/components/ui/section-label'
import { StateLine, type Tone } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { plainReason } from '@/lib/status'
import type { AgentInfo, MissionAttempt, MissionTask, MissionTaskScope } from '@/lib/types'
import { useStore } from '@/store'
import type { MissionDetail } from '@/store/missions'

const statusWord: Record<MissionTask['status'], string> = {
  proposed: 'Proposed',
  ready: 'Ready',
  working: 'Working',
  review: 'Review',
  done: 'Done',
  abandoned: 'Abandoned',
  blocked: 'Blocked',
}

const statusTone: Record<MissionTask['status'], Tone> = {
  proposed: 'neutral',
  ready: 'neutral',
  working: 'working',
  review: 'working',
  done: 'done',
  abandoned: 'neutral',
  blocked: 'failed',
}

const columns = 'md:grid md:grid-cols-[minmax(0,1fr)_7rem_6rem_13rem] md:items-center md:gap-3'

function scopeFacts(scope: MissionTaskScope): string[] {
  return [
    ...(scope.expected_paths ?? []).map((path) => `Expected ${path}`),
    ...(scope.exclusions ?? []).map((path) => `Excluded ${path}`),
    ...(scope.semantic_responsibility ? [scope.semantic_responsibility] : []),
    ...(scope.target ? [`Target ${scope.target}`] : []),
  ]
}

function TaskDetails({ task, detail, showProposalBlocker }: { task: MissionTask; detail: MissionDetail; showProposalBlocker: boolean }) {
  const navigate = useStore((s) => s.navigate)
  const revision = task.revision
  const facts = revision ? scopeFacts(revision.scope) : []
  const blockers = (task.blockers ?? []).filter((blocker) => showProposalBlocker || blocker.kind !== 'proposal')
  const diagnostics = detail.diagnostics.filter((item) => item.task_id === task.id)
  const errors = detail.attempts.filter((attempt) => attempt.task_id === task.id && attempt.last_error)
  return (
    <div className="flex flex-col gap-2 pt-1 pb-3 pl-5 text-ui">
      {revision && <p className="whitespace-pre-wrap">{revision.objective}</p>}
      {task.pending_revision && (
        <p className="text-muted">Revision pending: {task.pending_revision.title}</p>
      )}
      {facts.length > 0 && <p className="text-ui-sm text-muted">{facts.join(' · ')}</p>}
      {blockers.map((blocker, index) => (
        <p key={`${blocker.kind}-${index}`} className="text-ui-sm text-muted">Waiting on {blocker.kind}: {blocker.action}</p>
      ))}
      {diagnostics.map((diagnostic, index) => (
        <p key={`${diagnostic.kind}-${index}`} className="text-ui-sm text-state-needs-you">
          {diagnostic.kind.replaceAll('_', ' ')}:{' '}
          {diagnostic.unavailable
            ? plainReason(diagnostic.unavailable_why || diagnostic.detail || 'snapshot or evidence is unavailable')
            : diagnostic.paths.join(', ') || diagnostic.detail || 'No paths reported'}
          {diagnostic.peer_run_id && (
            <>
              {' '}
              <Button variant="link" size="sm" onClick={() => navigate('run', { runId: diagnostic.peer_run_id! })}>Open the other run</Button>
            </>
          )}
        </p>
      ))}
      {errors.map((attempt) => (
        <p key={attempt.id} className="text-ui-sm text-state-failed">{plainReason(attempt.last_error ?? '')}</p>
      ))}
    </div>
  )
}

function ControlHold({ attempt, canRelease, client, onReleased }: {
  attempt: MissionAttempt
  canRelease: boolean
  client: Api
  onReleased: () => void
}) {
  const holder = useStore((s) => (attempt.takeover_member_id ? s.members[attempt.takeover_member_id]?.display_name : undefined))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const release = async () => {
    if (attempt.takeover_generation == null || busy) return
    setBusy(true)
    setError(null)
    try {
      await client.missionWorkerRelease({ run_id: attempt.run_id, expected_takeover_generation: attempt.takeover_generation })
      toast.success('Human control released')
      onReleased()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Callout
      tone="needs-you"
      title={`${holder ?? 'A human'} holds control of this worker`}
      actions={canRelease && attempt.takeover_active && (
        <Button size="sm" variant="secondary" disabled={busy} onClick={() => void release()}>
          {busy ? 'Releasing…' : 'Release control'}
        </Button>
      )}
    >
      The integrator cannot steer it until control is released, here or from the worker run.
      {error && <p className="text-state-failed">{error}</p>}
    </Callout>
  )
}

function TaskRow({ task, detail, agents, showProposalBlocker, canRelease, client, onChanged }: {
  task: MissionTask
  detail: MissionDetail
  agents: AgentInfo[] | null
  showProposalBlocker: boolean
  canRelease: boolean
  client: Api
  onChanged: () => void
}) {
  const navigate = useStore((s) => s.navigate)
  const attempt = detail.attempts.filter((item) => item.task_id === task.id).sort((a, b) => b.number - a.number)[0]
  const agent = attempt && (agents?.find((item) => item.name === attempt.harness)?.display_name || attempt.harness)
  const held = attempt && (attempt.takeover_active || attempt.orchestration_hold)
  const title = task.revision?.title || task.pending_revision?.title || 'Untitled task'
  return (
    <li className="border-b border-seam last:border-b-0">
      <Collapsible>
        <div className={`flex min-h-7 flex-col gap-0.5 py-1 coarse:min-h-11 ${columns}`}>
          <CollapsibleTrigger>
            <span className="min-w-0 truncate">{title}</span>
          </CollapsibleTrigger>
          <div className="flex min-w-0 items-center gap-3 pl-5 md:contents">
            <StateLine tone={statusTone[task.status]}>{statusWord[task.status]}</StateLine>
            <span className="text-ui-sm text-muted">{attempt ? modeLabel(attempt.mode) : '-'}</span>
            {attempt?.run_id ? (
              <Button
                variant="link"
                size="sm"
                className="min-w-0 justify-start"
                onClick={() => navigate('run', { runId: attempt.run_id })}
              >
                <AgentGlyph agent={attempt.harness} />
                <span className="truncate">{swarmHandle(detail, attempt.run_id)?.name ?? 'Worker'} · {agent}</span>
              </Button>
            ) : (
              <span className="text-ui-sm text-muted">No worker yet</span>
            )}
          </div>
        </div>
        {held && (
          <div className="pb-2 pl-5">
            <ControlHold attempt={attempt} canRelease={canRelease} client={client} onReleased={onChanged} />
          </div>
        )}
        <CollapsibleContent>
          <TaskDetails task={task} detail={detail} showProposalBlocker={showProposalBlocker} />
        </CollapsibleContent>
      </Collapsible>
    </li>
  )
}

export function SwarmTasks({ detail, agents, canRelease, client, onChanged }: {
  detail: MissionDetail
  agents: AgentInfo[] | null
  canRelease: boolean
  client: Api
  onChanged: () => void
}) {
  const { mission, tasks } = detail
  return (
    <section aria-label="Tasks" className="flex flex-col gap-1">
      <SectionLabel as="h2">Tasks</SectionLabel>
      {tasks.length === 0 ? (
        <p className="text-ui text-muted">
          {mission.phase === 'planning' ? 'The integrator has not proposed tasks yet.' : 'This swarm has no tasks.'}
        </p>
      ) : (
        <>
          <div aria-hidden className={`hidden border-b border-seam pb-1 text-ui-sm text-muted ${columns}`}>
            <span className="pl-5">Task</span>
            <span>Status</span>
            <span>Mode</span>
            <span>Worker</span>
          </div>
          <ul className="flex flex-col">
            {tasks.map((task) => (
              <TaskRow
                key={task.id}
                task={task}
                detail={detail}
                agents={agents}
                showProposalBlocker={mission.phase === 'active'}
                canRelease={canRelease}
                client={client}
                onChanged={onChanged}
              />
            ))}
          </ul>
        </>
      )}
    </section>
  )
}

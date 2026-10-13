import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { agentLabel, modeLabel } from '@/components/launch/modes'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { SectionLabel } from '@/components/ui/section-label'
import { StateLine, StatusDot, type Tone } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { plainReason } from '@/lib/status'
import type { AgentInfo, MissionAttempt, MissionAttemptState, MissionTask, MissionTaskScope } from '@/lib/types'
import { cn } from '@/lib/utils'
import { elapsed, missionTree, taskTitle, type AttemptNode, type TaskNode } from '@/routes/missions/tree'
import { useStore } from '@/store'
import { useRun, useRunPresentation } from '@/store/hooks'
import type { MissionDetail } from '@/store/missions'
import type { RunRecord } from '@/store/runs'

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

const attemptWord: Record<MissionAttemptState, string> = {
  reserved: 'Reserved',
  launching: 'Launching',
  running: 'Running',
  unknown: 'State unknown',
  submitted: 'Submitted',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
  superseded: 'Superseded',
  abandoned: 'Abandoned',
}

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

interface RunLineProps {
  runID: string
  label: string
  lead?: string
  note?: string
  from?: string | null
  to?: string | null
}

function RunRow({ runID, label, tone, pulse = false, detail, shimmer = false, from, to }: {
  runID: string
  label: string
  tone: Tone
  pulse?: boolean
  detail: string
  shimmer?: boolean
  from?: string | null
  to?: string | null
}) {
  const navigate = useStore((s) => s.navigate)
  useClock()
  const time = from ? elapsed(from, to ?? Date.now()) : ''
  return (
    <Button
      variant="ghost"
      className="h-auto min-h-7 w-full justify-start gap-2 py-1 text-left whitespace-normal coarse:min-h-11"
      onClick={() => navigate('run', { runId: runID })}
    >
      <StatusDot tone={tone} pulse={pulse} />
      <span className="line-clamp-2 min-w-0 flex-1 text-ui-sm md:block md:truncate">
        <span className="text-text">{label}</span>
        <span className={cn(shimmer && 'live-shimmer')}> · {detail}</span>
      </span>
      {time && <span className="shrink-0 text-ui-sm tabular-nums">{time}</span>}
    </Button>
  )
}

function LoadedRunLine({ run, label, lead, note, from, to }: RunLineProps & { run: RunRecord }) {
  const shown = useRunPresentation(run)
  const working = shown.state === 'working'
  return (
    <RunRow
      runID={run.id}
      label={label}
      tone={shown.state}
      pulse={working}
      shimmer={working}
      detail={[lead, shown.reason, note].filter(Boolean).join(' · ')}
      from={from}
      to={to}
    />
  )
}

function RunLine(props: RunLineProps) {
  const run = useRun(props.runID)
  if (!props.runID) return <p className="py-1 pl-1 text-ui-sm text-muted">{props.label} · No run yet</p>
  if (!run) {
    return (
      <RunRow
        runID={props.runID}
        label={props.label}
        tone="neutral"
        detail={[props.lead ?? 'Run not loaded', props.note].filter(Boolean).join(' · ')}
        from={props.from}
        to={props.to}
      />
    )
  }
  return <LoadedRunLine run={run} {...props} />
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

function runLabel(name: string, harness: string, mode: string, agents: AgentInfo[] | null): string {
  return `${name} · ${agentLabel(agents?.find((item) => item.name === harness), harness)} · ${modeLabel(mode)}`
}

function AttemptRow({ node, agents }: { node: AttemptNode; agents: AgentInfo[] | null }) {
  const { attempt } = node
  return (
    <li>
      <RunLine
        runID={attempt.run_id}
        label={runLabel(node.name, attempt.harness, attempt.mode, agents)}
        lead={attemptWord[attempt.state]}
        note={node.earlierIntegrator ? 'from a replaced integrator' : undefined}
        from={attempt.started_at}
        to={attempt.finished_at}
      />
    </li>
  )
}

function TaskRow({ node, detail, agents, showProposalBlocker, canRelease, client, onChanged }: {
  node: TaskNode
  detail: MissionDetail
  agents: AgentInfo[] | null
  showProposalBlocker: boolean
  canRelease: boolean
  client: Api
  onChanged: () => void
}) {
  const { task, title, dependsOn, attempts } = node
  const latest = attempts[0]?.attempt
  const held = latest && (latest.takeover_active || latest.orchestration_hold)
  return (
    <li className="border-b border-seam last:border-b-0">
      <Collapsible defaultOpen>
        <div className="flex min-h-7 items-center gap-3 coarse:min-h-11">
          <CollapsibleTrigger className="w-auto min-w-0 flex-1">
            <span className="min-w-0 truncate">{title}</span>
          </CollapsibleTrigger>
          <StateLine tone={statusTone[task.status]} className="shrink-0">{statusWord[task.status]}</StateLine>
        </div>
        {dependsOn.length > 0 && (
          <p className="flex flex-wrap items-center gap-x-1 pb-1 pl-5 text-ui-sm text-muted">
            Depends on
            {dependsOn.map((other, index) => (
              <span key={other.id} className="inline-flex items-center gap-1">
                <StatusDot tone={statusTone[other.status]} label={statusWord[other.status]} />
                {taskTitle(other)}
                {index < dependsOn.length - 1 && ','}
              </span>
            ))}
          </p>
        )}
        {held && (
          <div className="pb-2 pl-5">
            <ControlHold attempt={latest} canRelease={canRelease} client={client} onReleased={onChanged} />
          </div>
        )}
        <CollapsibleContent>
          <div className="pl-5">
            {attempts.length === 0 ? (
              <p className="py-1 text-ui-sm text-muted">No worker yet</p>
            ) : (
              <ul className="flex flex-col">
                {attempts.map((attempt) => (
                  <AttemptRow key={attempt.attempt.id} node={attempt} agents={agents} />
                ))}
              </ul>
            )}
            <Collapsible>
              <CollapsibleTrigger>Task details</CollapsibleTrigger>
              <CollapsibleContent>
                <TaskDetails task={task} detail={detail} showProposalBlocker={showProposalBlocker} />
              </CollapsibleContent>
            </Collapsible>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </li>
  )
}

function IntegratorLine({ detail, agents }: { detail: MissionDetail; agents: AgentInfo[] | null }) {
  const { integrator, current_integrator_run_id: runID } = detail.mission
  const run = useRun(runID)
  return (
    <RunLine
      runID={runID}
      label={runLabel('Integrator', integrator.harness, integrator.mode, agents)}
      from={run?.started_at ?? run?.created_at}
      to={run?.finished_at}
    />
  )
}

export function SwarmTasks({ detail, agents, canRelease, client, onChanged }: {
  detail: MissionDetail
  agents: AgentInfo[] | null
  canRelease: boolean
  client: Api
  onChanged: () => void
}) {
  const { mission } = detail
  const tree = useMemo(() => missionTree(detail), [detail])
  return (
    <section aria-label="Tasks" className="flex flex-col gap-1">
      <SectionLabel as="h2">Tasks</SectionLabel>
      <Collapsible defaultOpen>
        <div className="flex items-center gap-1">
          <CollapsibleTrigger className="w-auto shrink-0" aria-label="Integrator's tasks" />
          <div className="min-w-0 flex-1">
            <IntegratorLine detail={detail} agents={agents} />
          </div>
        </div>
        <CollapsibleContent>
          <div className="ml-2 border-l border-seam pl-3">
            {tree.length === 0 ? (
              <p className="text-ui text-muted">
                {mission.phase === 'planning' ? 'The integrator has not proposed tasks yet.' : 'This swarm has no tasks.'}
              </p>
            ) : (
              <ul className="flex flex-col">
                {tree.map((node) => (
                  <TaskRow
                    key={node.task.id}
                    node={node}
                    detail={detail}
                    agents={agents}
                    showProposalBlocker={mission.phase === 'active'}
                    canRelease={canRelease}
                    client={client}
                    onChanged={onChanged}
                  />
                ))}
              </ul>
            )}
          </div>
        </CollapsibleContent>
      </Collapsible>
    </section>
  )
}

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import { AskAssistantButton } from '@/components/assistant/ask'
import { agentLabel, modeLabel } from '@/components/launch/modes'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { SectionLabel } from '@/components/ui/section-label'
import { WorkflowTree } from '@/components/workflow/workflow-tree'
import type { Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { memberName } from '@/lib/member-name'
import { plainReason } from '@/lib/reasons'
import { presentRun } from '@/lib/status'
import type { AgentInfo, MissionAttempt, MissionTask, MissionTaskScope } from '@/lib/types'
import { missionTree } from '@/routes/missions/tree'
import { useStore } from '@/store'
import { useCapability, useChildRunFacts } from '@/store/hooks'
import type { MissionDetail } from '@/store/missions'
import { stateContextOf } from '@/store/selectors'
import { integrationSeqOf } from '@/store/workflow-signals'
import { deriveSwarmWorkflow, runFacts, shareTree, type CandidateFacts, type RunFacts, type WorkflowNode } from '@/store/workflow'

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
        <p key={attempt.id} className="text-ui-sm text-state-failed">
          {plainReason(attempt.last_error ?? '')}{' '}
          <AskAssistantButton
            variant="link"
            size="sm"
            ask={{
              draft: 'Why did this task fail?',
              attachments: [{ kind: 'task', workspace_id: detail.mission.workspace_id, mission_id: task.mission_id, task_id: task.id, attempt_id: attempt.id, run_id: attempt.run_id || undefined, label: revision?.title ?? task.id }],
            }}
          >
            Ask the assistant
          </AskAssistantButton>
        </p>
      ))}
    </div>
  )
}

export function useReleaseHold(client: Api, attempt: MissionAttempt | undefined, refresh: () => void) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const release = async () => {
    if (attempt?.takeover_generation == null || busy) return
    setBusy(true)
    setError(null)
    try {
      await client.missionWorkerRelease({ run_id: attempt.run_id, expected_takeover_generation: attempt.takeover_generation })
      toast.success('Human control released')
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
      refresh()
    }
  }
  return { busy, error, release }
}

function ControlHold({ attempt, worker, canRelease, client, onChanged }: {
  attempt: MissionAttempt
  worker: string
  canRelease: boolean
  client: Api
  onChanged: () => void
}) {
  const holder = useStore((s) => (attempt.takeover_member_id ? memberName(s, attempt.takeover_member_id) : undefined))
  const { busy, error, release } = useReleaseHold(client, attempt, onChanged)
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
      {worker}. The integrator cannot steer it until control is released, here or from the worker run.
      {error && <p className="text-state-failed">{error}</p>}
    </Callout>
  )
}

function runLabel(name: string, harness: string, mode: string, agents: AgentInfo[] | null): string {
  return `${name} · ${agentLabel(agents?.find((item) => item.name === harness), harness)} · ${modeLabel(mode)}`
}

function useSwarmRuns(runIDs: string[]): Record<string, RunFacts> {
  const now = useClock()
  const key = useStore((s) => {
    const ctx = stateContextOf(s, now)
    return JSON.stringify(runIDs.flatMap((id) => {
      const run = s.runs[id]
      if (!run) return []
      const shown = presentRun(run, ctx)
      return [[id, runFacts(run, shown, '', shown.needsYou !== undefined)]]
    }))
  })
  return useMemo(() => Object.fromEntries(JSON.parse(key) as [string, RunFacts][]), [key])
}

const candidateDetails = 3

const noCandidates: CandidateFacts[] = []

function useCandidates(detail: MissionDetail, client: Api): { candidates: CandidateFacts[]; error?: string } {
  const cap = useCapability()
  const [state, setState] = useState<{ missionID: string; candidates: CandidateFacts[]; error?: string }>({ missionID: '', candidates: noCandidates })
  const changed = useStore(integrationSeqOf(detail.mission.id))
  const { mission } = detail
  const readable = cap.hasMethod('integration.list') && cap.hasMethod('integration.show')
  useEffect(() => {
    if (!readable) return
    let live = true
    const load = async () => {
      const listed = await client.integrationList({ workspace_id: mission.workspace_id, mission_id: mission.id, limit: 50 })
      const newest = [...listed.candidates].sort((a, b) => b.created_at.localeCompare(a.created_at))
      const shown = await Promise.all(newest.map(async (summary, index): Promise<CandidateFacts> => {
        if (index >= candidateDetails) return summary
        try {
          const { candidate } = await client.integrationShow({ workspace_id: mission.workspace_id, candidate_id: summary.candidate_id })
          return candidate
        } catch (err) {
          return { ...summary, readError: message(err) }
        }
      }))
      if (live) setState({ missionID: mission.id, candidates: shown })
    }
    load().catch((err) => live && setState((current) => ({
      missionID: mission.id,
      candidates: current.missionID === mission.id ? current.candidates : noCandidates,
      error: message(err),
    })))
    return () => {
      live = false
    }
  }, [client, detail, mission.id, mission.workspace_id, readable, changed])
  return state.missionID === mission.id ? state : { candidates: noCandidates }
}

export function SwarmTasks({ detail, agents, canRelease, client, onChanged }: {
  detail: MissionDetail
  agents: AgentInfo[] | null
  canRelease: boolean
  client: Api
  onChanged: () => void
}) {
  const { mission } = detail
  const navigate = useStore((s) => s.navigate)
  const runIDs = useMemo(
    () => [mission.current_integrator_run_id, ...detail.attempts.map((attempt) => attempt.run_id)].filter(Boolean),
    [mission.current_integrator_run_id, detail.attempts],
  )
  const runs = useSwarmRuns(runIDs)
  const childRuns = useChildRunFacts(runIDs)
  const { candidates, error } = useCandidates(detail, client)
  const previous = useRef<WorkflowNode | undefined>(undefined)
  const tree = useMemo(() => {
    const next = shareTree(previous.current, deriveSwarmWorkflow({
      detail,
      runs,
      candidates,
      childRuns,
      integratorLabel: runLabel('Integrator', mission.integrator.harness, mission.integrator.mode, agents),
      workerLabel: (attempt, name) => runLabel(name, attempt.harness, attempt.mode, agents),
    }))
    previous.current = next
    return next
  }, [detail, runs, candidates, childRuns, agents, mission.integrator.harness, mission.integrator.mode])
  const tasks = useMemo(() => new Map(detail.tasks.map((task) => [`task:${task.id}`, task])), [detail.tasks])
  const held = useMemo(() => missionTree(detail).flatMap((node) => {
    const latest = node.attempts[0]
    return latest && (latest.attempt.takeover_active || latest.attempt.orchestration_hold)
      ? [{ attempt: latest.attempt, worker: `${latest.name} on ${node.title}` }]
      : []
  }), [detail])
  const select = (node: WorkflowNode) => {
    if (!node.runId || node.kind === 'task' || node.kind === 'candidate') return false
    navigate('run', { runId: node.runId })
    return true
  }
  const extra = useCallback((node: WorkflowNode, expanded: boolean | undefined) => {
    const task = tasks.get(node.id)
    if (!task || expanded === false) return null
    return (
      <Collapsible>
        <CollapsibleTrigger className="text-ui-sm text-muted">Task details</CollapsibleTrigger>
        <CollapsibleContent>
          <TaskDetails task={task} detail={detail} showProposalBlocker={mission.phase === 'active'} />
        </CollapsibleContent>
      </Collapsible>
    )
  }, [tasks, detail, mission.phase])
  return (
    <section aria-label="Tasks" className="flex flex-col gap-1">
      <SectionLabel as="h2">Tasks</SectionLabel>
      {held.map(({ attempt, worker }) => (
        <ControlHold key={attempt.id} attempt={attempt} worker={worker} canRelease={canRelease} client={client} onChanged={onChanged} />
      ))}
      <WorkflowTree root={tree} label="Swarm workflow" onSelect={select} extra={extra} />
      {detail.tasks.length === 0 && (
        <p className="pl-5 text-ui text-muted">
          {mission.phase === 'planning' ? 'The integrator has not proposed tasks yet.' : 'This swarm has no tasks.'}
        </p>
      )}
      {error && <p className="pl-5 text-ui-sm text-state-failed">Integration candidates: {error}</p>}
    </section>
  )
}

import type { ReactNode } from 'react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { agentLabel, modeLabel } from '@/components/launch/modes'
import type { Api } from '@/lib/api'
import type { AgentInfo } from '@/lib/types'
import { CandidateReview } from '@/routes/terminal/candidate-review'
import { useStore } from '@/store'
import type { MissionDetail } from '@/store/missions'

function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <>
      <dt className="truncate text-muted" title={term}>{term}</dt>
      <dd className="min-w-0 font-code break-all whitespace-pre-line">{children}</dd>
    </>
  )
}

function TechnicalDetails({ detail, agents }: { detail: MissionDetail; agents: AgentInfo[] | null }) {
  const { mission, tasks, attempts, submissions } = detail
  const members = useStore((s) => s.members)
  const taskTitle = (taskID: string) => tasks.find((task) => task.id === taskID)?.revision?.title || taskID
  const accepted = submissions.filter((item) => item.state === 'accepted')
  return (
    <Collapsible>
      <CollapsibleTrigger>Technical details</CollapsibleTrigger>
      <CollapsibleContent>
        <dl className="grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)] gap-x-4 gap-y-1 py-2 pl-5 text-ui-sm">
          <Fact term="Swarm">{mission.id}</Fact>
          <Fact term="Integrator run">{mission.current_integrator_run_id || 'none'}</Fact>
          <Fact term="Integrator generation">{mission.integrator_generation}</Fact>
          <Fact term="Accepted set">{mission.accepted_set_version}</Fact>
          <Fact term="Execution choices">
            {mission.execution_choices
              .map((c) => `${agentLabel(agents?.find((agent) => agent.name === c.harness), c.harness)} · ${modeLabel(c.mode)} · ${members[c.account_member_id]?.display_name ?? c.account_member_id}`)
              .join('\n')}
          </Fact>
          {tasks.map((task) => (
            <Fact key={task.id} term={task.revision?.title || task.id}>
              {task.id} · revision {task.current_revision}
              {task.pending_revision ? ` · pending ${task.pending_revision.revision}` : ''}
            </Fact>
          ))}
          {attempts.map((attempt) => (
            <Fact key={attempt.id} term={`${taskTitle(attempt.task_id)} · attempt ${attempt.number}`}>
              {attempt.run_id} · {attempt.state} · rev {attempt.task_revision}
              {attempt.takeover_generation != null ? ` · takeover ${attempt.takeover_generation}` : ''}
            </Fact>
          ))}
          {accepted.map((submission) => (
            <Fact key={submission.id} term={`Accepted · revision ${submission.task_revision}`}>
              {submission.ref.run_id} · {submission.ref.retained_revision}
              {submission.ref.evidence_ref && ` · evidence ${submission.ref.evidence_ref}`}
              {submission.evidence
                .filter((source) => !source.available || source.truncated)
                .map((source) => `\n${source.kind}: ${source.available ? 'partial (truncated) at acceptance' : 'unavailable at acceptance'}`)
                .join('')}
              {(submission.scope_violations ?? []).length > 0 && `\nScope violations: ${submission.scope_violations!.join(', ')}`}
              {submission.acceptance?.scope_disposition && `\nScope disposition: ${submission.acceptance.scope_disposition}`}
            </Fact>
          ))}
        </dl>
      </CollapsibleContent>
    </Collapsible>
  )
}

export function SwarmIntegration({ detail, agents, client }: { detail: MissionDetail; agents: AgentInfo[] | null; client: Api }) {
  const { mission } = detail
  return (
    <section aria-label="Integration" className="flex flex-col gap-1">
      <Collapsible>
        <CollapsibleTrigger>Integration</CollapsibleTrigger>
        <CollapsibleContent>
          <div className="flex flex-col gap-2 pl-5">
            <p className="text-ui-sm text-muted">The integrator prepares, verifies and delivers the result on its own. This view is read-only.</p>
            {mission.current_integrator_run_id && (
              <CandidateReview
                workspaceID={mission.workspace_id}
                currentRunID={mission.current_integrator_run_id}
                missionID={mission.id}
                readOnly
                initialExpanded
                client={client}
              />
            )}
            <TechnicalDetails detail={detail} agents={agents} />
          </div>
        </CollapsibleContent>
      </Collapsible>
    </section>
  )
}

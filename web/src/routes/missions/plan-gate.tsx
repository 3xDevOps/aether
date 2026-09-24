// The human plan gate: the clarifying questions the integrator asks, and the
// approve/request-changes/reject decision that turns a plan into dispatchable
// work, both for the initial plan and for an amendment that changes it after
// activation. No new work dispatches until a human decides the round.

import { useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { message, timeAgo } from '@/lib/format'
import type {
  Member,
  Mission,
  MissionPhase,
  MissionPlanDecision,
  MissionPlanItem,
  MissionPlanReview,
  MissionQuestion,
  MissionScopeDiagnostic,
  MissionTask,
  MissionTaskRevision,
} from '@/lib/types'
import { useStore } from '@/store'
import { isTerminal, type RunRecord } from '@/store/runs'

export function ErrorNotice({ error }: { error: string }) {
  return <p role="alert" className="mb-3 break-words border-l-2 border-state-failed bg-state-failed/10 px-2 py-1.5 text-xs text-state-failed">{error}</p>
}

function displayName(members: Record<string, Member>, memberID?: string): string {
  if (!memberID) return 'a human'
  return members[memberID]?.display_name ?? memberID
}

/** A phase the dashboard does not know - a server that predates the gate, or
 * a phase added after this build - renders as the pre-gate active view rather
 * than a page with no tasks on it. */
export function missionPhase(mission: Mission): MissionPhase {
  switch (mission.phase) {
    case 'planning':
    case 'clarified':
    case 'plan_review':
    case 'amendment_review':
    case 'rejected':
      return mission.phase
    default:
      return 'active'
  }
}

function phaseChipLabel(mission: Mission): string {
  switch (missionPhase(mission)) {
    case 'planning':
      return mission.open_questions > 0
        ? `Planning · ${mission.open_questions} question${mission.open_questions === 1 ? '' : 's'} for you`
        : 'Planning'
    case 'clarified':
      return 'Preparing plan'
    case 'plan_review':
      return 'Plan ready for review'
    case 'amendment_review':
      return 'Amendment ready for review'
    case 'rejected':
      return 'Rejected'
    default:
      return 'Active'
  }
}

const phaseChipColor: Record<Mission['phase'], 'accent' | 'default' | 'warning' | 'danger'> = {
  planning: 'default',
  clarified: 'default',
  plan_review: 'warning',
  active: 'accent',
  amendment_review: 'warning',
  rejected: 'danger',
}

/** The mission-list marker. It replaces the generic Mission chip: which phase
 * a mission is in is the only thing the card can say that changes what the
 * reader must do. */
export function PhaseChip({ mission }: { mission: Mission }) {
  return (
    <Chip color={phaseChipColor[missionPhase(mission)]} variant="soft" size="sm">
      <Chip.Label>{phaseChipLabel(mission)}</Chip.Label>
    </Chip>
  )
}

const phaseTone: Record<Mission['phase'], string> = {
  planning: 'border-accent bg-accent/10',
  clarified: 'border-accent bg-accent/10',
  plan_review: 'border-state-needs-attention bg-state-needs-attention/10',
  active: 'border-state-success bg-state-success/10',
  amendment_review: 'border-state-needs-attention bg-state-needs-attention/10',
  rejected: 'border-state-failed bg-state-failed/10',
}

function phaseSentence(mission: Mission): string {
  switch (missionPhase(mission)) {
    case 'planning':
      return 'The integrator may ask you clarifying questions, then submits a plan. No worker starts until you approve it.'
    case 'clarified':
      return 'Clarification is complete. The integrator is preparing the plan for your review.'
    case 'plan_review':
      return `Plan version ${mission.plan_version} is waiting for your decision. No worker starts until you approve it.`
    case 'amendment_review':
      return `Plan version ${mission.plan_version} amends the approved plan. Approved work continues; nothing new starts until you decide.`
    case 'rejected':
      return `Plan version ${mission.plan_version} was rejected. The integrator run is being cancelled and no worker will start.`
    default:
      return `Plan version ${mission.plan_version} is approved. Workers dispatch within the authorized limits.`
  }
}

export function PhaseBanner({
  mission,
  integratorRun,
  integratorMissing,
  canReplace,
  onReplace,
}: {
  mission: Mission
  integratorRun?: RunRecord
  /** The server confirmed it holds no run for `current_integrator_run_id`. */
  integratorMissing: boolean
  canReplace: boolean
  onReplace: () => void
}) {
  const integratorRunID = mission.current_integrator_run_id
  // A rejected mission's integrator is cancelled on purpose and
  // mission.replace-integrator is refused in that phase, so the recovery
  // sentence would offer a control the server will not accept.
  const phase = missionPhase(mission)
  const exited = phase !== 'rejected' && Boolean(integratorRun && isTerminal(integratorRun.status))
  const notStarted = phase !== 'rejected' && integratorMissing
  return (
    <section aria-label="Mission phase" className={`mb-3 border-l-2 px-2 py-1.5 text-xs ${phaseTone[phase]}`}>
      <p className="font-medium">{phaseChipLabel(mission)}</p>
      <p className="mt-0.5">
        {notStarted
          ? `The integrator run has not started. The server retries the launch periodically and logs each failure as "mission: recover integrator".`
          : phaseSentence(mission)}
      </p>
      {(exited || notStarted) && mission.integrator_launch_error && (
        <p className="mt-1 break-words text-state-failed">
          Last launch failure
          {mission.integrator_launch_error_at && (
            <>
              {' '}
              <time dateTime={mission.integrator_launch_error_at} title={mission.integrator_launch_error_at}>
                {timeAgo(mission.integrator_launch_error_at)}
              </time>
            </>
          )}
          : {mission.integrator_launch_error}
        </p>
      )}
      {(exited || (notStarted && canReplace)) && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          {exited && <span className="min-w-0 break-words">The integrator run {integratorRunID} has exited; replace the integrator to continue</span>}
          {canReplace && (
            <Button size="sm" variant="outline" onClick={onReplace}>
              Replace integrator
            </Button>
          )}
        </div>
      )}
    </section>
  )
}

export function QuestionsSection({
  questions,
  canAnswer,
  complete,
  client,
  onAnswered,
}: {
  questions: MissionQuestion[]
  canAnswer: boolean
  /** In `clarified` the integrator declared it has what it needs, and the
   * server refuses an answer, so the section is a record rather than a form. */
  complete: boolean
  client: Api
  onAnswered: () => void
}) {
  const members = useStore((state) => state.members)
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [answerError, setAnswerError] = useState<Record<string, string>>({})
  const [answeringID, setAnsweringID] = useState<string | null>(null)

  const answer = async (question: MissionQuestion) => {
    const body = (drafts[question.id] ?? '').trim()
    if (!body || answeringID) return
    setAnsweringID(question.id)
    setAnswerError((current) => ({ ...current, [question.id]: '' }))
    try {
      await client.missionQuestionAnswer({
        question_id: question.id,
        answer: body,
        idempotency_key: `question-answer-${question.id}`,
      })
      setDrafts((current) => ({ ...current, [question.id]: '' }))
      onAnswered()
    } catch (error) {
      setAnswerError((current) => ({ ...current, [question.id]: message(error) }))
    } finally {
      setAnsweringID(null)
    }
  }

  return (
    <section className="mt-3 border bg-card p-3 sm:p-4" aria-label="Questions from the integrator">
      <h2 className="text-sm font-semibold">Questions from the integrator</h2>
      {complete && <p className="mt-1 text-xs text-muted-foreground">Clarification complete.</p>}
      {questions.length === 0 && (
        <p className="mt-1 text-xs text-muted-foreground">The integrator has not asked anything yet.</p>
      )}
      <div className="mt-2 space-y-3 text-xs">
        {questions.map((question) => {
          const answered = Boolean(question.answered_at)
          const draft = drafts[question.id] ?? ''
          const failure = answerError[question.id]
          return (
            <article key={question.id} className="border-t pt-3 first:border-t-0 first:pt-0">
              <p className="whitespace-pre-wrap break-words font-medium">{`${question.seq}. ${question.body}`}</p>
              {answered && (
                <div className="mt-1 border-l-2 border-state-success bg-state-success/10 px-2 py-1.5">
                  <p className="font-medium">Answered by {displayName(members, question.answered_by_member_id)}</p>
                  <p className="mt-0.5 whitespace-pre-wrap break-words">{question.answer}</p>
                </div>
              )}
              {failure && <div className="mt-2"><ErrorNotice error={failure} /></div>}
              {canAnswer && (!answered || draft !== '') && (
                <div className="mt-2 space-y-1.5">
                  <Label className="block space-y-1.5">
                    <span>Answer question {question.seq}</span>
                    <Textarea
                      rows={3}
                      value={draft}
                      onChange={(event) => setDrafts((current) => ({ ...current, [question.id]: event.target.value }))}
                    />
                  </Label>
                  {answered ? (
                    <p className="text-muted-foreground">Answered while you were typing. Your draft is kept here.</p>
                  ) : (
                    <Button
                      size="sm"
                      disabled={answeringID !== null || draft.trim() === ''}
                      onClick={() => void answer(question)}
                    >
                      {answeringID === question.id ? 'Answering…' : 'Answer'}
                    </Button>
                  )}
                </div>
              )}
            </article>
          )
        })}
      </div>
    </section>
  )
}

function PlanDecision({
  mission,
  canDecide,
  canReject,
  client,
  onDecided,
}: {
  mission: Mission
  canDecide: boolean
  /** An amendment is approved or sent back; the server refuses to reject one,
   * because the already-approved work it amends keeps running either way. */
  canReject: boolean
  client: Api
  onDecided: () => void
}) {
  const [feedback, setFeedback] = useState('')
  const [decideError, setDecideError] = useState<string | null>(null)
  const [deciding, setDeciding] = useState<MissionPlanDecision | null>(null)

  const decide = async (decision: MissionPlanDecision) => {
    if (deciding) return
    const trimmed = feedback.trim()
    setDeciding(decision)
    setDecideError(null)
    try {
      await client.missionPlanDecide({
        mission_id: mission.id,
        expected_plan_version: mission.plan_version,
        decision,
        ...(trimmed ? { feedback: trimmed } : {}),
        idempotency_key: `plan-decide-${mission.id}-${mission.plan_version}-${decision}`,
      })
      onDecided()
    } catch (error) {
      setDecideError(message(error))
    } finally {
      setDeciding(null)
    }
  }

  if (!canDecide) {
    return <p className="mt-3 text-xs text-muted-foreground">Only the accountable human or an admin may decide this plan.</p>
  }
  return (
    <>
      {decideError && <div className="mt-3"><ErrorNotice error={decideError} /></div>}
      <div className="mt-3 space-y-2">
        <Label className="block space-y-1.5 text-xs">
          <span>Feedback (required to request changes)</span>
          <Textarea rows={3} value={feedback} onChange={(event) => setFeedback(event.target.value)} />
        </Label>
        <div className="flex flex-wrap gap-1">
          <Button size="sm" disabled={deciding !== null} onClick={() => void decide('approve')}>Approve</Button>
          <Button size="sm" variant="outline" disabled={deciding !== null || feedback.trim() === ''} onClick={() => void decide('revise')}>Request changes</Button>
          {canReject && <Button size="sm" variant="outline" disabled={deciding !== null} onClick={() => void decide('reject')}>Reject</Button>}
        </div>
      </div>
    </>
  )
}

export function PlanReviewSection({
  mission,
  review,
  canDecide,
  client,
  onDecided,
  children,
}: {
  mission: Mission
  review?: MissionPlanReview
  canDecide: boolean
  client: Api
  onDecided: () => void
  children: ReactNode
}) {
  return (
    <section className="mt-3 border bg-card p-3 sm:p-4" aria-label="Plan review">
      <h2 className="text-sm font-semibold">Plan review</h2>
      <p className="mt-1 font-mono text-[11px] text-muted-foreground">plan version {mission.plan_version}</p>
      <p className="mt-2 whitespace-pre-wrap break-words text-xs">
        {review?.summary || 'The integrator submitted no summary.'}
      </p>
      <div className="mt-3 space-y-2">{children}</div>
      <PlanDecision mission={mission} canDecide={canDecide} canReject client={client} onDecided={onDecided} />
    </section>
  )
}

/** The amendment round only: what changes about a plan the human already
 * approved. The approved tasks themselves keep rendering in the Tasks
 * section, because their workers are still running. */
export function AmendmentReviewSection({
  mission,
  review,
  tasks,
  diagnostics,
  canDecide,
  client,
  onDecided,
}: {
  mission: Mission
  review?: MissionPlanReview
  tasks: MissionTask[]
  diagnostics: MissionScopeDiagnostic[]
  canDecide: boolean
  client: Api
  onDecided: () => void
}) {
  const items = review?.items ?? []
  return (
    <section className="mt-3 border bg-card p-3 sm:p-4" aria-label="Amendment review">
      <h2 className="text-sm font-semibold">Amendment review</h2>
      <p className="mt-1 font-mono text-[11px] text-muted-foreground">plan version {mission.plan_version}</p>
      <p className="mt-2 whitespace-pre-wrap break-words text-xs">
        {review?.summary || 'The integrator submitted no summary.'}
      </p>
      <div className="mt-3 space-y-2">
        {items.length === 0 && (
          <p className="text-xs text-muted-foreground">This round has no items left to decide.</p>
        )}
        {items.map((item) => (
          <AmendmentItemCard
            key={item.task_id}
            item={item}
            task={tasks.find((candidate) => candidate.id === item.task_id)}
            diagnostics={diagnostics.filter(
              (diagnostic) =>
                diagnostic.kind === 'intended_overlap' &&
                (diagnostic.task_id === item.task_id || diagnostic.peer_task_id === item.task_id),
            )}
          />
        ))}
      </div>
      <PlanDecision mission={mission} canDecide={canDecide} canReject={false} client={client} onDecided={onDecided} />
    </section>
  )
}

function AmendmentItemCard({
  item,
  task,
  diagnostics,
}: {
  item: MissionPlanItem
  task?: MissionTask
  diagnostics: MissionScopeDiagnostic[]
}) {
  const widening = item.widening ?? []
  // The item names its revision; a new task revised again in `active` keeps
  // its first draft as current_revision while the item is the later one.
  const proposed = [task?.pending_revision, task?.revision].find((candidate) => candidate?.revision === item.revision)
  return (
    <article className="border bg-card p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="break-words font-medium">{item.new_task ? 'New work' : 'Changed task'} · {item.title}</p>
          <p className="mt-1 font-mono text-[11px] text-muted-foreground">
            {item.task_id} · revision {item.revision}
            {item.supersedes_revision ? ` · supersedes ${item.supersedes_revision}` : ''}
          </p>
        </div>
        {item.material && (
          <Chip color="warning" variant="soft" size="sm">
            <Chip.Label>Material</Chip.Label>
          </Chip>
        )}
      </div>
      {proposed ? (
        <div className="mt-3 grid gap-2 text-xs sm:grid-cols-2">
          {!item.new_task && (
            <div>
              <p className="font-medium text-muted-foreground">Approved now</p>
              {task?.revision ? (
                <RevisionFacts revision={task.revision} widening={[]} />
              ) : (
                <p className="mt-0.5 text-muted-foreground">No approved revision is recorded.</p>
              )}
            </div>
          )}
          <div>
            <p className="font-medium text-muted-foreground">{item.new_task ? 'Proposed' : 'Proposed instead'}</p>
            <RevisionFacts revision={proposed} widening={widening} />
          </div>
        </div>
      ) : (
        <p className="mt-3 text-xs text-muted-foreground">This revision is no longer pending.</p>
      )}
      {diagnostics.length > 0 && (
        <div className="mt-3 border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-2 py-1.5 text-xs">
          <p className="font-medium">Intended overlap</p>
          {diagnostics.map((diagnostic, index) => (
            <p key={`${diagnostic.task_id}-${index}`} className="mt-0.5 break-words">
              {diagnostic.paths.length > 0 ? diagnostic.paths.join(', ') : diagnostic.detail || 'No paths reported'}
              {` · with ${diagnostic.task_id === item.task_id ? diagnostic.peer_task_id : diagnostic.task_id}`}
            </p>
          ))}
        </div>
      )}
    </article>
  )
}

/** `widening` is the server's list for this item; a path or exclusion in it
 * reaches outside the approved plan and is what the decision is about. */
function RevisionFacts({ revision, widening }: { revision: MissionTaskRevision; widening: string[] }) {
  const paths = revision.scope.expected_paths ?? []
  const exclusions = revision.scope.exclusions ?? []
  // A dropped exclusion widens the approved scope too, and the revision that
  // dropped it no longer lists it, so it is named from the server's list.
  const dropped = widening.filter((path) => !paths.includes(path))
  return (
    <div className="mt-0.5 space-y-1">
      <p className="whitespace-pre-wrap break-words">{revision.objective}</p>
      {paths.length + exclusions.length + dropped.length === 0 ? (
        <p className="text-muted-foreground">No scope details reported.</p>
      ) : (
        <ul className="list-disc pl-4">
          {paths.map((path) => (
            <li key={`path-${path}`} className={widening.includes(path) ? 'text-state-needs-attention' : undefined}>
              Expected {path}{widening.includes(path) ? ' · widens the approved scope' : ''}
            </li>
          ))}
          {exclusions.map((path) => (
            <li key={`exclusion-${path}`}>Excluded {path}</li>
          ))}
          {dropped.map((path) => (
            <li key={`widening-${path}`} className="text-state-needs-attention">
              {path} · widens the approved scope
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** Questions and decided review rounds, collapsed once the gate is behind the
 * mission. An undecided round is the live plan, not history, so it is never
 * listed here. */
export function PlanningHistory({
  questions,
  reviews,
}: {
  questions: MissionQuestion[]
  reviews: MissionPlanReview[]
}) {
  const members = useStore((state) => state.members)
  const [open, setOpen] = useState(false)
  const decided = reviews.filter((review) => review.decision)
  if (!questions.length && !decided.length) return null
  return (
    <section className="mt-3 border bg-card p-3 sm:p-4" aria-label="Planning history">
      <Collapsible open={open} onOpenChange={setOpen}>
        <CollapsibleTrigger>
          Planning history · {questions.length} question{questions.length === 1 ? '' : 's'} · {decided.length} review round{decided.length === 1 ? '' : 's'}
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="mt-2 space-y-3 text-xs">
            {questions.map((question) => (
              <article key={question.id}>
                <p className="whitespace-pre-wrap break-words font-medium">{`${question.seq}. ${question.body}`}</p>
                <p className="mt-0.5 whitespace-pre-wrap break-words text-muted-foreground">
                  {question.answered_at
                    ? `${question.answer} — answered by ${displayName(members, question.answered_by_member_id)}`
                    : 'Unanswered.'}
                </p>
              </article>
            ))}
            {decided.map((review) => (
              <article key={review.plan_version} className="border-t pt-3">
                <p className="font-medium">Plan version {review.plan_version} · {review.decision} by {displayName(members, review.decided_by_member_id)}</p>
                <p className="mt-0.5 whitespace-pre-wrap break-words">{review.summary}</p>
                {review.feedback && <p className="mt-0.5 whitespace-pre-wrap break-words text-muted-foreground">Feedback: {review.feedback}</p>}
              </article>
            ))}
          </div>
        </CollapsibleContent>
      </Collapsible>
    </section>
  )
}

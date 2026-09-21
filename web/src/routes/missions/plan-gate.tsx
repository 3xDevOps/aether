// The human plan gate: the clarifying questions the integrator asks, and the
// approve/request-changes/reject decision that turns a plan into dispatchable
// work. Only `active` dispatches workers, so everything here is what a human
// must do before any worker starts.

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
import { message } from '@/lib/format'
import type {
  Member,
  Mission,
  MissionPhase,
  MissionPlanDecision,
  MissionPlanReview,
  MissionQuestion,
} from '@/lib/types'
import { useStore } from '@/store'
import { isTerminal } from '@/store/runs'

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
    case 'plan_review':
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
    case 'plan_review':
      return 'Plan ready for review'
    case 'rejected':
      return 'Rejected'
    default:
      return 'Active'
  }
}

const phaseChipColor: Record<Mission['phase'], 'accent' | 'default' | 'warning' | 'danger'> = {
  planning: 'default',
  plan_review: 'warning',
  active: 'accent',
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
  plan_review: 'border-state-needs-attention bg-state-needs-attention/10',
  active: 'border-state-success bg-state-success/10',
  rejected: 'border-state-failed bg-state-failed/10',
}

function phaseSentence(mission: Mission): string {
  switch (missionPhase(mission)) {
    case 'planning':
      return 'The integrator asks you clarifying questions and submits a plan. No worker starts until you approve it.'
    case 'plan_review':
      return `Plan version ${mission.plan_version} is waiting for your decision. No worker starts until you approve it.`
    case 'rejected':
      return `Plan version ${mission.plan_version} was rejected. The integrator run is being cancelled and no worker will start.`
    default:
      return `Plan version ${mission.plan_version} is approved. Workers dispatch within the authorized limits.`
  }
}

export function PhaseBanner({
  mission,
  canReplace,
  onReplace,
}: {
  mission: Mission
  canReplace: boolean
  onReplace: () => void
}) {
  const integratorRunID = mission.current_integrator_run_id
  const integratorRun = useStore((state) => (integratorRunID ? state.runs[integratorRunID] : undefined))
  // A rejected mission's integrator is cancelled on purpose and
  // mission.replace-integrator is refused in that phase, so the recovery
  // sentence would offer a control the server will not accept.
  const phase = missionPhase(mission)
  const exited = phase !== 'rejected' && Boolean(integratorRun && isTerminal(integratorRun.status))
  return (
    <section aria-label="Mission phase" className={`mb-3 border-l-2 px-2 py-1.5 text-xs ${phaseTone[phase]}`}>
      <p className="font-medium">{phaseChipLabel(mission)}</p>
      <p className="mt-0.5">{phaseSentence(mission)}</p>
      {exited && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <span className="min-w-0 break-words">The integrator run {integratorRunID} has exited; replace the integrator to continue</span>
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
  client,
  onAnswered,
}: {
  questions: MissionQuestion[]
  canAnswer: boolean
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

  return (
    <section className="mt-3 border bg-card p-3 sm:p-4" aria-label="Plan review">
      <h2 className="text-sm font-semibold">Plan review</h2>
      <p className="mt-1 font-mono text-[11px] text-muted-foreground">plan version {mission.plan_version}</p>
      <p className="mt-2 whitespace-pre-wrap break-words text-xs">
        {review?.summary || 'The integrator submitted no summary.'}
      </p>
      <div className="mt-3 space-y-2">{children}</div>
      {decideError && <div className="mt-3"><ErrorNotice error={decideError} /></div>}
      {canDecide ? (
        <div className="mt-3 space-y-2">
          <Label className="block space-y-1.5 text-xs">
            <span>Feedback (required to request changes)</span>
            <Textarea rows={3} value={feedback} onChange={(event) => setFeedback(event.target.value)} />
          </Label>
          <div className="flex flex-wrap gap-1">
            <Button size="sm" disabled={deciding !== null} onClick={() => void decide('approve')}>Approve</Button>
            <Button size="sm" variant="outline" disabled={deciding !== null || feedback.trim() === ''} onClick={() => void decide('revise')}>Request changes</Button>
            <Button size="sm" variant="outline" disabled={deciding !== null} onClick={() => void decide('reject')}>Reject</Button>
          </div>
        </div>
      ) : (
        <p className="mt-3 text-xs text-muted-foreground">Only the accountable human or an admin may decide this plan.</p>
      )}
    </section>
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

// The mission's phase as the human sees it, and the clarifying questions the
// integrator may ask while it plans.

import { useState } from 'react'
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
import type { Member, Mission, MissionPhase, MissionQuestion } from '@/lib/types'
import { useStore } from '@/store'
import { isTerminal, type RunRecord } from '@/store/runs'

export function ErrorNotice({ error }: { error: string }) {
  return <p role="alert" className="mb-3 break-words border-l-2 border-state-failed bg-state-failed/10 px-2 py-1.5 text-xs text-state-failed">{error}</p>
}

function displayName(members: Record<string, Member>, memberID?: string): string {
  if (!memberID) return 'a human'
  return members[memberID]?.display_name ?? memberID
}

/** Completed and cancelled missions never run again. */
export function missionFinal(mission: Mission): boolean {
  return mission.phase === 'completed' || mission.phase === 'cancelled'
}

export function phaseLabel(mission: Mission): string {
  switch (mission.phase) {
    case 'planning':
      return mission.open_questions > 0
        ? `Planning · ${mission.open_questions} question${mission.open_questions === 1 ? '' : 's'} for you`
        : 'Planning'
    case 'active':
      return 'Active'
    case 'completed':
      return 'Completed'
    case 'cancelled':
      return 'Cancelled'
  }
}

const phaseChipColor: Record<MissionPhase, 'accent' | 'default' | 'success' | 'danger'> = {
  planning: 'default',
  active: 'accent',
  completed: 'success',
  cancelled: 'danger',
}

/** The mission-list marker. It replaces the generic Mission chip: which phase
 * a mission is in is the only thing the card can say that changes what the
 * reader must do. */
export function PhaseChip({ mission }: { mission: Mission }) {
  return (
    <Chip color={phaseChipColor[mission.phase]} variant="soft" size="sm">
      <Chip.Label>{phaseLabel(mission)}</Chip.Label>
    </Chip>
  )
}

const phaseTone: Record<MissionPhase, string> = {
  planning: 'border-accent bg-accent/10',
  active: 'border-accent bg-accent/10',
  completed: 'border-state-success bg-state-success/10',
  cancelled: 'border-state-failed bg-state-failed/10',
}

const phaseSentence: Record<MissionPhase, string> = {
  planning: 'The integrator asks you clarifying questions only if it needs answers, then proposes tasks and starts the swarm.',
  active: 'Workers run within the authorized limits. The integrator accepts their work, verifies and delivers the result, then reports success.',
  completed: 'The integrator reported success. Leftover workers were stopped.',
  cancelled: 'The swarm was cancelled. Its workers and integrator run are stopped.',
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
  // A finished mission's integrator stops on purpose and
  // mission.replace-integrator is refused there, so the recovery sentence
  // would offer a control the server will not accept.
  const final = missionFinal(mission)
  const exited = !final && Boolean(integratorRun && isTerminal(integratorRun.status))
  const notStarted = !final && integratorMissing
  return (
    <section aria-label="Mission phase" className={`mb-3 border-l-2 px-2 py-1.5 text-xs ${phaseTone[mission.phase]}`}>
      <p className="font-medium">{phaseLabel(mission)}</p>
      <p className="mt-0.5">
        {!notStarted
          ? phaseSentence[mission.phase]
          : mission.integrator_run_launched
            ? 'The integrator run was deleted; replace the integrator or cancel the swarm.'
            : `The integrator run has not started. The server retries the launch periodically and logs each failure as "mission: recover integrator".`}
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

/** The questions asked while planning, collapsed once the mission started. */
export function QuestionHistory({ questions }: { questions: MissionQuestion[] }) {
  const members = useStore((state) => state.members)
  const [open, setOpen] = useState(false)
  if (!questions.length) return null
  return (
    <section className="mt-3 border bg-card p-3 sm:p-4" aria-label="Planning questions">
      <Collapsible open={open} onOpenChange={setOpen}>
        <CollapsibleTrigger>
          Planning questions · {questions.length}
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="mt-2 space-y-3 text-xs">
            {questions.map((question) => (
              <article key={question.id}>
                <p className="whitespace-pre-wrap break-words font-medium">{`${question.seq}. ${question.body}`}</p>
                <p className="mt-0.5 whitespace-pre-wrap break-words text-muted-foreground">
                  {question.answered_at
                    ? `${question.answer} - answered by ${displayName(members, question.answered_by_member_id)}`
                    : 'Unanswered.'}
                </p>
              </article>
            ))}
          </div>
        </CollapsibleContent>
      </Collapsible>
    </section>
  )
}

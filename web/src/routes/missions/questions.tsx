import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FormField } from '@/components/ui/form-field'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { MissionQuestion } from '@/lib/types'
import { useStore } from '@/store'
import { useHeaderPrimary } from '@/store/hooks'

export const answerFormID = (question: MissionQuestion) => `swarm-answer-${question.id}`

function useMemberName(memberID: string | undefined): string {
  return useStore((s) => (memberID ? s.members[memberID]?.display_name ?? memberID : 'a human'))
}

function OpenQuestion({
  question,
  draft,
  setDraft,
  canAnswer,
  accountable,
  client,
  onAnswered,
}: {
  question: MissionQuestion
  draft: string
  setDraft: (draft: string) => void
  canAnswer: boolean
  accountable: string
  client: Api
  onAnswered: () => void
}) {
  const answeredBy = useMemberName(question.answered_by_member_id)
  const [error, setError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)
  const answered = Boolean(question.answered_at)

  const answer = async () => {
    const body = draft.trim()
    if (!body || sending) return
    setSending(true)
    setError(null)
    try {
      await client.missionQuestionAnswer({ question_id: question.id, answer: body, idempotency_key: `question-answer-${question.id}` })
      setDraft('')
      onAnswered()
    } catch (err) {
      setError(message(err))
    } finally {
      setSending(false)
    }
  }

  return (
    <Callout
      tone="needs-you"
      title={<>The integrator asks <span className="font-normal text-muted"><RelativeTime at={question.asked_at} /></span></>}
    >
      <p className="whitespace-pre-wrap">{question.body}</p>
      {canAnswer ? (
        <form
          id={answerFormID(question)}
          className="mt-2 flex flex-col gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            void answer()
          }}
        >
          <FormField label={`Answer question ${question.seq}`} error={error ?? undefined}>
            <Textarea rows={3} value={draft} onChange={(event) => setDraft(event.target.value)} />
          </FormField>
          {answered ? (
            <p className="text-ui-sm text-muted">
              Answered by {answeredBy} while you were typing: {question.answer}. Your draft is kept here.
            </p>
          ) : (
            <div>
              <Button type="submit" size="sm" disabled={sending || draft.trim() === ''}>
                {sending ? 'Answering…' : 'Answer'}
              </Button>
            </div>
          )}
        </form>
      ) : (
        <p className="mt-1 text-ui-sm text-muted">Only {accountable} or an admin can answer.</p>
      )}
    </Callout>
  )
}

function AnsweredQuestion({ question }: { question: MissionQuestion }) {
  const by = useMemberName(question.answered_by_member_id)
  return (
    <Collapsible>
      <CollapsibleTrigger>
        <span className="min-w-0 flex-1 truncate">{question.body}</span>
        <span className="shrink-0 text-ui-sm text-muted">Answered by {by}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="flex flex-col gap-1 py-1 pl-5 text-ui">
          <p className="whitespace-pre-wrap text-muted">{question.body}</p>
          <p className="whitespace-pre-wrap">{question.answer}</p>
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

export function SwarmQuestions({
  questions,
  canAnswer,
  accountableID,
  client,
  onAnswered,
}: {
  questions: MissionQuestion[]
  canAnswer: boolean
  accountableID: string
  client: Api
  onAnswered: () => void
}) {
  const accountable = useMemberName(accountableID)
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  useHeaderPrimary(canAnswer && questions.some((question) => !question.answered_at))
  if (questions.length === 0) return null
  const waiting = questions.some((question) => !question.answered_at || drafts[question.id])
  const title = waiting ? 'Questions for you' : 'Questions from the integrator'
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <SectionLabel as="h2">{title}</SectionLabel>
      {questions.map((question) =>
        question.answered_at && !drafts[question.id] ? (
          <AnsweredQuestion key={question.id} question={question} />
        ) : (
          <OpenQuestion
            key={question.id}
            question={question}
            draft={drafts[question.id] ?? ''}
            setDraft={(draft) => setDrafts((current) => ({ ...current, [question.id]: draft }))}
            canAnswer={canAnswer}
            accountable={accountable}
            client={client}
            onAnswered={onAnswered}
          />
        ),
      )}
    </section>
  )
}

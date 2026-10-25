import { useEffect, useState } from 'react'
import { DecisionButtons } from '@/components/decision-buttons'
import { Button } from '@/components/ui/button'
import { RequestCard } from '@/components/ui/request-card'
import { Textarea } from '@/components/ui/textarea'
import { memberName, resolveMember } from '@/lib/member-name'
import { inputHint, inputTitle } from '@/lib/run-requests'
import type { SessionRequest } from '@/lib/session-types'
import type { RoomMessage } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunNavigation } from '@/routes/run/header'
import type { RunRoom } from '@/routes/run/room'
import { SessionRequestCard } from '@/routes/run/session-requests'
import { useStore } from '@/store'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'
import type { RunRecord } from '@/store/runs'

export const requestCardID = {
  input: (id: string) => `request-input-${id}`,
  steer: (id: string) => `request-steer-${id}`,
  question: (id: string) => `request-question-${id}`,
}

const emptyMessages: RoomMessage[] = []
const emptyRequests: SessionRequest[] = []

function secondsUntil(at: string | undefined): number {
  return at ? Math.max(0, Math.ceil((Date.parse(at) - Date.now()) / 1000)) : 0
}

export function DeliveryCountdown({ deliverAfter }: { deliverAfter?: string }) {
  const [remaining, setRemaining] = useState(() => secondsUntil(deliverAfter))
  useEffect(() => {
    setRemaining(secondsUntil(deliverAfter))
    if (secondsUntil(deliverAfter) === 0) return
    const timer = window.setInterval(() => {
      const next = secondsUntil(deliverAfter)
      setRemaining(next)
      if (next === 0) window.clearInterval(timer)
    }, 1000)
    return () => window.clearInterval(timer)
  }, [deliverAfter])
  return <>{remaining ? `Delivers in ${remaining}s` : 'Ready to deliver'}</>
}

export function useRunRequests(run: RunRecord) {
  const room = useStore((s) => s.roomMessages[run.id] ?? emptyMessages)
  const selfID = useStore((s) => s.info?.member.id)
  const sessionRequests = useStore((s) => (run.acp ? s.acpSessions[run.id]?.pending : undefined)) ?? emptyRequests
  return {
    inputs: run.acp ? [] : run.pending_inputs ?? [],
    sessionRequests,
    steers: queuedSteers(room).filter((m) => m.actor_id !== selfID),
    questions: unansweredQuestions(room).filter((m) => m.actor_id !== run.member_id),
  }
}

function ReplyForm({ question, room }: { question: RoomMessage; room: RunRoom }) {
  const [body, setBody] = useState('')
  const [sending, setSending] = useState<object | null>(null)
  const late = usePastDeadline(sending !== null)
  const send = async () => {
    const text = body.trim()
    if (!text || sending) return
    const mine = {}
    setSending(mine)
    const posted = await room.post({ kind: 'reply', body: text, correlationID: question.id })
    setSending((current) => (current === mine ? null : current))
    if (posted) setBody((current) => (current.trim() === text ? '' : current))
  }
  return (
    <div className="flex w-full flex-col gap-2">
      <Textarea
        aria-label={`Reply to ${question.actor_display_name ?? 'the question'}`}
        rows={2}
        value={body}
        placeholder="Write a reply…"
        onChange={(event) => setBody(event.target.value)}
        onKeyDown={(event) => {
          if (!event.nativeEvent.isComposing && (event.metaKey || event.ctrlKey) && event.key === 'Enter') {
            event.preventDefault()
            void send()
          }
        }}
      />
      <div className="flex gap-2">
        <Button size="sm" disabled={!body.trim() || sending !== null} onClick={() => void send()}>
          {late ? stillWaiting : sending ? 'Replying…' : 'Reply'}
        </Button>
        {late && <Button size="sm" variant="ghost" onClick={() => setSending(null)}>Cancel</Button>}
      </div>
    </div>
  )
}

export function NeedsYouCards({ run, agent, room, nav, docked }: {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  nav: RunNavigation
  docked: boolean
}) {
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const { inputs, sessionRequests, steers, questions } = useRunRequests(run)
  const name = (id: string, snapshot?: string) => (resolveMember({ members, mergedInto }, id).member ? memberName({ members, mergedInto }, id) : snapshot ?? id)
  const openTerminal = () => {
    nav.go('terminal')
    if (!agent.localControl && !agent.controlUnavailable) agent.session.takeControl()
  }

  return (
    <div className="flex flex-col gap-2">
      {docked && sessionRequests.length > 0 ? (
        <Button variant="link" size="sm" className="self-start" onClick={nav.focusRequest}>
          {sessionRequests.length === 1 ? '1 request' : `${sessionRequests.length} requests`}, shown below the timeline
        </Button>
      ) : sessionRequests.map((request) => (
        <SessionRequestCard key={request.id} id={requestCardID.input(request.id)} run={run} request={request} agent={agent} />
      ))}
      {inputs.map((input) => (
        <RequestCard
          key={input.id}
          id={requestCardID.input(input.id)}
          title={inputTitle[input.kind]}
          actions={agent.hasAgentTerminal && <Button size="sm" variant="secondary" onClick={openTerminal}>Open terminal</Button>}
        >
          {inputHint(agent.hasAgentTerminal)}
        </RequestCard>
      ))}
      {steers.map((steer) => (
        <RequestCard
          key={steer.id}
          id={requestCardID.steer(steer.id)}
          title={`${name(steer.actor_id, steer.actor_display_name)} sent the agent a message${steer.via ? ` via ${steer.via}` : ''}`}
          meta={<DeliveryCountdown deliverAfter={steer.deliver_after} />}
          actions={agent.localControl ? (
            <DecisionButtons decide={(approve) => room.decide(steer.id, approve ? 'approve' : 'deny')} />
          ) : agent.steerable && agent.hasAgentTerminal && (
            <Button size="sm" variant="secondary" onClick={openTerminal}>Take control to decide</Button>
          )}
        >
          {steer.body}
        </RequestCard>
      ))}
      {questions.map((question) => (
        <RequestCard
          key={question.id}
          id={requestCardID.question(question.id)}
          title={`${name(question.actor_id, question.actor_display_name)} asked`}
          actions={<ReplyForm question={question} room={room} />}
        >
          {question.body}
        </RequestCard>
      ))}
    </div>
  )
}

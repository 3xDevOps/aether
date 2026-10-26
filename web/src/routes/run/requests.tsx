import { useState } from 'react'
import { DecisionButtons } from '@/components/decision-buttons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Clamped, IconTile, RequestCard, RequestTimeBar } from '@/components/ui/request-card'
import { RequestAction, RequestActions, RequestFooter } from '@/components/ui/request-choice'
import { Textarea } from '@/components/ui/textarea'
import { useSecondsUntil } from '@/lib/hooks'
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

const deliveryText = (remaining: number) => (remaining ? `Delivers in ${remaining}s` : 'Ready to deliver')

export function DeliveryCountdown({ deliverAfter }: { deliverAfter?: string }) {
  return <>{deliveryText(useSecondsUntil(deliverAfter))}</>
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
    <RequestFooter>
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
      <RequestActions>
        {late && <Button variant="ghost" onClick={() => setSending(null)}>Cancel</Button>}
        <RequestAction label={late ? stillWaiting : sending ? 'Replying…' : 'Reply'} variant="primary" disabled={!body.trim() || sending !== null} onClick={() => void send()} />
      </RequestActions>
    </RequestFooter>
  )
}

function SteerCard({ steer, name, agent, room, openTerminal }: {
  steer: RoomMessage
  name: string
  agent: AgentTerminal
  room: RunRoom
  openTerminal: () => void
}) {
  const remaining = useSecondsUntil(steer.deliver_after)
  const total = steer.deliver_after ? Date.parse(steer.deliver_after) - Date.parse(steer.created_at) : 0
  return (
    <RequestCard
      id={requestCardID.steer(steer.id)}
      avatar={<MemberAvatar id={steer.actor_id} name={name} />}
      title={`${name} sent the agent a message${steer.via ? ` via ${steer.via}` : ''}`}
      meta={deliveryText(remaining)}
      description={<Clamped className="max-h-[6lh]">{steer.body}</Clamped>}
      timeBar={remaining > 0 && total > 0 && <RequestTimeBar left={remaining * 1000} total={total} />}
      footer={(agent.localControl || (agent.steerable && agent.hasAgentTerminal)) && (
        <RequestFooter>
          <RequestActions>
            {agent.localControl
              ? <DecisionButtons decide={(approve) => room.decide(steer.id, approve ? 'approve' : 'deny')} />
              : <RequestAction label="Take control to decide" onClick={openTerminal} />}
          </RequestActions>
        </RequestFooter>
      )}
    />
  )
}

function MemberAvatar({ id, name }: { id: string; name: string }) {
  const member = useStore((s) => resolveMember(s, id).member)
  return <Avatar name={name} color={member?.color} size="header" />
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
          avatar={<IconTile><AgentGlyph agent={run.harness} /></IconTile>}
          title={inputTitle[input.kind]}
          description={inputHint(agent.hasAgentTerminal)}
          footer={agent.hasAgentTerminal && (
            <RequestFooter>
              <RequestActions><RequestAction label="Open terminal" onClick={openTerminal} /></RequestActions>
            </RequestFooter>
          )}
        />
      ))}
      {steers.map((steer) => (
        <SteerCard key={steer.id} steer={steer} name={name(steer.actor_id, steer.actor_display_name)} agent={agent} room={room} openTerminal={openTerminal} />
      ))}
      {questions.map((question) => {
        const asker = name(question.actor_id, question.actor_display_name)
        return (
          <RequestCard
            key={question.id}
            id={requestCardID.question(question.id)}
            avatar={<MemberAvatar id={question.actor_id} name={asker} />}
            title={`${asker} asked`}
            description={question.body}
            footer={<ReplyForm question={question} room={room} />}
          />
        )
      })}
    </div>
  )
}

import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { RequestCard } from '@/components/ui/request-card'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { inputHint, inputTitle } from '@/lib/run-requests'
import type { SessionRequest } from '@/lib/session-types'
import type { Approval, RoomMessage } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunNavigation } from '@/routes/run/header'
import type { RunRoom } from '@/routes/run/room'
import { SessionRequestCard } from '@/routes/run/session-requests'
import { useStore } from '@/store'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import type { RunRecord } from '@/store/runs'

export const requestCardID = {
  input: (id: string) => `request-input-${id}`,
  approval: (id: string) => `request-approval-${id}`,
  steer: (id: string) => `request-steer-${id}`,
  question: (id: string) => `request-question-${id}`,
}

const emptyMessages: RoomMessage[] = []
const emptyApprovals: Approval[] = []
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
  const approvals = useStore((s) => s.approvalsByRun[run.id] ?? emptyApprovals)
  const selfID = useStore((s) => s.info?.member.id)
  const sessionRequests = useStore((s) => (run.acp ? s.acpSessions[run.id]?.pending : undefined)) ?? emptyRequests
  return {
    inputs: run.acp ? [] : run.pending_inputs ?? [],
    sessionRequests,
    approvals,
    steers: queuedSteers(room).filter((m) => m.actor_id !== selfID),
    questions: unansweredQuestions(room).filter((m) => m.actor_id !== run.member_id),
  }
}

function ReplyForm({ question, room }: { question: RoomMessage; room: RunRoom }) {
  const [body, setBody] = useState('')
  const send = async () => {
    const text = body.trim()
    if (!text || room.busy) return
    if (await room.post({ kind: 'reply', body: text, correlationID: question.id })) setBody('')
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
      <div>
        <Button size="sm" disabled={!body.trim() || room.busy} onClick={() => void send()}>Reply</Button>
      </div>
    </div>
  )
}

export function NeedsYouCards({ run, agent, room, nav }: {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  nav: RunNavigation
}) {
  const members = useStore((s) => s.members)
  const { inputs, sessionRequests, approvals, steers, questions } = useRunRequests(run)
  const [deciding, setDeciding] = useState<string | null>(null)
  const name = (id: string, snapshot?: string) => members[id]?.display_name ?? snapshot ?? id
  const openTerminal = () => {
    nav.go('terminal')
    if (!agent.localControl && !agent.controlUnavailable) agent.session.takeControl()
  }
  const decideApproval = (approval: Approval, approve: boolean) => {
    setDeciding(approval.id)
    api.approvalDecide(run.id, approval.id, approve).then(
      (decided) => useStore.getState().decideApproval(decided.workspace_id, decided.id, decided.decision, decided.decided_by ?? '', decided.decided_at ?? ''),
      (err) => toast.error(`${approve ? 'Approve' : 'Deny'} failed: ${message(err)}`),
    ).finally(() => setDeciding(null))
  }

  return (
    <div className="flex flex-col gap-2">
      {sessionRequests.map((request) => (
        <SessionRequestCard key={request.id} id={requestCardID.input(request.id)} runID={run.id} request={request} agent={agent} />
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
      {approvals.map((approval) => (
        <RequestCard
          key={approval.id}
          id={requestCardID.approval(approval.id)}
          title={`Permission: ${approval.action}`}
          actions={
            <>
              <Button size="sm" disabled={deciding === approval.id} onClick={() => decideApproval(approval, true)}>Approve</Button>
              <Button size="sm" variant="secondary" disabled={deciding === approval.id} onClick={() => decideApproval(approval, false)}>Deny</Button>
            </>
          }
        >
          {approval.detail}
        </RequestCard>
      ))}
      {steers.map((steer) => (
        <RequestCard
          key={steer.id}
          id={requestCardID.steer(steer.id)}
          title={`${name(steer.actor_id, steer.actor_display_name)} sent the agent a message`}
          meta={<DeliveryCountdown deliverAfter={steer.deliver_after} />}
          actions={agent.localControl ? (
            <>
              <Button size="sm" disabled={room.busy} onClick={() => void room.decide(steer.id, 'approve')}>Approve</Button>
              <Button size="sm" variant="secondary" disabled={room.busy} onClick={() => void room.decide(steer.id, 'deny')}>Deny</Button>
            </>
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

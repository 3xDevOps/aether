import { useRef } from 'react'
import { canReopenRun } from '@/lib/commands'
import { allowed } from '@/lib/permissions'
import { sendToAgent } from '@/lib/send-to-agent'
import type { RoomMessage } from '@/lib/types'
import { modeLabel } from '@/routes/run/agent-name'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import { composerBlock } from '@/routes/run/composer-state'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** The Session composer's rules for a message another surface sends to the run's agent. */
export function useAgentSend(run: RunRecord, agent: AgentTerminal) {
  const self = useSelf()
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const control = useImplicitControl(run, agent)
  const held = useRef(agent.roomControl)
  held.current = agent.roomControl

  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const blocked = run.switching
    ? `Switching to ${modeLabel[run.switching] ?? run.switching}…`
    : run.acp && run.mode === 'headless'
      ? 'Background runs take no input.'
      : composerBlock(run, maySteer, maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run))

  return {
    blocked,
    waits: !control.canAct,
    note: blocked ?? (control.canAct ? '' : 'Delivers in 45 s unless the controller decides sooner.'),
    /** Control that has not arrived within `implicitControlWindowMs` drops `act` unrun. */
    dispatch: (act: () => void) => (control.canAct ? control.withControl(act) : act()),
    post: (body: string): Promise<RoomMessage> => {
      const lease = held.current?.has_control && held.current.control_generation > 0
        ? { control_session_id: held.current.control_session_id, control_generation: held.current.control_generation }
        : undefined
      return sendToAgent(run, body, lease)
    },
  }
}

export function refusal(sent: RoomMessage): string {
  if (sent.state === 'uncertain') return 'Delivery uncertain: the message may have reached the agent. Check the session before sending again.'
  return `Not sent: ${sent.failure?.message ?? sent.state}.`
}

/** `what` counts what the message carried, as in "2 comments". */
export function receipt(sent: RoomMessage, what: string, agentName: string): { text: string; tone?: 'done' | 'failed' } {
  switch (sent.state) {
    case 'queued':
      return { text: `${what} queued for ${agentName}. Delivers in 45 s unless the controller decides sooner.` }
    case 'not_sent':
      return { text: `${agentName} did not get your ${what}: ${sent.failure?.message ?? 'not sent'}. The text is in the session.`, tone: 'failed' }
    case 'uncertain':
      return { text: `Your ${what} may not have reached ${agentName}. The text is in the session.`, tone: 'failed' }
    case 'denied':
      return { text: `The controller declined your ${what}. The text is in the session.`, tone: 'failed' }
    case 'cancelled':
      return { text: `Delivery of your ${what} was cancelled. The text is in the session.`, tone: 'failed' }
    default:
      return { text: `Sent ${what} to ${agentName}${sent.agent_delivery === 'queued' ? ', queued behind its current turn' : ''}.`, tone: 'done' }
  }
}

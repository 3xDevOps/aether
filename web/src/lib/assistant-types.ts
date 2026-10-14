// Wire types of assistant.* and /ws/assistant (internal/protocol/assistant.go).

import type { SessionFrame } from '@/lib/session-types'

export type AssistantState = 'setup' | 'needs_restart' | 'idle' | 'starting' | 'running' | 'failed'

export interface AssistantAgent {
  name: string
  display_name: string
  ready: boolean
  /** Why the agent is not ready, for example a missing adapter or login. */
  reason?: string
}

export interface AssistantConfirmation {
  id: string
  method: string
  summary: string
  params: unknown
  created_at: string
  expires_at: string
}

export interface AssistantStatus {
  state: AssistantState
  agent?: string
  agents: AssistantAgent[]
  error?: string
  confirmations: AssistantConfirmation[]
}

export interface AssistantStartParams {
  agent?: string
  restart_environment?: boolean
}

/** A session frame, or the whole status: sent after the ack and whenever the state or the pending confirmations change. */
export interface AssistantFrame extends SessionFrame {
  status?: AssistantStatus
}

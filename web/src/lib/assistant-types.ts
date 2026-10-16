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
  /** The call acts on another member's run, owned by `other_owner`: it asks even with auto-approval on. */
  other_member?: boolean
  other_owner?: string
}

export interface AssistantStatus {
  state: AssistantState
  agent?: string
  agents: AssistantAgent[]
  error?: string
  confirmations: AssistantConfirmation[]
  /** Confirmed tools run without a card, as if approved. */
  auto_approve: boolean
  /** The member approved enough cards by hand that the panel may suggest auto-approval. */
  suggest_auto_approve: boolean
}

export interface AssistantSettings {
  auto_approve: boolean
  suggest_auto_approve: boolean
  /** The suggestion will not show again. */
  suggestion_off: boolean
}

/** Fields left out are not changed; none reads the settings. */
export interface AssistantSettingsParams {
  auto_approve?: boolean
  /** Answers the suggestion: `later` snoozes it, `never` stops it. */
  suggestion?: 'later' | 'never'
}

export interface AssistantStartParams {
  agent?: string
  restart_environment?: boolean
}

/** A session frame, or the whole status: sent after the ack and whenever the state or the pending confirmations change. */
export interface AssistantFrame extends SessionFrame {
  status?: AssistantStatus
}

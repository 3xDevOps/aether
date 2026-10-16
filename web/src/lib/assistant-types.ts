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
  /** Confirmed tools run without a card, as if approved. */
  auto_approve: boolean
}

export interface AssistantSettings {
  auto_approve: boolean
}

/** Fields left out are not changed; none reads the settings. */
export interface AssistantSettingsParams {
  auto_approve?: boolean
}

export interface AssistantStartParams {
  agent?: string
  restart_environment?: boolean
}

/** A session frame, or the whole status: sent after the ack and whenever the state or the pending confirmations change. */
export interface AssistantFrame extends SessionFrame {
  status?: AssistantStatus
}

/** One page of the dashboard: ids and view names only, never titles or content. */
export interface ViewEntry {
  name: string
  workspace_id?: string
  run_id?: string
  mission_id?: string
  subagent?: string
  view?: string
  path?: string
  at?: string
}

/** What `assistant.prompt` and `assistant.view` carry as `view_context`. */
export interface ViewContext {
  version: 1
  captured_at: string
  route: ViewEntry
  open_file?: { kind: 'workspace' | 'home'; path: string; run_id?: string; workspace_id?: string }
  layout?: Record<string, boolean>
  recent?: ViewEntry[]
  pending?: {
    approvals: number
    assistant_notices: number
    needs_you?: { run_id: string; reason?: string }[]
  }
}

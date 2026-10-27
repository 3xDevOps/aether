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
  /** Prompts carry the member's view context. */
  share_view: boolean
}

export interface AssistantSettings {
  auto_approve: boolean
  suggest_auto_approve: boolean
  /** The suggestion will not show again. */
  suggestion_off: boolean
  share_view: boolean
}

/** Fields left out are not changed; none reads the settings. */
export interface AssistantSettingsParams {
  auto_approve?: boolean
  /** Answers the suggestion: `later` snoozes it, `never` stops it. */
  suggestion?: 'later' | 'never'
  share_view?: boolean
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
  at?: string
}

export type ViewAttachmentKind = 'workspace' | 'run' | 'mission' | 'pr' | 'check' | 'failure' | 'task'

/** One thing a prompt is about, by id: never a title, log or file contents. */
export interface ViewAttachment {
  kind: ViewAttachmentKind
  workspace_id?: string
  run_id?: string
  mission_id?: string
  task_id?: string
  attempt_id?: string
  pr?: number
  /** `owner/name` of the pull request. */
  repository?: string
  /** A CI check's name. */
  check?: string
  /** A word for the failure, such as `adapter`. */
  failure?: string
  /** The transcript item that reported the failure. */
  seq?: number
}

/** What `assistant.prompt` and `assistant.view` carry as `view_context`. A prompt whose page the member removed has no route. */
export interface ViewContext {
  version: 1
  captured_at: string
  route?: ViewEntry
  open_file?: { kind: 'workspace' | 'home'; path: string; run_id?: string; workspace_id?: string }
  recent?: ViewEntry[]
  attachments?: ViewAttachment[]
}

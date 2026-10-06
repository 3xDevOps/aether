// Wire types of an enhanced run's agent session: the item log the
// /ws/acp/{run} stream and the run.acp.* methods carry (internal/acphost,
// internal/protocol/acp.go). Clients skip item kinds they do not know.

export type SessionItemKind =
  | 'message'
  | 'thought'
  | 'tool_call'
  | 'plan'
  | 'request'
  | 'mode_change'
  | 'config_options'
  | 'commands'
  | 'usage'
  | 'auth_status'
  | 'session_info'
  | 'notice'
  | 'turn_start'
  | 'turn_end'
  | 'reset'
  | 'unknown'

/** One segment of a user or assistant message or a thought; segments with
 * the same message_id append, and the last has complete set. */
export interface SessionMessage {
  role: 'user' | 'assistant' | 'thought'
  message_id: string
  text: string
  attachments?: SessionContent[]
  complete?: boolean
}

export interface SessionContent {
  type: string
  text?: string
  mime_type?: string
  uri?: string
  terminal_id?: string
}

/** A tool call snapshot: it replaces the previous one with the same id,
 * except output, which appends. */
export interface SessionToolCall {
  id: string
  title: string
  tool_kind?: string
  status?: string
  locations?: { path: string; line?: number }[]
  content?: SessionContent[]
  diffs?: { path: string; patch: string }[]
  raw_input?: unknown
  raw_output?: unknown
  output?: string
  output_bytes?: number
  exit_code?: number
}

export interface SessionOption {
  id: string
  name: string
  /** allow_once | allow_always | reject_once | reject_always, or empty. */
  kind?: string
}

/** Something the agent waits on a person for; logged when opened and again
 * when answered or cancelled. */
export interface SessionRequest {
  id: string
  kind: 'permission' | 'question' | 'link'
  title: string
  tool_call_id?: string
  options?: SessionOption[]
  schema?: unknown
  url?: string
  status: 'pending' | 'answered' | 'cancelled'
  answer?: string
}

export interface SessionItem {
  seq: number
  epoch: number
  time: string
  turn: number
  kind: SessionItemKind
  /** The log itself holds a cut-down item. */
  truncated?: boolean
  message?: SessionMessage
  tool_call?: SessionToolCall
  plan?: { content: string; priority?: string; status?: string }[]
  request?: SessionRequest
  mode?: string
  config_options?: unknown
  commands?: unknown
  usage?: { used: number; size: number; cost?: number; currency?: string }
  auth?: unknown
  title?: string
  notice?: { severity: string; title: string; description?: string }
  stop_reason?: string
  raw?: unknown
}

/** One /ws/acp line: an item, or a reset after which the client drops what
 * it holds. truncated means the item was cut to fit the wire; run.acp.item
 * returns it whole. */
export interface SessionFrame {
  seq?: number
  item?: SessionItem
  truncated?: boolean
  reset?: boolean
  epoch?: number
}

/** Snapshot of a live session sent in the /ws/acp ack. */
export interface SessionState {
  turn_in_flight: boolean
  queued: number
  pending: SessionRequest[]
  last_activity: string
  mode?: string
  config_options?: unknown
  commands?: unknown
  auth?: unknown
}

/** The /ws/acp header; write asks for the run's control lease. */
export interface SessionStreamRequest {
  after_seq: number
  write?: boolean
  control_session_id?: string
  control_generation?: number
  takeover?: boolean
  release_control?: boolean
}

export interface SessionStreamAck {
  ok: boolean
  seq: number
  replay: number
  epoch: number
  live: boolean
  state?: SessionState
  has_control: boolean
  control_session_id?: string
  control_generation?: number
  code?: number
  error?: string
}

/** The control lease proof run.input.answer, run.acp.cancel and
 * run.acp.set_option carry. */
export interface SessionLease {
  control_session_id: string
  control_generation: number
}

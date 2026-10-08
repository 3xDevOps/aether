// Wire types of /ws/acp/{run} and run.acp.* (internal/protocol/acp.go).
// Clients skip item kinds they do not know.

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
  config_options?: ConfigOption[]
  commands?: SessionCommand[]
  usage?: { used: number; size: number; cost?: number; currency?: string }
  auth?: AuthStatusUpdate
  title?: string
  notice?: { severity: string; title: string; description?: string }
  stop_reason?: string
  raw?: unknown
}

/** After a reset the client drops what it holds. A truncated item was cut to
 * fit the wire; run.acp.item returns it whole. */
export interface SessionFrame {
  seq?: number
  item?: SessionItem
  truncated?: boolean
  reset?: boolean
  epoch?: number
}

export interface SessionHistory {
  frames: SessionFrame[]
  oldest_seq?: number
  truncated_before?: boolean
}

/** Snapshot of a live session sent in the /ws/acp ack. */
export interface SessionState {
  turn_in_flight: boolean
  queued: number
  pending: SessionRequest[]
  last_activity: string
  mode?: string
  config_options?: ConfigOption[]
  commands?: SessionCommand[]
  auth?: AuthStatusUpdate
  steering?: boolean
  prompt_images: boolean
  auth_methods?: AuthMethod[]
}

export interface ConfigOption {
  id: string
  name: string
  description?: string
  category?: string
  type: 'select' | 'boolean' | string
  currentValue?: string | boolean
  options?: (ConfigValue | { group: string; name: string; options: ConfigValue[] })[]
}

export interface ConfigValue {
  value: string
  name: string
  description?: string
}

export interface SessionCommand {
  name: string
  description?: string
  input?: { hint?: string } | null
}

export interface AuthMethod {
  id: string
  name: string
  description?: string
  type?: string
  args?: string[]
}

export interface AuthStatusUpdate {
  authStatus?: { kind?: string; label?: string; detail?: string }
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
  oldest_seq?: number
  truncated_before?: boolean
  epoch: number
  live: boolean
  state?: SessionState
  has_control: boolean
  control_session_id?: string
  control_generation?: number
  code?: number
  error?: string
}

/** Carried by run.input.answer, run.acp.cancel and run.acp.set_option. */
export interface SessionLease {
  control_session_id: string
  control_generation: number
}

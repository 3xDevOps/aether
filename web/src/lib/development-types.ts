// JSON contracts from internal/protocol/development.go and development_stream.go.

/** Human API requests always select a run; agent sockets supply it themselves. */
export interface DevRunParams {
  run_id: string
}

export interface DevCapability {
  available: boolean
  reason?: string
}

export interface DevControlFence {
  control_session_id: string
  control_generation: number
}

export interface DevSurface {
  kind: 'terminal' | 'browser'
  id: string
  incarnation: string
}

export interface DevControlStatusParams extends DevRunParams {
  surface: DevSurface
}

export interface DevController extends DevControlFence {
  kind: 'member' | 'run_agent'
  member_id?: string
  run_id?: string
  connected: boolean
  acquired_at: string
  expires_at?: string
}

export interface DevControlStatusResult {
  surface: DevSurface
  controller: DevController | null
}

export interface DevControlAcquireParams extends DevControlStatusParams {
  control_session_id: string
  expected_generation?: number
  takeover?: boolean
}

export interface DevControlAcquireResult extends DevControlStatusResult {
  displaced?: DevController
}

export interface DevControlReleaseParams extends DevControlStatusParams, DevControlFence {}

export interface DevControlReleaseResult {
  released: boolean
}

export interface DevOutputCursor {
  epoch: string
  sequence: number
}

export interface DevTerminalTarget extends DevRunParams {
  terminal_id: string
  incarnation: string
}

export interface DevProcessState {
  state: 'running' | 'exited' | 'stopped' | 'unavailable'
  exit_code?: number
  reason?: string
}

export interface DevTerminal {
  terminal_id: string
  incarnation: string
  name: string
  started_by: 'run_agent' | 'member'
  cols: number
  rows: number
  process: DevProcessState
}

export type DevTerminalListParams = DevRunParams

export interface DevTerminalListResult {
  terminals: DevTerminal[]
}

export interface DevTerminalStartParams extends DevRunParams {
  name?: string
  command?: string[]
  cols?: number
  rows?: number
}

export interface DevTerminalStartResult {
  terminal: DevTerminal
}

export interface DevTerminalOutputParams extends DevTerminalTarget {
  after?: DevOutputCursor
  max_bytes?: number
  format?: 'text' | 'raw'
}

export interface DevTerminalOutputResult {
  terminal: DevTerminal
  start: DevOutputCursor
  next: DevOutputCursor
  position: DevOutputCursor
  text?: string
  /** Bounded raw terminal bytes encoded as base64, not a screenshot. */
  data?: string
  missing_cursor: boolean
  truncated: boolean
  more: boolean
}

export interface DevTerminalScreenParams extends DevTerminalTarget {
  row_offset?: number
  max_cells?: number
  column_offset?: number
  expected_screen_revision?: number
}

export interface DevTerminalCell {
  text: string
  width: number
  foreground?: string
  background?: string
  bold?: boolean
  dim?: boolean
  italic?: boolean
  underline?: boolean
  underline_style?: number
  underline_color?: string
  blink?: boolean
  inverse?: boolean
  hidden?: boolean
  strikethrough?: boolean
  overline?: boolean
  protected?: boolean
}

export interface DevTerminalRow {
  text: string
  wrapped: boolean
  cells: DevTerminalCell[]
}

export interface DevTerminalCursor {
  x: number
  y: number
  visible: boolean
}

export interface DevTerminalScreenResult {
  terminal: DevTerminal
  position: DevOutputCursor
  screen_revision: number
  geometry_revision: number
  alternate: boolean
  cursor: DevTerminalCursor
  text: string
  lines: DevTerminalRow[]
  row_offset: number
  column_offset: number
  next_row?: number
  next_column?: number
  truncated: boolean
  unsupported_graphics?: string[]
  protocol_error?: string
  palette?: string[]
}

export type DevTerminalScreenshotParams = DevTerminalTarget

export interface DevTerminalScreenshotResult {
  artifact: DevArtifact
}

export interface DevTerminalMouse {
  action: 'press' | 'release' | 'move' | 'wheel'
  button?: string
  x: number
  y: number
  delta?: number
}

export interface DevTerminalInputParams extends DevTerminalTarget, DevControlFence {
  kind: 'text' | 'paste' | 'key' | 'mouse'
  text?: string
  key?: string
  modifiers?: string[]
  mouse?: DevTerminalMouse
}

export interface DevTerminalInputResult {
  accepted: boolean
}

export interface DevTerminalResizeParams extends DevTerminalTarget, DevControlFence {
  cols: number
  rows: number
}

export interface DevTerminalResizeResult {
  terminal: DevTerminal
  screen_revision: number
  geometry_revision: number
}

export interface DevTerminalWaitParams extends DevTerminalTarget {
  after_output?: DevOutputCursor
  after_screen_revision?: number
  contains?: string
  exit?: boolean
  timeout_ms: number
}

export interface DevTerminalWaitResult {
  terminal: DevTerminal
  position: DevOutputCursor
  screen_revision: number
  geometry_revision: number
  matched: boolean
  timed_out: boolean
  missing_cursor: boolean
  truncated: boolean
  protocol_error?: string
}

export interface DevTerminalStopParams extends DevTerminalTarget, DevControlFence {
  timeout_ms: number
}

export interface DevTerminalStopResult {
  terminal: DevTerminal
  stopped: boolean
  timed_out: boolean
}

/** session_id is the browser incarnation, not a control session ID. */
export interface DevBrowserTarget extends DevRunParams {
  session_id: string
}

export interface DevBrowserPageTarget extends DevBrowserTarget {
  page_id: string
  page_revision: number
}

export interface DevBrowserPage {
  session_id: string
  page_id: string
  page_revision: number
  viewport_id: string
  url: string
  title: string
  width: number
  height: number
}

export type DevBrowserStatusParams = DevRunParams

export interface DevBrowserStatusResult extends DevCapability {
  running: boolean
  state: string
  session_id?: string
  selected_page_id?: string
}

export interface DevBrowserOpenParams extends DevRunParams, DevControlFence {
  session_id?: string
  url: string
  width?: number
  height?: number
}

export interface DevBrowserOpenResult {
  page: DevBrowserPage
  control: DevControlFence
}

export type DevBrowserPagesParams = DevBrowserTarget

export interface DevBrowserPagesResult {
  pages: DevBrowserPage[]
  selected_page_id?: string
}

export interface DevBrowserNavigateParams extends DevBrowserPageTarget, DevControlFence {
  url?: string
  direction?: 'url' | 'back' | 'forward' | 'reload'
  timeout_ms: number
}

export interface DevBrowserNavigateResult {
  page: DevBrowserPage
}

export interface DevBrowserSnapshotParams extends DevBrowserPageTarget {
  max_nodes?: number
  max_chars?: number
}

export interface DevBrowserNode {
  node_id?: string
  parent_id?: string
  role?: string
  name?: string
  text?: string
  value?: string
  tag?: string
  frame_url?: string
  expanded?: string
  disabled?: boolean
  checked?: boolean
  selected?: boolean
}

export interface DevBrowserSnapshotResult {
  page: DevBrowserPage
  nodes: DevBrowserNode[]
  truncated: boolean
}

export interface DevBrowserActionParams extends DevBrowserPageTarget, DevControlFence {
  action: 'click' | 'fill' | 'select_option' | 'key' | 'scroll' | 'text' | 'pointer' | 'touch' | 'select'
  node_id?: string
  viewport_id?: string
  text?: string
  key?: string
  modifiers?: string[]
  values?: string[]
  x?: number
  y?: number
  delta_x?: number
  delta_y?: number
  button?: string
  phase?: 'down' | 'move' | 'up' | 'cancel'
  touch_id?: number
  click_count?: number
  timeout_ms?: number
}

export interface DevBrowserActionResult {
  page: DevBrowserPage
}

export interface DevBrowserScreenshotParams extends DevBrowserPageTarget {
  full_page?: boolean
}

export interface DevBrowserScreenshotResult {
  artifact: DevArtifact
}

export interface DevBrowserViewportParams extends DevBrowserPageTarget, DevControlFence {
  width: number
  height: number
}

export interface DevBrowserViewportResult {
  page: DevBrowserPage
}

export interface DevBrowserWaitParams extends DevBrowserPageTarget {
  condition: 'text' | 'visible' | 'hidden' | 'url' | 'load'
  node_id?: string
  text?: string
  timeout_ms: number
}

export interface DevBrowserWaitResult {
  page: DevBrowserPage
  matched: boolean
  timed_out: boolean
}

export interface DevBrowserConsoleParams extends DevBrowserPageTarget {
  after?: number
  limit?: number
}

export interface DevBrowserConsoleEntry {
  sequence: number
  time: string
  level: string
  text: string
  url?: string
}

export interface DevBrowserConsoleResult {
  entries: DevBrowserConsoleEntry[]
  next: number
  missing_cursor: boolean
  truncated: boolean
}

export interface DevBrowserNetworkParams extends DevBrowserPageTarget {
  after?: number
  limit?: number
}

export interface DevBrowserNetworkEntry {
  sequence: number
  time: string
  url: string
  method: string
  status?: number
  failure?: string
}

export interface DevBrowserNetworkResult {
  entries: DevBrowserNetworkEntry[]
  next: number
  missing_cursor: boolean
  truncated: boolean
}

export interface DevBrowserResetParams extends DevBrowserTarget, DevControlFence {}

export interface DevBrowserResetResult {
  session_id: string
}

export interface DevBrowserCloseParams extends DevBrowserPageTarget, DevControlFence {}

export interface DevBrowserCloseResult {
  closed: boolean
}

export interface DevArtifact {
  id: string
  path: string
  source: string
  run_id: string
  incarnation: string
  terminal_id?: string
  page_id?: string
  page_revision?: number
  screen_revision?: number
  geometry_revision?: number
  viewport_id?: string
  url?: string
  captured_at: string
  content_type: string
  bytes: number
  width: number
  height: number
  cols?: number
  rows?: number
  git_head?: string
  /** Omission means the capture's Git boundary was not observed. */
  dirty?: boolean
  truncated: boolean
}

export interface DevArtifactListParams extends DevRunParams {
  after?: string
  limit?: number
}

export interface DevArtifactListResult {
  artifacts: DevArtifact[]
  next?: string
  truncated: boolean
}

export interface DevArtifactGetParams extends DevRunParams {
  artifact_id: string
}

export interface DevArtifactGetResult {
  artifact: DevArtifact
}

export interface DevArtifactDeleteParams extends DevRunParams {
  artifact_id: string
}

export interface DevArtifactDeleteResult {
  deleted: boolean
}

export interface DevArtifactRetainParams extends DevRunParams {
  artifact_ids: string[]
  verification_notes?: string
  idempotency_key: string
}

export interface DevArtifactRetainResult {
  packet_id: string
}

export type DevBrowserStreamRequest = DevBrowserPageTarget

export interface DevArtifactDownloadRequest extends DevArtifactGetParams {
  evidence_packet_id?: string
}

export interface DevStreamResponse {
  ok: boolean
  error?: string
  code?: number
  artifact?: DevArtifact
}

export interface DevBrowserFrameMetadata {
  run_id: string
  session_id: string
  page_id: string
  page_revision: number
  viewport_id: string
  width: number
  height: number
  timestamp: string
  mime_type: string
  sequence: number
  offset_top?: number
  page_scale_factor?: number
  scroll_x?: number
  scroll_y?: number
}

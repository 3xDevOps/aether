// Terminal sockets can be /ws/attach/<run>, /ws/attach/<run>?shell=<tab>, or
// /ws/terminal?tab=<name>. One attachment per logical terminal tab, with
// jittered reconnect and a read-only mirror unless the caller asks to steer.
// Persistent dock sockets rebind callbacks when a new host adopts them.
// The contract is docs/local-gateway.md - one text header frame, one text ack,
// terminal output as binary frames, input and resizes as text control frames.

import { type ConnectionState, backoff, onWake } from '@/lib/stream'

/** JSON-RPC "permission denied": a write attach without the steer capability. */
export const codeDenied = -32001

/** JSON-RPC "conflict": a writable control lease is occupied or stale. */
export const codeConflict = -32003

/** JSON-RPC "unavailable": the run has no live PTY session. */
export const codeUnavailable = -32004

/**
 * Retries of a `codeUnavailable` refusal before it counts as final.
 * internal/sshd/attach.go calls a missing session on a run that can still
 * gain one a transient race the client's retry resolves; four retries span
 * enough `backoff()` to outlive the recovery that starts the session.
 */
const unavailableRetries = 4

/** WebSocket policy violation: the gateway's authorization watch fired. */
const policyClose = 1008

/**
 * Raw input characters per frame. The gateway reads at most 64KB per frame,
 * and JSON escaping inflates a character to six bytes at worst, so 8K raw
 * stays comfortably under the limit.
 */
const inputChunk = 8 * 1024

/**
 * The geometry a client sends when it is not the one deciding: a follower,
 * and any caller with no terminal to measure yet. The ack answers with the
 * session's own size; this is only what a session that has none yet - a
 * finished run's replay - is laid out at, where 80 columns reads a recorded
 * transcript far better than a phone's width does.
 */
export const standardGeometry = { cols: 80, rows: 24 }

interface AttachHeader {
  write?: boolean
  follow?: boolean
  resume?: boolean
  /** The PTY process incarnation whose parsed cursor is being resumed. */
  resume_id?: string
  cursor?: number
  cols: number
  rows: number
  control_session_id: string
  control_generation?: number
  takeover?: boolean
  release_control?: boolean
}

/**
 * A text frame from the server: the ack, or - once attached - the one
 * control frame the server sends, the session's new geometry and lease
 * metadata.
 */
interface AttachFrame {
  type?: string
  ok: boolean
  code?: number
  error?: string
  cols?: number
  rows?: number
  cursor?: number
  resumed?: boolean
  /** The PTY process incarnation that produced this screen/cursor. */
  resume_id?: string
  replay?: number
  control_generation?: number
  has_control?: boolean
}


export interface ControlMetadata {
  control_session_id: string
  control_generation: number
  has_control: boolean
}

export type AttachDataKind = 'replay' | 'replay-end' | 'live'

export type AttachDataResult = void | Promise<void>

export interface AttachHandlers {
  /**
   * Terminal output, tagged as a frame-sized replay or live operation. For
   * live output, call `settled` only after the current xterm generation has
   * parsed the bytes; the callback is what advances the resumable cursor.
   */
  onData?: (
    chunk: Uint8Array,
    kind: AttachDataKind,
    settled?: () => void,
  ) => AttachDataResult
  /**
   * An attach was accepted. The server replays the recent transcript
   * straight after, so the caller clears what it has rather than appending a
   * second copy of the scrollback. `size` is the geometry the ack reports -
   * the live session's, not what the header asked for - which is what a
   * client that renders the session at its own size adopts. `resumed` says
   * this attach asked to keep the screen it already had: no replay follows,
   * so clearing it would throw away the only copy.
   */
  onAttached: (
    write: boolean,
    size: { cols: number; rows: number },
    resumed?: boolean,
  ) => void
  /**
   * Runs immediately after onAttached, before the first replay frame can be
   * handled. `bytes` is the ack-declared replay length; zero means this
   * attach resumes the existing screen or has no transcript to send.
   */
  onReplayStart?: (bytes: number) => void
  /**
   * Cancel an in-flight replay without revealing its partial terminal. The
   * replacement attach will announce the next replay (or a final refusal).
   */
  onReplayAbort?: () => void
  /** Lease metadata returned with every successful attach ack. */
  onControl?: (metadata: ControlMetadata) => void
  onState: (state: ConnectionState) => void
  /**
   * The attach was refused for good; no further reconnect is attempted. The
   * JSON-RPC code comes with a refusal frame, and is absent when the gateway
   * refused by closing the socket instead.
   */
  onRefused: (message: string, code?: number) => void
  /** The member cannot steer this run. The attach continues as a mirror. */
  onWriteDenied: () => void
  /**
   * Another session took or released writable control. This is an ephemeral
   * lease loss, not a permission denial: the caller clears its writable
   * preference and reconnects as a mirror so the member can ask again.
   */
  onControlLost?: () => void
  /**
   * The session's PTY was resized by whoever does decide its size. Only a
   * follower is sent this, and only after the ack that carried the first
   * geometry; it is not an event and nothing replays it.
   */
  onGeometry?: (cols: number, rows: number) => void
  /**
   * Whether this client renders the session at the size it already is
   * rather than imposing one, read at every connect. A follower is left out
   * of the minimum the PTY is sized to, so a phone can steer a run without
   * reflowing the agent's screen for everyone watching it.
   */
  follows?: () => boolean
  /**
   * Whether a missing session on this run is worth waiting out rather than
   * reporting. True only while the run can still gain one; read at every
   * refusal, so a run that ends mid-retry stops being retried.
   */
  sessionPending?: () => boolean
  /** A terminal process exited and the gateway closed the socket normally. */
  onExit?: () => void
  /** Geometry to ask for, read at every connect. */
  geometry: () => { cols: number; rows: number }
  /** Whether the caller wants to steer, read at every connect. */
  wantsWrite: () => boolean
}

export interface Attachment {
  /** Keystrokes for the agent's terminal; dropped while not attached. */
  send: (data: string) => void
  resize: (cols: number, rows: number) => void
  /**
   * Reattach now, picking up the current write preference. `resume` asks
   * the server for no replay and keeps the screen already on-screen; it is
   * honoured only while the current attach is still live, because a
   * reattach after a drop has no idea what it missed.
   */
  reopen: (options?: {
    resume?: boolean
    takeover?: boolean
    releaseControl?: boolean
  }) => void
  /**
   * Park the transport without disposing the parsed terminal. A later
   * `resume` reuses the parsed cursor and control-session identity.
   */
  suspend: () => void
  resume: () => void
  /** Forget a permission denial after the caller observes changed authority. */
  resetWriteDenial: () => void
  /** Whether the server has said this terminal session is permanently over. */
  isEnded: () => boolean
  /** Current tab identity and server-fenced control lease metadata. */
  controlMetadata?: () => ControlMetadata
  /** Update callbacks when a persistent socket gets a new terminal host. */
  rebind: (handlers: AttachHandlers) => void
  close: () => void
}

/**
 * Writes tagged output into xterm and tracks whether terminal-generated input
 * must be dropped. Replayed scrollback can hold queries (DA, DSR, OSC colour
 * reads) that xterm answers as if the shell had just asked; the answers must
 * not reach the PTY. Each replay chunk gets a write completion, and the
 * replay-end completion covers the final chunk and every reply the replay
 * provoked.
 */
export function replayGate(
  write: (chunk: Uint8Array, done?: () => void) => void,
  onReplaying?: (replaying: boolean) => void,
) {
  let muted = false
  // A reopen mid-replay leaves callbacks pending in xterm's write queue; the
  // generation lets them expire instead of unmuting the replay that replaced
  // them. It also guards the two-frame reveal below.
  let generation = 0
  let pendingReveal: number | null = null
  const nextFrame = (callback: () => void) => {
    if (typeof requestAnimationFrame === 'function') {
      requestAnimationFrame(() => callback())
    } else {
      // jsdom and non-visual embeds have no RAF. Keep the same turn-based
      // delay there rather than making a replay callback crash the attach.
      setTimeout(callback, 0)
    }
  }
  const reveal = (current: number) => {
    if (generation !== current || pendingReveal !== current) return
    nextFrame(() => {
      if (generation !== current || pendingReveal !== current) return
      nextFrame(() => {
        if (generation !== current || pendingReveal !== current) return
        pendingReveal = null
        onReplaying?.(false)
      })
    })
  }
  const start = () => {
    muted = true
    generation++
    pendingReveal = null
    onReplaying?.(true)
  }
  const unmute = () => {
    muted = false
    generation++
    pendingReveal = null
    onReplaying?.(false)
  }
  const cancel = () => {
    muted = true
    generation++
    pendingReveal = null
    onReplaying?.(true)
  }
  return {
    muted: () => muted,
    /** Begin a replay before its first binary frame is delivered. */
    start,
    unmute,
    cancel,
    write: (
      chunk: Uint8Array,
      kind: AttachDataKind,
      settled?: () => void,
    ): void | Promise<void> => {
      if (kind === 'live') {
        write(chunk, settled)
        return
      }

      muted = true
      const current = generation
      return new Promise<void>((resolve, reject) => {
        let completed = false
        const done = () => {
          if (completed) return
          completed = true
          if (kind === 'replay-end' && generation === current) {
            muted = false
            pendingReveal = current
            reveal(current)
          }
          resolve()
        }
        try {
          write(chunk, done)
        } catch (error) {
          completed = true
          reject(error)
        }
      })
    },
  }
}

/** Connect to a terminal socket, re-reading its URL before every reconnect. */
export function connectAttach(socketURL: () => string, h: AttachHandlers): Attachment {
  // A socket can outlive the component that currently displays it (a dock
  // collapse or route change keeps the server-side shell alive). Rebinding
  // keeps callbacks pointed at the current terminal instead of a disposed
  // component from the first mount.
  let handlers = h
  let socket: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let attempt = 0
  let unavailableTries = 0
  let waitingForSession = false
  // The missing-session budget is its own count: an ordinary reconnect must
  // neither spend it nor be slowed by it.
  let disposed = false
  let refused = false
  // The run's terminal session is over and this attachment is parked. A
  // reconnect could only replay the same finished transcript, so nothing -
  // a dropped socket or a foreground return - may reopen it; only an
  // explicit reopen() does.
  let ended = false
  let suspended = false
  let resumePending = false
  // Once an in-flight replay is abandoned, the xterm screen may contain only
  // a prefix of the session. Keep it invalid until a fresh replay has parsed
  // completely; no later resume may trust that partial screen.
  let replayInvalid = false
  let attached = false
  // Sticky for the life of the attachment: once the server has said this
  // member cannot steer, every reconnect is a mirror.
  let writeDenied = false
  type ReplayOperation =
    | { type: 'data'; chunk: Uint8Array; kind: AttachDataKind; cursor?: number }
    | { type: 'geometry'; cols: number; rows: number }
  // Keep each replay operation backed by the WebSocket frame that carried it.
  // In particular, never allocate a transcript-sized buffer from ack.replay.
  let replayRemaining = 0
  let replayReady = true
  let replayIsFull = false
  let replayQueue: ReplayOperation[] = []
  let replayQueueOffset = 0
  let replayDraining = false
  let retryAfterReplay = false
  let pendingReopen:
    | {
        resume?: boolean
        takeover?: boolean
        releaseControl?: boolean
      }
    | null = null
  // A resume cursor is meaningful only for the PTY incarnation that produced
  // the retained screen. The server's nonempty ack value becomes the fence
  // for every subsequent resume request; it intentionally survives a full
  // replay fallback so the next request can target the new incarnation.
  let resumeID: string | null = null
  // The ack cursor is the boundary at which the declared replay ends. Bytes
  // received after it are accounted for independently until xterm confirms
  // that it parsed them.
  let receivedCursor = 0
  let parsedCursor = 0
  let replayCursorTarget: number | null = null
  const pendingLiveWrites = new Set<Promise<void>>()
  let reopenGeneration = 0
  let deliveryGeneration = 0
  // Stable for this logical browser tab and intentionally distinct from
  // another tab by the same member. Reconnects reuse it to resume control.
  const controlSessionID = crypto.randomUUID()
  let controlGeneration = 0
  let hasControl = false
  let drainGeneration = 0
  let drainAbort: AbortController | null = null
  const replayPending = () =>
    !replayReady || replayDraining || replayQueueOffset < replayQueue.length

  const cancelDrain = () => {
    drainAbort?.abort()
    drainAbort = null
    replayDraining = false
  }

  const clearReplay = () => {
    cancelDrain()
    drainGeneration++
    replayRemaining = 0
    replayReady = true
    replayIsFull = false
    replayCursorTarget = null
    replayQueue = []
    replayQueueOffset = 0
  }

  const waitForDrain = (completion: Promise<void>, signal: AbortSignal): Promise<boolean> => {
    if (signal.aborted) return Promise.resolve(false)
    return new Promise<boolean>((resolve, reject) => {
      let settled = false
      const finish = (value: boolean) => {
        if (settled) return
        settled = true
        signal.removeEventListener('abort', abort)
        resolve(value)
      }
      const rejectCompletion = (error: unknown) => {
        if (settled) return
        settled = true
        signal.removeEventListener('abort', abort)
        reject(error)
      }
      const abort = () => finish(false)
      signal.addEventListener('abort', abort, { once: true })
      completion.then(
        () => finish(!signal.aborted),
        (error) => rejectCompletion(error),
      )
    })
  }

  const failReplay = (error: unknown) => {
    if (refused) return
    refused = true
    attached = false
    waitingForSession = false
    retryAfterReplay = false
    pendingReopen = null
    clearReplay()
    // Stop the socket before notifying the host. This is a terminal refusal,
    // not a dropped connection that may be retried behind the callback.
    drop()
    handlers.onReplayStart?.(0)
    const detail = error instanceof Error ? error.message : String(error)
    handlers.onRefused(`terminal replay failed: ${detail}`)
    handlers.onState('offline')
  }

  const failLive = (error: unknown) => {
    if (refused) return
    refused = true
    attached = false
    waitingForSession = false
    retryAfterReplay = false

    pendingReopen = null
    clearReplay()
    drop()
    handlers.onReplayStart?.(0)
    const detail = error instanceof Error ? error.message : String(error)
    handlers.onRefused(`terminal live output failed: ${detail}`)
    handlers.onState('offline')
  }
  const advanceParsed = (target: number, generation: number) => {
    if (generation !== deliveryGeneration || disposed || refused) return
    parsedCursor = Math.max(parsedCursor, target)
  }

  const trackPendingLive = (
    chunk: Uint8Array,
    target: number,
    generation: number,
  ) => {
    const onData = handlers.onData
    if (!onData) {
      advanceParsed(target, generation)
      return
    }

    let complete!: () => void
    let callbackSettled = false
    const settledCompletion = new Promise<void>((resolve) => {
      complete = () => {
        if (callbackSettled) return
        callbackSettled = true
        resolve()
      }
    })
    let completion: AttachDataResult
    try {
      completion = onData(chunk, 'live', complete)
    } catch (error) {
      if (generation === deliveryGeneration && !disposed && !refused) failLive(error)
      return
    }
    let promise = false
    try {
      promise = !!completion && typeof completion.then === 'function'
    } catch (error) {
      if (generation === deliveryGeneration && !disposed && !refused) failLive(error)
      return
    }
    if (!promise && callbackSettled) {
      advanceParsed(target, generation)
      return
    }
    const pending = promise
      ? Promise.resolve(completion).then(
          () => advanceParsed(target, generation),
          (error) => {
            if (generation === deliveryGeneration && !disposed && !refused) failLive(error)
          },
        )
      : settledCompletion.then(() => advanceParsed(target, generation))
    const tracked = pending.then(
      () => undefined,
      () => undefined,
    )
    pendingLiveWrites.add(tracked)
    void tracked.then(() => pendingLiveWrites.delete(tracked))
  }

  // Feed each replay frame to xterm as soon as it arrives. The queue retains
  // frame-backed slices only; wire order and xterm's own completion provide
  // the backpressure instead of waiting for the declared transcript boundary.
  const drainReplay = () => {
    if (replayDraining || disposed || refused || suspended) return
    replayDraining = true
    const generation = drainGeneration
    const dataGeneration = deliveryGeneration
    const controller = new AbortController()
    drainAbort = controller
    const { signal } = controller
    const valid = () =>
      generation === drainGeneration &&
      dataGeneration === deliveryGeneration &&
      drainAbort === controller &&
      !signal.aborted &&
      !disposed &&
      !refused &&
      !suspended
    void (async () => {
      try {
        while (valid() && replayQueueOffset < replayQueue.length) {
          const operation = replayQueue[replayQueueOffset++]
          if (!valid()) return
          if (operation.type === 'geometry') {
            try {
              handlers.onGeometry?.(operation.cols, operation.rows)
            } catch (error) {
              if (valid()) failReplay(error)
              return
            }
            if (!valid()) return
            continue
          }

          if (operation.kind === 'live') {
            const onData = handlers.onData
            if (!onData) {
              advanceParsed(operation.cursor ?? receivedCursor, dataGeneration)
              continue
            }
            let settled!: () => void
            let callbackSettled = false
            const settledCompletion = new Promise<void>((resolve) => {
              settled = () => {
                if (callbackSettled) return
                callbackSettled = true
                resolve()
              }
            })
            let completion: AttachDataResult
            try {
              completion = onData(operation.chunk, operation.kind, settled)
            } catch (error) {
              if (valid()) failReplay(error)
              return
            }
            if (!valid()) return
            let promise = false
            try {
              promise = !!completion && typeof completion.then === 'function'
            } catch (error) {
              if (valid()) failReplay(error)
              return
            }
            try {
              const settledResult = promise
                ? await waitForDrain(Promise.resolve(completion), signal)
                : await waitForDrain(settledCompletion, signal)
              if (!settledResult || !valid()) return
              // Promise and callback-only handlers both settle the xterm
              // write. In the callback-only case the callback may be
              // asynchronous, so inspect callbackSettled after the await.
              if (promise || callbackSettled) {
                advanceParsed(operation.cursor ?? receivedCursor, dataGeneration)
              }
            } catch (error) {
              if (valid()) failReplay(error)
              return
            }
            continue
          }

          let completion: AttachDataResult
          try {
            completion = handlers.onData?.(operation.chunk, operation.kind)
          } catch (error) {
            if (valid()) failReplay(error)
            return
          }
          if (!valid()) return
          if (completion && typeof completion.then === 'function') {
            try {
              const settled = await waitForDrain(Promise.resolve(completion), signal)
              if (!settled || !valid()) return
            } catch (error) {
              if (valid()) failReplay(error)
              return
            }
            if (!valid()) return
          }
          if (operation.kind === 'replay-end' && operation.cursor !== undefined) {
            parsedCursor = operation.cursor
            // A fresh replay is the only operation that can make a screen
            // abandoned by an earlier replay safe to resume again.
            if (replayIsFull) replayInvalid = false
          }
        }
      } finally {
        if (drainAbort !== controller) return
        replayQueue = []
        replayQueueOffset = 0
        replayDraining = false
        drainAbort = null
        maybeCutover()
      }
    })()
  }

  /** Connect to a terminal socket, re-reading its URL before every reconnect. */
  const open = (options: {
    resume?: boolean
    takeover?: boolean
    releaseControl?: boolean
  } = {}) => {
    if (disposed || suspended) return
    // A replacement is not attached until its ack arrives. Controls sent
    // during CONNECTING must not leak onto the old or half-open transport.
    attached = false
    deliveryGeneration++
    clearReplay()
    // Never send a cursor without the server-issued PTY incarnation fence.
    // An unknown fence deliberately becomes an ordinary full attach.
    const resume = (options.resume ?? false) && resumeID !== null
    handlers.onState(attempt === 0 ? 'connecting' : 'reconnecting')
    let ws: WebSocket
    try {
      ws = new WebSocket(socketURL())
    } catch {
      retry()
      return
    }
    ws.binaryType = 'arraybuffer'
    socket = ws
    const askedWrite = handlers.wantsWrite() && !writeDenied
    const follows = handlers.follows?.() ?? false
    // The server closes a refused attach with 1008 too, so the close handler
    // needs to know whether this socket already got its answer.
    let answered = false

    ws.onopen = () => {
      if (disposed || refused || socket !== ws) return
      const { cols, rows } = handlers.geometry()
      // A mirror sends no "write" key at all; every attach still carries
      // its stable control-session identity alongside geometry.
      const header: AttachHeader = {
        cols,
        rows,
        control_session_id: controlSessionID,
      }
      if ((askedWrite || options.releaseControl) && hasControl && controlGeneration > 0) {
        header.control_generation = controlGeneration
      }
      if (options.takeover || (askedWrite && hasControl)) header.takeover = true
      if (options.releaseControl) header.release_control = true
      if (askedWrite) header.write = true
      if (follows) header.follow = true
      if (resume) {
        header.resume = true
        header.resume_id = resumeID!
        header.cursor = parsedCursor
      }
      ws.send(JSON.stringify(header))
    }
    ws.onmessage = (msg) => {
      if (disposed || refused || socket !== ws) return
      if (typeof msg.data !== 'string') {
        const chunk = new Uint8Array(msg.data as ArrayBuffer)
        if (replayRemaining > 0) {
          const replayLength = Math.min(chunk.length, replayRemaining)
          if (replayLength > 0) {
            const reachesBoundary = replayLength === replayRemaining
            replayRemaining -= replayLength
            if (replayRemaining === 0) replayReady = true
            // Tag the exact slice that consumes the declared replay before it
            // enters the queue. A straddling frame's suffix remains live.
            replayQueue.push({
              type: 'data',
              chunk: chunk.subarray(0, replayLength),
              kind: reachesBoundary ? 'replay-end' : 'replay',
              cursor: reachesBoundary ? replayCursorTarget ?? receivedCursor : undefined,
            })
          }

          // A frame can straddle the replay/live boundary. Queue its suffix
          // after the final replay slice, preserving byte order and cursor
          // accounting.
          if (replayLength < chunk.length) {
            const live = chunk.subarray(replayLength)
            receivedCursor += live.length
            replayQueue.push({
              type: 'data',
              chunk: live,
              kind: 'live',
              cursor: receivedCursor,
            })
          }
          // Parsing starts with this frame, even when more replay bytes are
          // still expected.
          drainReplay()
          return
        }

        receivedCursor += chunk.length
        const target = receivedCursor
        if (replayPending()) {
          replayQueue.push({ type: 'data', chunk, kind: 'live', cursor: target })
          drainReplay()
        } else {
          trackPendingLive(chunk, target, deliveryGeneration)
        }
        return
      }

      let ack: AttachFrame
      try {
        ack = JSON.parse(msg.data) as AttachFrame
      } catch {
        return
      }
      // Someone who does impose a size resized the session; a follower
      // redraws at it. Before the ack there is nothing to redraw.
      if (attached && ack.type === 'geometry') {
        if (ack.cols && ack.rows) {
          const geometry: ReplayOperation = {
            type: 'geometry',
            cols: ack.cols,
            rows: ack.rows,
          }
          if (replayPending()) {
            replayQueue.push(geometry)
            drainReplay()
          } else {
            handlers.onGeometry?.(ack.cols, ack.rows)
          }
        }
        return
      }

      if (ack.ok) {
        const replayBytes = ack.replay === undefined ? 0 : ack.replay
        if (
          typeof replayBytes !== 'number' ||
          !Number.isFinite(replayBytes) ||
          !Number.isSafeInteger(replayBytes) ||
          replayBytes < 0
        ) {
          answered = true
          refused = true
          attached = false
          clearReplay()
          handlers.onReplayStart?.(0)
          handlers.onRefused(`invalid replay length: ${String(ack.replay)}`)
          handlers.onState('offline')
          drop()
          return
        }
        // Keep only a nonempty server-issued incarnation ID. A legacy or
        // malformed ack leaves the last known fence untouched; when no fence
        // is known, a later resume request is downgraded to a full attach.
        if (typeof ack.resume_id === 'string' && ack.resume_id.length > 0) {
          resumeID = ack.resume_id
        }
        replayRemaining = replayBytes
        replayReady = replayBytes === 0
        replayIsFull = !resume || ack.resumed !== true
        replayQueueOffset = 0
        replayQueue = []
        receivedCursor = ack.cursor ?? 0
        replayCursorTarget = replayBytes > 0 ? receivedCursor : null
        if (replayBytes === 0) parsedCursor = receivedCursor
        hasControl = ack.has_control === true
        if (ack.control_generation !== undefined) {
          controlGeneration = ack.control_generation
        }
        handlers.onControl?.({
          control_session_id: controlSessionID,
          control_generation: controlGeneration,
          has_control: hasControl,
        })
        attached = true
        attempt = 0
        unavailableTries = 0
        waitingForSession = false
        handlers.onState('live')
        handlers.onAttached(
          askedWrite,
          {
            cols: ack.cols ?? standardGeometry.cols,
            rows: ack.rows ?? standardGeometry.rows,
          },
          resume && ack.resumed === true,
        )
        if (disposed || refused || socket !== ws) return
        // Keep this immediately after the callback: WebSocket events are
        // serialized, so replayGate is muted before the first binary frame.
        handlers.onReplayStart?.(replayBytes)
        if (disposed || refused || socket !== ws) return
        if (replayReady) {
          if (replayIsFull) replayInvalid = false
          maybeCutover()
        }
        return
      }
      answered = true
      // A refused write is not a dead attach: drop the request and mirror.
      if (ack.code === codeDenied && askedWrite) {
        writeDenied = true
        hasControl = false
        handlers.onControl?.({
          control_session_id: controlSessionID,
          control_generation: controlGeneration,
          has_control: false,
        })
        handlers.onWriteDenied()
        attempt = 0
        return
      }
      // An occupied or stale writable lease is an ephemeral control loss, not
      // a permanent permission decision. This also covers a raced forced
      // reconnect whose generation was already displaced. Clear the caller's
      // write preference and reconnect as a mirror so it can still observe
      // the terminal.
      if (ack.code === codeConflict && askedWrite) {
        hasControl = false
        handlers.onControl?.({
          control_session_id: controlSessionID,
          control_generation: controlGeneration,
          has_control: false,
        })
        handlers.onControlLost?.()
        attempt = 0
        return
      }
      // Leaving `refused` unset is the whole retry: the gateway closes 1008
      // behind every refusal frame, and onclose reconnects for any close it
      // was not told to give up on.
      if (
        ack.code === codeUnavailable &&
        unavailableTries < unavailableRetries &&
        handlers.sessionPending?.()
      ) {
        unavailableTries++
        waitingForSession = true
        return
      }
      refused = true
      waitingForSession = false
      clearReplay()
      handlers.onReplayStart?.(0)
      handlers.onRefused(ack.error ?? 'attach refused', ack.code)
      handlers.onState('offline')
    }
    ws.onclose = (ev) => {
      if (disposed || socket !== ws) return
      socket = null
      // A close before the replay boundary abandons the hidden transaction.
      // Once every replay byte has arrived, let its parser completion finish
      // before cutting over; that completed replay is still safe to retain.
      const replayAborted = !replayReady
      if (replayAborted) {
        replayInvalid = true
        handlers.onReplayAbort?.()
        clearReplay()
      }
      const replayBusy = replayPending()
      // asked for after this point would be resuming nothing.
      const wasAttached = attached
      attached = false
      const controlTakenOver =
        ev.code === policyClose && !answered && wasAttached && ev.reason === 'control taken over'
      const steerWithdrawn =
        ev.code === policyClose && !answered && wasAttached && ev.reason === 'steer permission withdrawn'
      if (controlTakenOver || steerWithdrawn) {
        hasControl = false
        handlers.onControl?.({
          control_session_id: controlSessionID,
          control_generation: controlGeneration,
          has_control: false,
        })
        if (controlTakenOver) {
          // This loss is ephemeral: clear the caller's write preference but
          // leave the permanent permission-denied latch untouched.
          handlers.onControlLost?.()
        } else {
          writeDenied = true
          handlers.onWriteDenied()
        }
        attempt = 0
        if (pendingReopen) {
          // The lease is already gone, so neither a release nor a takeover
          // from the old generation belongs on the replacement. Keep the
          // deferred cutover behind the parser, but make it an ordinary
          // read-only mirror reconnect.
          pendingReopen = { resume: pendingReopen.resume }
          if (!replayBusy) maybeCutover()
          return
        }
        if (replayBusy) {
          retryAfterReplay = true
          handlers.onState('reconnecting')
        } else {
          retry()
        }
        return
      }
      // A requested cutover still owns the next attach. If the socket drops
      // before the replay reaches xterm, the incomplete parser was settled
      // above; if one is draining, its completion calls maybeCutover.
      if (pendingReopen) {
        if (!replayBusy) maybeCutover()
        return
      }
      // 1000 is the terminal process ending. A caller that owns tab
      // lifecycle (the shell dock) takes over; everyone else who gets the
      // gateway's named "session ended" close - the agent exited, or a
      // replay of a finished run's transcript drained - stays put, since a
      // reconnect could only replay the same bytes again. Any other close
      // reconnects and gets the server's refusal message, as before.
      if (ev.code === 1000 && handlers.onExit) {
        refused = true
        handlers.onExit()
        handlers.onState('offline')
        return
      }
      if (wasAttached && ev.reason === 'session ended') {
        ended = true
        if (replayAborted) handlers.onReplayStart?.(0)
        handlers.onState('offline')
        return
      }
      if (ev.code === policyClose && !answered) {
        refused = true
        clearReplay()
        handlers.onReplayStart?.(0)
        handlers.onRefused(ev.reason || 'the gateway refused the attach')
        handlers.onState('offline')
        return
      }
      if (replayBusy) {
        retryAfterReplay = true
        handlers.onState('reconnecting')
        return
      }
      retry()
    }
  }

  const retry = () => {
    if (disposed || refused || suspended || resumePending) return
    handlers.onState(!waitingForSession && attempt > 3 ? 'offline' : 'reconnecting')
    timer = setTimeout(open, backoff(attempt))
    attempt++
  }

  // Detach the handlers first: a close we asked for must not schedule its own
  // reconnect on top of the one we are about to make.
  const drop = () => {
    const ws = socket
    socket = null
    if (!ws) return
    ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null
    ws.close()
  }

  const reopenNow = (
    options: {
      resume?: boolean
      takeover?: boolean
      releaseControl?: boolean
    } = {},
    resumeAttached = attached,
  ) => {
    reopenGeneration++
    // Only a live attach can be resumed: after a drop the screen has moved
    // on without this client, and only a replay can say how. An aborted
    // replay also makes the existing screen unusable until a fresh one lands.
    suspended = false
    resumePending = false
    startWake()
    const request = {
      ...options,
      resume: (options.resume ?? false) && resumeAttached && !replayInvalid,
    }
    clearTimeout(timer!)
    timer = null
    retryAfterReplay = false
    refused = false
    ended = false
    attempt = 0
    unavailableTries = 0
    waitingForSession = false
    attached = false
    drop()
    open(request)
  }

  const reopenAfterLiveWrites = (options: {
    resume?: boolean
    takeover?: boolean
    releaseControl?: boolean
  }) => {
    const resumeAttached = attached
    // Freeze the old transport before waiting. Its handlers are detached by
    // drop(), so bytes arriving from it cannot race the parsed cursor.
    attached = false
    drop()
    const pending = [...pendingLiveWrites]
    if (pending.length === 0) {
      reopenNow(options, resumeAttached)
      return
    }

    suspended = false
    resumePending = true
    startWake()
    clearTimeout(timer!)
    timer = null
    retryAfterReplay = false
    refused = false
    ended = false
    attempt = 0
    unavailableTries = 0
    waitingForSession = false
    const generation = ++reopenGeneration
    Promise.all(pending).then(() => {
      if (
        disposed ||
        suspended ||
        ended ||
        refused ||
        !resumePending ||
        generation !== reopenGeneration
      ) {
        return
      }
      reopenNow(options, resumeAttached)
    })
  }

  const maybeCutover = () => {
    if (suspended || resumePending || replayPending()) return
    if (pendingReopen) {
      const request = pendingReopen
      pendingReopen = null
      retryAfterReplay = false
      reopenNow(request)
      return
    }
    if (retryAfterReplay) {
      retryAfterReplay = false
      retry()
    }
  }

  const control = (frame: object) => {
    if (!attached || !socket) return
    socket.send(JSON.stringify(frame))
  }

  // A phone that was in a pocket comes back to a dead socket and a frozen
  // retry timer. An attach the gateway refused, and one parked on a finished
  // session, are answers rather than failures: neither event re-asks them.
  const wake = (kind: 'visible' | 'online') => {
    if (disposed || refused || ended || suspended || resumePending) return
    // A tolerated missing-session refusal is a deliberate wait, and its
    // budget is four `backoff()` waits because that is what outlives
    // recovery starting the PTY. A wake reopens for free, so without this a
    // burst of app switches would spend all four in a second and report the
    // wait as a failure inside a second. The freeze only delays that reconnect;
    // a run with no session has nothing to show sooner anyway.
    if (waitingForSession) return
    clearTimeout(timer!)
    timer = null
    attempt = 0
    if (socket && kind === 'visible') return
    if (replayPending()) {
      // Never reveal a partial or boundary-complete replay while replacing a
      // socket. Cancel its drain, keep the current host hidden, and let the
      // new ack announce one fresh full replay.
      replayInvalid = true
      handlers.onReplayAbort?.()
      clearReplay()
      drop()
      retryAfterReplay = false
      open()
      return
    }
    // A foreground return leaves any existing socket alone; the tab being
    // hidden said nothing about the network. `online` did: every socket the
    // old network carried is suspect, attached ones most of all, because a
    // switch leaves them half open with no close ever arriving. The event is
    // rare enough that one re-attach and its replay is the cheaper mistake.
    drop()
    open()
  }
  let stopWake = onWake(wake)
  const startWake = () => {
    stopWake()
    stopWake = onWake(wake)
  }
  open()
  return {
    rebind: (next) => {
      if (disposed) return
      // A dock remount replaces the xterm host. Keep the replacement hidden
      // while canceling any old drain, then begin one fresh full replay.
      handlers = next
      replayInvalid = true
      handlers.onReplayAbort?.()
      clearReplay()
      pendingReopen = null
      retryAfterReplay = false
      refused = false
      ended = false
      resumePending = false
      reopenGeneration++
      attached = false
      waitingForSession = false
      attempt = 0
      clearTimeout(timer!)
      timer = null
      drop()
      open()
    },
    // A paste arrives as one string that can dwarf the gateway's 64KB frame
    send: (data) => {
      for (let at = 0; at < data.length; ) {
        let end = Math.min(at + inputChunk, data.length)
        // Never split a surrogate pair across frames.
        const last = data.charCodeAt(end - 1)
        if (end < data.length && last >= 0xd800 && last < 0xdc00) end--
        control({ type: 'input', data: data.slice(at, end) })
        at = end
      }
    },
    resize: (cols, rows) => control({ type: 'resize', cols, rows }),
    controlMetadata: () => ({
      control_session_id: controlSessionID,
      control_generation: controlGeneration,
      has_control: hasControl,
    }),
    resetWriteDenial: () => {
      writeDenied = false
    },
    isEnded: () => ended,
    reopen: (options) => {
      if (disposed) return
      const request = { ...(options ?? {}) }
      // Do not cut over while the current replay is still arriving or its
      // ordered writes and controls are draining. The old socket and cursor
      // stay authoritative until the parser transaction settles.
      if (replayPending()) {
        pendingReopen = request
        return
      }
      if (request.resume && pendingLiveWrites.size > 0) {
        reopenAfterLiveWrites(request)
        return
      }
      reopenNow(request)
    },
    suspend: () => {
      if (disposed || suspended || ended) return
      const incompleteReplay = replayPending()
      reopenGeneration++
      suspended = true
      resumePending = false
      pendingReopen = null
      retryAfterReplay = false
      clearTimeout(timer!)
      timer = null
      stopWake()
      stopWake = () => {}
      if (incompleteReplay) {
        replayInvalid = true
        handlers.onReplayAbort?.()
        clearReplay()
      }
      attached = false
      drop()
    },
    resume: () => {
      if (disposed || !suspended || ended) return
      suspended = false
      resumePending = true
      const generation = ++reopenGeneration
      // A screen invalidated by an abandoned replay must get a fresh full
      // replay. Keep the latch set until that replay's parser completion.
      const fullReplay = replayInvalid
      const pending = [...pendingLiveWrites]
      Promise.all(pending).then(() => {
        if (
          disposed ||
          suspended ||
          ended ||
          refused ||
          !resumePending ||
          generation !== reopenGeneration
        ) {
          return
        }
        resumePending = false
        startWake()
        attempt = 0
        unavailableTries = 0
        waitingForSession = false
        open({ resume: !fullReplay })
      })
    },
    close: () => {
      disposed = true
      reopenGeneration++
      clearReplay()
      stopWake()
      stopWake = () => {}
      clearTimeout(timer!)
      timer = null
      drop()
    },
  }
}

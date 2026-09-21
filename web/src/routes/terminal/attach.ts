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

export type TerminalEpoch = string
export type TerminalSequence = string

/** One indivisible terminal incarnation and byte high-water mark. */
export interface TerminalPosition {
  readonly epoch: TerminalEpoch
  readonly sequence: TerminalSequence
}

const maxTerminalSequence = 18_446_744_073_709_551_615n

const decodeTerminalSequence = (cursor: number | string | undefined): TerminalSequence | null => {
  if (cursor === undefined) return '0'
  if (typeof cursor === 'number') {
    if (!Number.isSafeInteger(cursor) || cursor < 0) return null
    return String(cursor)
  }
  if (!/^[0-9]+$/.test(cursor)) return null
  const sequence = BigInt(cursor)
  if (sequence > maxTerminalSequence) return null
  return sequence.toString()
}

const positionFromFrame = (frame: Pick<AttachFrame, 'resume_id' | 'cursor'>): TerminalPosition | null => {
  if (typeof frame.resume_id !== 'string' || frame.resume_id.length === 0) return null
  const sequence = decodeTerminalSequence(frame.cursor)
  return sequence === null ? null : { epoch: frame.resume_id, sequence }
}

const advancePosition = (position: TerminalPosition | null, bytes: number): TerminalPosition | null => {
  if (position === null) return null
  const sequence = BigInt(position.sequence) + BigInt(bytes)
  if (sequence > maxTerminalSequence) return null
  return { epoch: position.epoch, sequence: sequence.toString() }
}

interface AttachHeader {
  write?: boolean
  screen?: boolean
  interactive?: boolean
  follow?: boolean
  resume?: boolean
  /** The PTY process incarnation whose parsed cursor is being resumed. */
  resume_id?: string
  cursor?: TerminalSequence
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
  request_id?: number
  code?: number
  error?: string
  cols?: number
  rows?: number
  cursor?: number | string
  resumed?: boolean
  /** The PTY process incarnation that produced this screen/cursor. */
  resume_id?: string
  replay?: number
  control_session_id?: string
  control_generation?: number
  has_control?: boolean
}



export interface ControlMetadata {
  control_session_id: string
  control_generation: number
  has_control: boolean
  /** Server-issued terminal high-water carried atomically by this state. */
  position?: TerminalPosition
}
export interface ControlResult extends ControlMetadata {
  ok: boolean
  request_id?: number
  code?: number
  error?: string
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
   * this attach asked to keep the screen it already had.
   */
  onAttached: (
    write: boolean,
    size: { cols: number; rows: number },
    resumed?: boolean,
  ) => void
  /**
   * Runs immediately after onAttached, before the first replay frame can be
   * handled. The second argument distinguishes a resumed delta from a reset
   * bootstrap, allowing a caller to mute input without hiding a warm screen.
   */
  onReplayStart?: (bytes: number, full: boolean) => void
  /** Cancel an in-flight replay without revealing its partial terminal. */
  onReplayAbort?: (full: boolean) => void
  /** Lease metadata returned with every successful attach ack. */
  onControl?: (metadata: ControlMetadata) => void
  /** A control request result, or an out-of-band lease revocation. */
  onControlResult?: (result: ControlResult) => void
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
   * lease loss, not a permission denial.
   */
  onControlLost?: () => void
  /** A geometry update from the session; replay draining awaits its reflow. */
  onGeometry?: (cols: number, rows: number) => AttachDataResult
  /** Whether this client follows the session geometry. */
  follows?: () => boolean
  /** Whether a missing session is worth waiting out. */
  sessionPending?: () => boolean
  /** A terminal process exited and the gateway closed the socket normally. */
  onExit?: () => void
  /** Geometry to ask for, read at every connect. */
  geometry: () => { cols: number; rows: number }
  /** Whether this client wants initial writable control. */
  wantsWrite: () => boolean
  /** Whether this browser attachment brings a compact screen snapshot. */
  screen?: () => boolean
  /** Whether this attachment accepts framed input/control records. */
  interactive?: () => boolean
}

export interface Attachment {
  /** Keystrokes for the agent's terminal; dropped while not attached. */
  send: (data: string) => void
  resize: (cols: number, rows: number) => void
  /** Request a control lease on the existing stream. */
  setControl: (write: boolean, takeover?: boolean) => void
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
export type ReplayMode = 'full' | 'delta'

export function replayGate(
  write: (chunk: Uint8Array, done?: () => void) => void,
  onReplaying?: (replaying: boolean, full?: boolean) => void,
) {
  let muted = false
  let generation = 0
  let pendingReveal: number | null = null
  let mode: ReplayMode = 'full'
  const nextFrame = (callback: () => void) => {
    if (typeof requestAnimationFrame === 'function') {
      requestAnimationFrame(() => callback())
    } else {
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
        onReplaying?.(false, true)
      })
    })
  }
  const start = (nextMode: ReplayMode = 'full') => {
    muted = true
    mode = nextMode
    generation++
    pendingReveal = null
    // A resumed delta mutes terminal replies and user input while preserving
    // the already-valid screen.
    onReplaying?.(true, nextMode === 'full')
  }
  const unmute = () => {
    muted = false
    generation++
    pendingReveal = null
    onReplaying?.(false, mode === 'full')
  }
  const cancel = (full = true) => {
    muted = true
    mode = full ? 'full' : 'delta'
    generation++
    pendingReveal = null
    onReplaying?.(true, full)
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
            if (mode === 'full') {
              pendingReveal = current
              reveal(current)
            } else {
              onReplaying?.(false, false)
            }
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
    | { type: 'data'; chunk: Uint8Array; kind: AttachDataKind; position?: TerminalPosition }
    | { type: 'geometry'; cols: number; rows: number }
  // Keep each replay operation backed by the WebSocket frame that carried it.
  // In particular, never allocate a transcript-sized buffer from ack.replay.
  let replayRemaining = 0
  let replayReady = true
  const queuedOutputLimit = 4 * 1024 * 1024
  let queuedOutputBytes = 0
  let queueOverflowed = false
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
  // Resume state is one object so an epoch can never be retained while its
  // sequence is replaced by a different server observation.
  let receivedPosition: TerminalPosition | null = null
  let pendingLiveBytes = 0
  let parsedPosition: TerminalPosition | null = null
  let replayPositionTarget: TerminalPosition | null = null
  let pendingServerPosition: TerminalPosition | null = null
  const pendingLiveWrites = new Set<Promise<void>>()
  let reopenGeneration = 0
  let deliveryGeneration = 0
  let liveOverflowed = false
  // Stable for this logical browser tab and intentionally distinct from
  // another tab by the same member. Reconnects reuse it to resume control.
  const controlSessionID = crypto.randomUUID()
  let controlGeneration = 0
  let hasControl = false
  let controlRequestID = 0
  let controlRevision = 0
  let controlPosition: TerminalPosition | null = null
  const pendingControls = new Map<
    number,
    { generation: number; observedGeneration: number; revision: number; write: boolean; fresh: boolean }
  >()
  const applyPendingServerPosition = () => {
    if (pendingServerPosition === null || replayPending() || pendingLiveWrites.size > 0) return
    parsedPosition = pendingServerPosition
    pendingServerPosition = null
  }
  const adoptServerPosition = (position: TerminalPosition) => {
    receivedPosition = position
    pendingServerPosition = position
    applyPendingServerPosition()
  }
  const publishControl = (
    generation: number,
    granted: boolean,
    position: TerminalPosition | null | undefined = undefined,
  ) => {
    controlGeneration = generation
    hasControl = granted
    controlRevision++
    if (position !== undefined) controlPosition = position
    const metadata: ControlMetadata = {
      control_session_id: controlSessionID,
      control_generation: controlGeneration,
      has_control: hasControl,
    }
    if (controlPosition !== null) metadata.position = controlPosition
    handlers.onControl?.(metadata)
  }
  const receiveControl = (frame: AttachFrame) => {
    if (frame.control_session_id && frame.control_session_id !== controlSessionID) return
    const requestID = frame.request_id
    const pending = requestID === undefined ? undefined : pendingControls.get(requestID)
    const expectedGeneration = pending?.generation ?? (requestID === undefined ? controlGeneration : undefined)
    if (requestID !== undefined && !pending) return
    const freshZero =
      pending?.fresh === true &&
      frame.control_generation === 0 &&
      pending.revision === controlRevision &&
      pending.observedGeneration === controlGeneration
    if (
      frame.control_generation !== undefined &&
      ((frame.control_generation < controlGeneration && !freshZero) ||
        (expectedGeneration !== undefined && frame.control_generation < expectedGeneration && !freshZero))
    ) {
      if (requestID !== undefined) pendingControls.delete(requestID)
      return
    }
    if (requestID !== undefined) pendingControls.delete(requestID)
    const generation = frame.control_generation ?? controlGeneration
    const currentFenceNegative =
      requestID === undefined &&
      frame.ok !== true &&
      frame.control_generation !== undefined &&
      frame.control_generation === controlGeneration
    const foreignGenerationNegative =
      frame.ok !== true &&
      frame.control_generation !== undefined &&
      frame.control_generation > controlGeneration
    const granted =
      frame.has_control ??
      (currentFenceNegative || foreignGenerationNegative ||
      (pending?.write === false && frame.ok === true) ? false : hasControl)
    const result: ControlResult = {
      request_id: requestID,
      ok: frame.ok === true,
      code: frame.code,
      error: frame.error,
      control_session_id: frame.control_session_id ?? controlSessionID,
      control_generation: generation,
      has_control: granted,
    }
    const framePosition = positionFromFrame(frame)
    if (framePosition !== null) adoptServerPosition(framePosition)
    const resultPosition = framePosition ?? controlPosition
    if (resultPosition !== null) result.position = resultPosition
    if (pending?.write === false && result.ok && !result.has_control) {
      // A released lease no longer needs its old fence. The next acquisition
      // starts from the server's unoccupied generation.
      result.control_generation = 0
      publishControl(0, false, framePosition ?? undefined)
    } else if (frame.has_control !== undefined || frame.control_generation !== undefined) {
      publishControl(result.control_generation, result.has_control, framePosition ?? undefined)
    }
    handlers.onControlResult?.(result)
  }
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
    queuedOutputBytes = 0
    queueOverflowed = false
    cancelDrain()
    drainGeneration++
    replayRemaining = 0
    replayReady = true
    replayIsFull = false
    replayPositionTarget = null
    replayQueue = []
    replayQueueOffset = 0
  }
  const enqueueReplay = (operation: ReplayOperation): boolean => {
    if (operation.type === 'data') {
      queuedOutputBytes += operation.chunk.length
      if (handlers.screen?.() === true && queuedOutputBytes > queuedOutputLimit) {
        queueOverflowed = true
        return false
      }
    }
    replayQueue.push(operation)
    return true
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
    handlers.onReplayStart?.(0, true)
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
    handlers.onReplayStart?.(0, true)
    const detail = error instanceof Error ? error.message : String(error)
    handlers.onRefused(`terminal live output failed: ${detail}`)
    handlers.onState('offline')
  }
  const advanceParsed = (target: TerminalPosition | null, generation: number) => {
    if (generation !== deliveryGeneration || disposed || refused || target === null) return
    if (parsedPosition?.epoch === target.epoch && BigInt(parsedPosition.sequence) > BigInt(target.sequence)) return
    parsedPosition = target
  }

  const trackPendingLive = (
    chunk: Uint8Array,
    target: TerminalPosition | null,
    generation: number,
  ) => {
    if (liveOverflowed) return
    const screen = handlers.screen?.() === true
    if (screen && (chunk.length > queuedOutputLimit || pendingLiveBytes + chunk.length > queuedOutputLimit)) {
      overflowLive()
      return
    }
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
    pendingLiveBytes += chunk.length
    pendingLiveWrites.add(tracked)
    void tracked.then(() => {
      pendingLiveWrites.delete(tracked)
      pendingLiveBytes = Math.max(0, pendingLiveBytes - chunk.length)
      applyPendingServerPosition()
    })
    if (screen && pendingLiveBytes >= queuedOutputLimit) overflowLive()
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
          if (operation.type === 'data') {
            queuedOutputBytes = Math.max(0, queuedOutputBytes - operation.chunk.length)
          }
          if (!valid()) return
          if (operation.type === 'geometry') {
            try {
              const completion = handlers.onGeometry?.(operation.cols, operation.rows)
              if (completion && typeof completion.then === 'function') {
                const settled = await waitForDrain(Promise.resolve(completion), signal)
                if (!settled || !valid()) return
              }
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
              advanceParsed(operation.position ?? null, dataGeneration)
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
                advanceParsed(operation.position ?? null, dataGeneration)
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
          if (operation.kind === 'replay-end') {
            if (operation.position !== undefined) parsedPosition = operation.position
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
        applyPendingServerPosition()
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
    liveOverflowed = false
    deliveryGeneration++
    // Never send a sequence without the server-issued epoch fence. An unknown
    // position deliberately becomes an ordinary full attach.
    const resumePosition = (options.resume ?? false) ? parsedPosition : null
    const resume = resumePosition !== null
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
    const screen = handlers.screen?.()
    const interactive = handlers.interactive?.()
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
      if (screen !== undefined) header.screen = screen
      if (interactive !== undefined) header.interactive = interactive
      if ((askedWrite || options.releaseControl) && controlGeneration > 0 && (hasControl || options.takeover)) {
        header.control_generation = controlGeneration
      }
      if (options.takeover || (askedWrite && hasControl)) header.takeover = true
      if (options.releaseControl) header.release_control = true
      if (askedWrite) header.write = true
      if (follows) header.follow = true
      if (resumePosition !== null) {
        header.resume = true
        header.resume_id = resumePosition.epoch
        header.cursor = resumePosition.sequence
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
            if (!enqueueReplay({
              type: 'data',
              chunk: chunk.subarray(0, replayLength),
              kind: reachesBoundary ? 'replay-end' : 'replay',
              position: reachesBoundary ? replayPositionTarget ?? undefined : undefined,
            })) {
              overflowReplay()
              return
            }
          }

          // A frame can straddle the replay/live boundary. Queue its suffix
          // after the final replay slice, preserving byte order and cursor
          // accounting.
          if (replayLength < chunk.length) {
            const live = chunk.subarray(replayLength)
            receivedPosition = advancePosition(receivedPosition, live.length)
            if (!enqueueReplay({
              type: 'data',
              chunk: live,
              kind: 'live',
              position: receivedPosition ?? undefined,
            })) {
              overflowReplay()
              return
            }
          }
          // Parsing starts with this frame, even when more replay bytes are
          // still expected.
          drainReplay()
          return
        }

        receivedPosition = advancePosition(receivedPosition, chunk.length)
        const target = receivedPosition
        if (replayPending()) {
          if (!enqueueReplay({ type: 'data', chunk, kind: 'live', position: target ?? undefined })) {
            overflowReplay()
            return
          }
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
      const framePosition = positionFromFrame(ack)
      if (ack.type === 'control') {
        receiveControl(ack)
        return
      }
      // Someone who does impose a size resized the session; a follower
      // redraws at it. Before the ack there is nothing to redraw.
      if (attached && ack.type === 'geometry') {
        if (framePosition !== null) adoptServerPosition(framePosition)
        if (ack.cols && ack.rows) {
          const geometry: ReplayOperation = {
            type: 'geometry',
            cols: ack.cols,
            rows: ack.rows,
          }
          if (replayPending()) {
            if (!enqueueReplay(geometry)) {
              overflowReplay()
              return
            }
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
          handlers.onReplayStart?.(0, true)
          handlers.onRefused(`invalid replay length: ${String(ack.replay)}`)
          handlers.onState('offline')
          drop()
          return
        }
        const highWater = positionFromFrame(ack)
        replayRemaining = replayBytes
        replayReady = replayBytes === 0
        replayIsFull = !resume || ack.resumed !== true
        replayQueueOffset = 0
        replayQueue = []
        // Replace the whole position even when this old peer supplied no
        // epoch; retaining either component from a prior ack would be unsafe.
        receivedPosition = highWater
        parsedPosition = null
        pendingServerPosition = null
        const ackGeneration = ack.control_generation ?? (ack.has_control === false ? 0 : controlGeneration)
        publishControl(ackGeneration, ack.has_control === true, highWater)
        replayPositionTarget = replayBytes > 0 ? highWater : null
        if (replayBytes === 0) parsedPosition = highWater
        attached = true
        attempt = 0
        unavailableTries = 0
        waitingForSession = false
        handlers.onState('live')
        handlers.onAttached(
          interactive === true ? hasControl : askedWrite,
          {
            cols: ack.cols ?? standardGeometry.cols,
            rows: ack.rows ?? standardGeometry.rows,
          },
          resume && ack.resumed === true,
        )
        if (disposed || refused || socket !== ws) return
        // Keep this immediately after the callback: WebSocket events are
        // serialized, so replayGate is muted before the first binary frame.
        handlers.onReplayStart?.(replayBytes, replayIsFull)
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
        publishControl(ack.control_generation ?? controlGeneration, false, framePosition ?? undefined)
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
        publishControl(ack.control_generation ?? controlGeneration, false, framePosition ?? undefined)
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
      handlers.onReplayStart?.(0, true)
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
        handlers.onReplayAbort?.(replayIsFull)
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
        publishControl(controlGeneration, false)
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
        if (replayAborted) handlers.onReplayStart?.(0, true)
        handlers.onState('offline')
        return
      }
      if (ev.code === policyClose && !answered) {
        refused = true
        clearReplay()
        handlers.onReplayStart?.(0, true)
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
  const overflowReplay = () => {
    if (!queueOverflowed || disposed || suspended || ended || refused) return
    replayInvalid = true
    handlers.onReplayAbort?.(replayIsFull)
    clearReplay()
    retryAfterReplay = false
    drop()
    open()
  }

  const reopenAfterLiveWrites = (options: {
    resume?: boolean
    takeover?: boolean
    releaseControl?: boolean
  }) => {
    if (disposed || suspended || ended || refused) return
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

  const overflowLive = () => {
    if (liveOverflowed || disposed || suspended || ended || refused) return
    liveOverflowed = true
    // The current screen has a parsed prefix but is missing the frames that
    // arrived after the cap. Keep it invalid until a deliberate full replay.
    replayInvalid = true
    handlers.onReplayAbort?.(true)
    reopenAfterLiveWrites({ resume: false })
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
  const sendControlFrame = (frame: Record<string, unknown>) => {
    if (handlers.interactive?.() === true) frame.control_generation = controlGeneration
    control(frame)
  }
  const setControl = (write: boolean, takeover = false) => {
    if (!attached || !socket) return
    const requestID = ++controlRequestID
    // An ordinary mirror acquisition must not fence itself to the owner it
    // observed: that lease may have been released before this request
    // arrives. An explicit, user-confirmed takeover keeps that generation as
    // its precondition so a raced replacement is not silently displaced.
    const fresh = write && !hasControl && !takeover
    const generation = fresh ? 0 : controlGeneration
    pendingControls.set(requestID, {
      generation,
      observedGeneration: controlGeneration,
      revision: controlRevision,
      write,
      fresh,
    })
    const frame: Record<string, unknown> = {
      type: 'control',
      request_id: requestID,
      write,
    }
    if (takeover) frame.takeover = true
    if (generation > 0) frame.control_generation = generation
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
      handlers.onReplayAbort?.(replayIsFull)
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
      handlers.onReplayAbort?.(replayIsFull)
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
        sendControlFrame({ type: 'input', data: data.slice(at, end) })
        at = end
      }
    },
    resize: (cols, rows) => sendControlFrame({ type: 'resize', cols, rows }),
    setControl,
    controlMetadata: () => {
      const metadata: ControlMetadata = {
        control_session_id: controlSessionID,
        control_generation: controlGeneration,
        has_control: hasControl,
      }
      if (controlPosition !== null) metadata.position = controlPosition
      return metadata
    },
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
        handlers.onReplayAbort?.(replayIsFull)
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

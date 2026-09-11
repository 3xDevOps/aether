// Terminal sockets can be /ws/attach/<run>, /ws/attach/<run>?shell=<tab>, or
// /ws/terminal?tab=<name>. One attachment per logical terminal tab, with
// jittered reconnect and a read-only mirror unless the caller asks to steer.
// Persistent dock sockets rebind callbacks when a new host adopts them.
// The contract is docs/local-gateway.md - one text header frame, one text ack,
// terminal output as binary frames, input and resizes as text control frames.

import { type ConnectionState, backoff, onWake } from '@/lib/stream'

/** JSON-RPC "permission denied": a write attach without the steer capability. */
export const codeDenied = -32001

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
  cursor?: number
  cols: number
  rows: number
}

/**
 * A text frame from the server: the ack, or - once attached - the one
 * control frame the server sends, the session's new geometry.
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
  replay?: number
}

export type AttachDataKind = 'replay' | 'replay-end' | 'live'

export interface AttachHandlers {
  /** Terminal output, tagged as replay, replay-end, or live. */
  onData?: (chunk: Uint8Array, kind: AttachDataKind) => void
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
   * The session's PTY was resized by whoever does decide its size. Only a
   * follower is sent this, and only after the ack that carried the first
   * geometry; it is not an event and nothing replays it.
   */
  onGeometry?: (cols: number, rows: number) => void
  /**
   * Whether this client renders the session at the size it already is
   * rather than imposing one, read at every connect. A follower is left
   * out of the minimum the PTY is sized to, so a phone can steer a run
   * without reflowing the agent's screen for everyone watching it.
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
  reopen: (options?: { resume?: boolean }) => void
  /** Update callbacks when a persistent socket gets a new terminal host. */
  rebind: (handlers: AttachHandlers) => void
  close: () => void
}

/**
 * Writes tagged output into xterm and tracks whether terminal-generated input
 * must be dropped. Replayed scrollback can hold queries (DA, DSR, OSC colour
 * reads) that xterm answers as if the shell had just asked; the answers must
 * not reach the PTY. xterm runs write callbacks after the chunk is parsed,
 * so unmuting from the replay-end callback covers every reply the replay
 * provoked.
 */
export function replayGate(write: (chunk: Uint8Array, done?: () => void) => void) {
  let muted = false
  // A reopen mid-replay leaves the previous replay-end callback pending in
  // xterm's write queue; the generation lets it expire instead of unmuting
  // the replay that replaced it.
  let generation = 0
  return {
    muted: () => muted,
    unmute: () => {
      muted = false
      generation++
    },
    write: (chunk: Uint8Array, kind: AttachDataKind) => {
      if (kind === 'live') {
        write(chunk)
        return
      }
      muted = true
      if (kind === 'replay-end') {
        const current = generation
        write(chunk, () => {
          if (generation === current) muted = false
        })
      } else {
        write(chunk)
      }
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
  // The missing-session budget is its own count: an ordinary reconnect must
  // neither spend it nor be slowed by it.
  let unavailableTries = 0
  // A tolerated missing-session refusal reconnects like any dropped socket,
  // so without this the deliberate wait would report itself offline once
  // `attempt` had climbed past the threshold on earlier drops. Each socket
  // earns it again, so a plain drop mid-wait is still reported as one.
  let waitingForSession = false
  let disposed = false
  let refused = false
  // The run's terminal session is over and this attachment is parked. A
  // reconnect could only replay the same finished transcript, so nothing -
  // a dropped socket or a foreground return - may reopen it; only an
  // explicit reopen() does.
  let ended = false
  let attached = false
  // Sticky for the life of the attachment: once the server has said this
  // member cannot steer, every reconnect is a mirror.
  let writeDenied = false
  let replayRemaining = 0
  // How much of the session's output this client holds. The ack sets it to
  // where the replay leaves off and every live byte advances it, so a
  // reattach can ask for exactly what it missed.
  let cursor = 0

  const open = (resume = false) => {
    if (disposed) return
    attached = false
    waitingForSession = false
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
      const { cols, rows } = handlers.geometry()
      // A mirror sends no "write" key at all - the read-only header is {}
      // plus geometry.
      const header: AttachHeader = { cols, rows }
      if (askedWrite) header.write = true
      if (follows) header.follow = true
      if (resume) {
        header.resume = true
        header.cursor = cursor
      }
      ws.send(JSON.stringify(header))
    }
    ws.onmessage = (msg) => {
      if (typeof msg.data !== 'string') {
        const chunk = new Uint8Array(msg.data as ArrayBuffer)
        if (replayRemaining <= 0) {
          cursor += chunk.length
          handlers.onData?.(chunk, 'live')
          return
        }
        const replayLength = Math.min(chunk.length, replayRemaining)
        replayRemaining -= replayLength
        handlers.onData?.(
          chunk.subarray(0, replayLength),
          replayRemaining === 0 ? 'replay-end' : 'replay',
        )
        if (replayLength < chunk.length) {
          cursor += chunk.length - replayLength
          handlers.onData?.(chunk.subarray(replayLength), 'live')
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
        if (ack.cols && ack.rows) handlers.onGeometry?.(ack.cols, ack.rows)
        return
      }
      replayRemaining = ack.ok && ack.replay !== undefined && ack.replay > 0 ? ack.replay : 0
      if (ack.ok) {
        attached = true
        attempt = 0
        unavailableTries = 0
        waitingForSession = false
        cursor = ack.cursor ?? 0
        handlers.onState('live')
        // The server decides whether a resume was possible: it answers one
        // it could not serve with the whole scrollback instead, which the
        // caller has to clear its screen for.
        handlers.onAttached(
          askedWrite,
          {
            cols: ack.cols ?? standardGeometry.cols,
            rows: ack.rows ?? standardGeometry.rows,
          },
          resume && ack.resumed === true,
        )
        return
      }
      answered = true
      // A refused write is not a dead attach: drop the request and mirror.
      if (ack.code === codeDenied && askedWrite) {
        writeDenied = true
        handlers.onWriteDenied()
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
      handlers.onRefused(ack.error ?? 'attach refused', ack.code)
      handlers.onState('offline')
    }
    ws.onclose = (ev) => {
      socket = null
      replayRemaining = 0
      // This socket is no longer attached, whatever happens next: a resume
      // asked for after this point would be resuming nothing.
      const wasAttached = attached
      attached = false
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
        handlers.onState('offline')
        return
      }
      // 1008 with no refusal frame is the gateway's authorization watch,
      // and its reason names which gate fell: a lost steer capability just
      // downgrades to a mirror, while withdrawn membership refuses every
      // reconnect. An unnamed one is still refused, with that said plainly
      // rather than dressed up as a cause we did not read.
      if (ev.code === policyClose && !answered) {
        if (wasAttached && ev.reason === 'steer permission withdrawn') {
          writeDenied = true
          handlers.onWriteDenied()
          attempt = 0
          retry()
          return
        }
        refused = true
        handlers.onRefused(ev.reason || 'the gateway refused the attach')
        handlers.onState('offline')
        return
      }
      retry()
    }
  }

  const retry = () => {
    if (disposed || refused) return
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

  const control = (frame: object) => {
    if (!attached || !socket) return
    socket.send(JSON.stringify(frame))
  }

  // A phone that was in a pocket comes back to a dead socket and a frozen
  // retry timer. An attach the gateway refused, and one parked on a finished
  // session, are answers rather than failures: neither event re-asks them.
  const stopWake = onWake((kind) => {
    if (disposed || refused || ended) return
    // A tolerated missing-session refusal is a deliberate wait, and its
    // budget is four `backoff()` waits because that is what outlives
    // recovery starting the PTY. A wake reopens for free, so without this a
    // burst of app switches would spend all four in a second and report the
    // wait as a failure. The freeze only delays that reconnect; a run with
    // no session has nothing to show sooner anyway.
    if (waitingForSession) return
    if (timer) clearTimeout(timer)
    timer = null
    attempt = 0
    // A foreground return leaves any existing socket alone; the tab being
    // hidden said nothing about the network. `online` did: every socket the
    // old network carried is suspect, attached ones most of all, because a
    // switch leaves them half open with no close ever arriving. The event is
    // rare enough that one re-attach and its replay is the cheaper mistake.
    if (socket && kind === 'visible') return
    drop()
    open()
  })

  open()

  return {
    rebind: (next) => {
      if (!disposed) handlers = next
    },
    // A paste arrives as one string that can dwarf the gateway's 64KB frame
    // limit, so large input goes out as several ordered frames.
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
    reopen: (options) => {
      if (disposed) return
      // Only a live attach can be resumed: after a drop the screen has
      // moved on without this client, and only a replay can say how.
      const resume = (options?.resume ?? false) && attached
      if (timer) clearTimeout(timer)
      timer = null
      refused = false
      ended = false
      attempt = 0
      unavailableTries = 0
      waitingForSession = false
      drop()
      open(resume)
    },
    close: () => {
      disposed = true
      stopWake()
      if (timer) clearTimeout(timer)
      drop()
    },
  }
}

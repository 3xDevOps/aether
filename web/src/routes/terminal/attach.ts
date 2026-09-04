// Terminal sockets can be /ws/attach/<run>, /ws/attach/<run>?shell=<tab>, or
// /ws/terminal?tab=<name>. One attach per mounted terminal, with jittered
// reconnect and a read-only mirror unless the caller asks to steer. The
// contract is docs/local-gateway.md - one text header frame, one text ack,
// terminal output as binary frames, input and resizes as text control frames.

import { type ConnectionState, backoff } from '@/lib/stream'

/** JSON-RPC "permission denied": a write attach without the steer capability. */
export const codeDenied = -32001

/** WebSocket policy violation: the gateway's authorization watch fired. */
const policyClose = 1008

/** The gateway's dead-token refusal message (internal/dashboard/auth.go). */
const deadToken = 'dashboard token revoked or expired'

/**
 * Raw input characters per frame. The gateway reads at most 64KB per frame,
 * and JSON escaping inflates a character to six bytes at worst, so 8K raw
 * stays comfortably under the limit.
 */
const inputChunk = 8 * 1024

interface AttachHeader {
  write?: boolean
  cols: number
  rows: number
}

interface AttachAck {
  ok: boolean
  code?: number
  error?: string
  cols?: number
  rows?: number
}

export interface AttachHandlers {
  /** Terminal output, in arrival order. */
  onData?: (chunk: Uint8Array) => void
  /**
   * A fresh attach was accepted. The server replays the recent transcript
   * straight after, so the caller clears what it has rather than appending a
   * second copy of the scrollback.
   */
  onAttached: (write: boolean) => void
  onState: (state: ConnectionState) => void
  /** The attach was refused for good; no further reconnect is attempted. */
  onRefused: (message: string) => void
  /** The member cannot steer this run. The attach continues as a mirror. */
  onWriteDenied: () => void
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
  /** Reattach now, picking up the current write preference. */
  reopen: () => void
  close: () => void
}

/** Connect to a terminal socket, re-reading its URL before every reconnect. */
export function connectAttach(socketURL: () => string, h: AttachHandlers): Attachment {
  let socket: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let attempt = 0
  let disposed = false
  let refused = false
  let attached = false
  // Sticky for the life of the attachment: once the server has said this
  // member cannot steer, every reconnect is a mirror.
  let writeDenied = false

  const open = () => {
    if (disposed) return
    attached = false
    h.onState(attempt === 0 ? 'connecting' : 'reconnecting')
    let ws: WebSocket
    try {
      ws = new WebSocket(socketURL())
    } catch {
      retry()
      return
    }
    ws.binaryType = 'arraybuffer'
    socket = ws
    const askedWrite = h.wantsWrite() && !writeDenied
    // The server closes a refused attach with 1008 too, so the close handler
    // needs to know whether this socket already got its answer.
    let answered = false

    ws.onopen = () => {
      const { cols, rows } = h.geometry()
      // A mirror sends no "write" key at all - the read-only header is {}
      // plus geometry.
      const header: AttachHeader = { cols, rows }
      if (askedWrite) header.write = true
      ws.send(JSON.stringify(header))
    }
    ws.onmessage = (msg) => {
      if (typeof msg.data !== 'string') {
        h.onData?.(new Uint8Array(msg.data as ArrayBuffer))
        return
      }
      let ack: AttachAck
      try {
        ack = JSON.parse(msg.data) as AttachAck
      } catch {
        return
      }
      if (ack.ok) {
        attached = true
        attempt = 0
        h.onState('live')
        h.onAttached(askedWrite)
        return
      }
      answered = true
      // A refused write is not a dead attach: drop the request and mirror.
      // A token revoked between the handshake and the header answers with
      // the same code, but reconnecting can never revive it - the message
      // tells the two apart, and the dead token falls through to refusal.
      if (ack.code === codeDenied && askedWrite && ack.error !== deadToken) {
        writeDenied = true
        h.onWriteDenied()
        attempt = 0
        return
      }
      refused = true
      h.onRefused(ack.error ?? 'attach refused')
      h.onState('offline')
    }
    ws.onclose = (ev) => {
      socket = null
      // 1000 is the terminal process ending. A caller that owns tab
      // lifecycle (the shell dock) takes over; everyone else reconnects
      // and gets the server's refusal message, as before.
      if (ev.code === 1000 && h.onExit) {
        refused = true
        h.onExit()
        h.onState('offline')
        return
      }
      // 1008 with no refusal frame is the gateway's authorization watch.
      // After a successful attach the close reason names which gate fell:
      // a lost steer capability just downgrades to a mirror, while a dead
      // token or withdrawn membership would refuse every reconnect.
      if (ev.code === policyClose && !answered) {
        if (attached && ev.reason === 'steer permission withdrawn') {
          writeDenied = true
          h.onWriteDenied()
          attempt = 0
          retry()
          return
        }
        refused = true
        h.onRefused(attached && ev.reason ? ev.reason : deadToken)
        h.onState('offline')
        return
      }
      retry()
    }
  }

  const retry = () => {
    if (disposed || refused) return
    h.onState(attempt > 3 ? 'offline' : 'reconnecting')
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

  open()

  return {
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
    reopen: () => {
      if (disposed) return
      if (timer) clearTimeout(timer)
      timer = null
      refused = false
      attempt = 0
      drop()
      open()
    },
    close: () => {
      disposed = true
      if (timer) clearTimeout(timer)
      drop()
    },
  }
}

// The /ws/shell socket: one workspace shell per connection, framed like
// /ws/attach (routes/terminal/attach.ts) - one JSON header frame, one JSON
// ack, terminal output as binary frames, input and resizes as text control
// frames. Unlike an attach, a shell is a one-shot session: the close code is
// the outcome (1000 registered, 4001 abandoned), so there is no reconnect to
// schedule - reopening is the user's explicit resume/reset choice.

import { api } from '@/lib/api'
import type { WorkspaceShellReq } from '@/store/shell'

/**
 * Raw input characters per frame. The gateway reads at most 64KB per frame,
 * and JSON escaping inflates a character to six bytes at worst, so 8K raw
 * stays comfortably under the limit (same arithmetic as attach.ts).
 */
const inputChunk = 8 * 1024

/** The server's dirty-exit close code: the shell died without registering. */
export const dirtyClose = 4001

/** protocol.WorkspaceShellResponse, as far as the SPA reads it. */
interface ShellAck {
  ok: boolean
  code?: number
  error?: string
  cols?: number
  rows?: number
}

export interface ShellHandlers {
  /** Terminal output, in arrival order. */
  onData: (chunk: Uint8Array) => void
  /** The header was accepted; the shell is live. */
  onAttached: () => void
  /** The request was refused for good; nothing reconnects. */
  onRefused: (message: string, code?: number) => void
  /**
   * The shell ended. Clean (close 1000) means the server registered and
   * snapshotted; anything else abandoned the session, with the server's
   * close reason when it gave one.
   */
  onExit: (clean: boolean, reason?: string) => void
  /** Geometry to ask for, read at connect. */
  geometry: () => { cols: number; rows: number }
}

export interface ShellSession {
  /** Keystrokes for the shell; dropped until the ack lands. */
  send: (data: string) => void
  resize: (cols: number, rows: number) => void
  close: () => void
}

export function connectShell(req: WorkspaceShellReq, h: ShellHandlers): ShellSession {
  let socket: WebSocket | null = null
  let disposed = false
  let attached = false
  // A session settles exactly once: a refusal or an exit, never both. The
  // server closes a refused shell too, so the close handler must know the
  // answer already went out.
  let settled = false

  const settle = (deliver: () => void) => {
    if (disposed || settled) return
    settled = true
    deliver()
  }

  try {
    socket = new WebSocket(api.shellSocket())
  } catch {
    settled = true
    // After the caller has its session handle; a constructor failure is a
    // dead connection, not a server refusal.
    queueMicrotask(() => {
      if (!disposed) h.onExit(false, 'connection failed')
    })
  }

  if (socket) {
    const ws = socket
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => {
      const { cols, rows } = h.geometry()
      // The full protocol.WorkspaceShellRequest, omitempty mirrored: absent
      // keys rather than empty values.
      const header: Record<string, unknown> = {
        workspace: req.workspace,
        mode: req.mode,
        cols,
        rows,
      }
      if (req.harness) header.harness = req.harness
      if (req.tui_args?.length) header.tui_args = req.tui_args
      if (req.headless_args?.length) header.headless_args = req.headless_args
      if (req.resume) header.resume = true
      if (req.reset) header.reset = true
      ws.send(JSON.stringify(header))
    }
    ws.onmessage = (msg) => {
      if (typeof msg.data !== 'string') {
        h.onData(new Uint8Array(msg.data as ArrayBuffer))
        return
      }
      let ack: ShellAck
      try {
        ack = JSON.parse(msg.data) as ShellAck
      } catch {
        return
      }
      if (ack.ok) {
        attached = true
        h.onAttached()
        return
      }
      settle(() => h.onRefused(ack.error ?? 'shell refused', ack.code))
    }
    ws.onerror = () => ws.close()
    ws.onclose = (ev) => {
      socket = null
      if (ev.code === 1000) {
        settle(() => h.onExit(true))
        return
      }
      settle(() =>
        h.onExit(
          false,
          ev.reason ||
            (ev.code === dirtyClose
              ? 'shell exited without registering'
              : `connection closed (${ev.code})`),
        ),
      )
    }
  }

  const control = (frame: object) => {
    if (!attached || !socket) return
    socket.send(JSON.stringify(frame))
  }

  return {
    // A paste can dwarf the gateway's 64KB frame limit, so large input goes
    // out as several ordered frames, never splitting a surrogate pair.
    send: (data) => {
      for (let at = 0; at < data.length; ) {
        let end = Math.min(at + inputChunk, data.length)
        const last = data.charCodeAt(end - 1)
        if (end < data.length && last >= 0xd800 && last < 0xdc00) end--
        control({ type: 'input', data: data.slice(at, end) })
        at = end
      }
    },
    resize: (cols, rows) => control({ type: 'resize', cols, rows }),
    // Detach the handlers first: a close we asked for is not an outcome, and
    // must not report one.
    close: () => {
      disposed = true
      const ws = socket
      socket = null
      if (!ws) return
      ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null
      ws.close()
    },
  }
}

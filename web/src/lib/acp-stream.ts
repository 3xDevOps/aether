import { api } from '@/lib/api'
import type { SessionFrame, SessionStreamAck, SessionStreamRequest } from '@/lib/session-types'
import { backoff, onWake, type ConnectionState } from '@/lib/stream'
import type { TakeoverAction, TakeoverState } from '@/routes/terminal/attach'

export interface ControlFrame {
  type: 'control'
  request_id?: number
  ok?: boolean
  code?: number
  error?: string
  has_control?: boolean
  control_session_id?: string
  control_generation?: number
  revocation_reason?: 'takeover' | 'permission' | 'revoked'
}

export interface TakeoverFrame {
  type: 'takeover'
  request_id?: number
  ok?: boolean
  code?: number
  error?: string
  takeover?: TakeoverState
}

export type StreamState = ConnectionState | 'refused'

export interface SessionStreamHandlers {
  afterSeq: () => number
  lease: () => Pick<SessionStreamRequest, 'write' | 'control_session_id' | 'control_generation'>
  onAck: (ack: SessionStreamAck) => void
  onFrames: (frames: SessionFrame[]) => void
  onControl: (frame: ControlFrame) => void
  onTakeover: (frame: TakeoverFrame) => void
  onState: (state: StreamState, error?: string) => void
}

export interface SessionStream {
  control: (write: boolean, opts?: { generation?: number }) => boolean
  takeover: (action: TakeoverAction, id: string, generation?: number) => boolean
  close: () => void
}

const codeInvalidParams = -32602
const codeConflict = -32003
const resubscribe = 1012
const policy = 1008

export function connectSessionStream(runID: string, h: SessionStreamHandlers): SessionStream {
  let socket: WebSocket | null = null
  let acked = false
  let timer: ReturnType<typeof setTimeout> | null = null
  let attempt = 0
  let closed = false
  let requestID = 0
  let dropLease = false

  const parse = (data: string): unknown[] =>
    data.split('\n').filter((line) => line.trim()).map((line) => JSON.parse(line) as unknown)

  const open = () => {
    if (closed) return
    h.onState(attempt === 0 ? 'connecting' : 'reconnecting')
    let ws: WebSocket
    try {
      ws = new WebSocket(api.acpSocket(runID))
    } catch {
      retry()
      return
    }
    socket = ws
    acked = false
    ws.onopen = () => {
      const lease = h.lease()
      const header: SessionStreamRequest = dropLease
        ? { after_seq: h.afterSeq(), control_session_id: lease.control_session_id }
        : { after_seq: h.afterSeq(), ...lease }
      ws.send(JSON.stringify(header))
    }
    ws.onmessage = (msg) => {
      if (typeof msg.data !== 'string') return
      let parsed: unknown[]
      try {
        parsed = parse(msg.data)
      } catch {
        return
      }
      const frames: SessionFrame[] = []
      for (const value of parsed) {
        const frame = value as Record<string, unknown>
        if (!acked) {
          const ack = frame as unknown as SessionStreamAck
          if (!ack.ok) {
            refused(ack)
            return
          }
          acked = true
          attempt = 0
          dropLease = false
          h.onAck(ack)
          h.onState('live')
        } else if (frame.type === 'control') {
          h.onControl(frame as unknown as ControlFrame)
        } else if (frame.type === 'takeover') {
          h.onTakeover(frame as unknown as TakeoverFrame)
        } else {
          frames.push(frame as SessionFrame)
        }
      }
      if (frames.length) h.onFrames(frames)
    }
    ws.onerror = () => ws.close()
    ws.onclose = (ev) => {
      socket = null
      if (ev.code === policy) {
        stop(ev.reason || 'The server closed the session stream.')
        return
      }
      if (ev.code === resubscribe) attempt = 0
      retry()
    }
  }

  const refused = (ack: SessionStreamAck) => {
    const error = ack.error ?? 'The server refused the session stream.'
    detach()
    if (ack.code === codeInvalidParams) {
      stop(error)
      return
    }
    if (ack.code === codeConflict && h.lease().write) dropLease = true
    retry()
  }

  const stop = (error: string) => {
    closed = true
    stopWake()
    h.onState('refused', error)
  }

  const retry = () => {
    if (closed) return
    h.onState(attempt > 3 ? 'offline' : 'reconnecting')
    timer = setTimeout(open, attempt === 0 ? 0 : backoff(attempt))
    attempt++
  }

  const detach = () => {
    const ws = socket
    socket = null
    acked = false
    if (!ws) return
    ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null
    ws.close()
  }

  const stopWake = onWake((kind) => {
    if (closed) return
    if (timer) clearTimeout(timer)
    timer = null
    attempt = 0
    if (socket && kind === 'visible') return
    detach()
    open()
  })

  const send = (frame: Record<string, unknown>): boolean => {
    if (!socket || !acked) return false
    socket.send(JSON.stringify({ ...frame, request_id: ++requestID }))
    return true
  }

  open()

  return {
    control: (write, opts = {}) => {
      const generation = opts.generation ?? h.lease().control_generation
      return send({
        type: 'control',
        write,
        ...(generation ? { control_generation: generation } : {}),
      })
    },
    takeover: (action, id, generation) => send({
      type: 'takeover',
      action,
      takeover_id: id,
      ...(generation === undefined ? {} : { control_generation: generation }),
    }),
    close: () => {
      closed = true
      stopWake()
      if (timer) clearTimeout(timer)
      detach()
    },
  }
}

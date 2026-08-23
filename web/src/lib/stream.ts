// The /ws/events subscription: one socket, jittered reconnect, replay from
// the last sequence the store has seen so a reconnect loses nothing.

import { api } from '@/lib/api'
import type { Event, SubscribeRequest } from '@/lib/types'

export type ConnectionState = 'connecting' | 'live' | 'reconnecting' | 'offline'

export interface StreamHandlers {
  onEvent: (ev: Event) => void
  /** `live` fires when the server acknowledges the subscription, not when
   * the socket opens. */
  onState: (state: ConnectionState) => void
  /** The gateway's token watch closed the socket: the bearer token is dead,
   * and every reconnect would carry it. The stream has stopped for good. */
  onDead?: (reason: string) => void
  /** The gateway's subscribe refusal named the failing hop (-32004
   * "network unreachable: ..." or "server unreachable: ..."): the gateway
   * itself is fine, something past it is not. `network` means this machine
   * never got off its own network stack; `server` means it did and
   * aether-server did not answer. `detail` is the refusal message, which on
   * a stream that never goes live is the only account of the failure the
   * client ever gets. Fired before the retry is scheduled. */
  onUnreachable?: (kind: 'network' | 'server', detail: string) => void
  /** Last sequence applied to the store; replay resumes after it. */
  afterSeq: () => number
}

/** WebSocket policy violation, which the gateway uses for a dead token. */
const policyClose = 1008

/** The token watch's close reason (internal/dashboard/auth.go). The gateway
 * also closes 1008 for refused subscribes and transient membership check
 * failures, which a reconnect can outlive - only a dead token is terminal. */
const deadTokenReason = 'dashboard token revoked or expired'

/** The local gateway's code for a dead SSH hop (protocol.CodeUnavailable). */
const codeUnavailable = -32004

const baseDelayMs = 500
const maxDelayMs = 30_000

/** Jittered exponential delay, shared with the hydration retry. */
export function backoff(attempt: number): number {
  const capped = Math.min(maxDelayMs, baseDelayMs * 2 ** attempt)
  return capped * (0.5 + Math.random() / 2)
}

/** Opens the event stream and keeps it open. Returns a disposer. */
export function connectEvents(h: StreamHandlers): () => void {
  let socket: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let attempt = 0
  let closed = false

  const open = () => {
    if (closed) return
    h.onState(attempt === 0 ? 'connecting' : 'reconnecting')
    let ws: WebSocket
    try {
      ws = new WebSocket(api.eventsSocket())
    } catch {
      retry()
      return
    }
    socket = ws

    ws.onopen = () => {
      // Nothing seen yet means this is the first connect: go live rather than
      // replaying the whole log.
      const seq = h.afterSeq()
      const req: SubscribeRequest = { replay: seq > 0, after_seq: seq }
      ws.send(JSON.stringify(req))
    }
    ws.onmessage = (msg) => {
      if (typeof msg.data !== 'string') return
      try {
        const parsed = JSON.parse(msg.data) as Event & {
          ok?: boolean
          code?: number
          error?: string
        }
        if (parsed.type === undefined) {
          // The subscribe acknowledgement, not an event. Only now has the
          // server installed the subscription, so only now is nothing able to
          // slip past us: that is what `live` means to callers. A refusal
          // closes the socket on the server side and lands in onclose - but
          // the local gateway's refusal frame already names which hop died,
          // and the close reason does not survive every proxy.
          if (parsed.ok) {
            attempt = 0
            h.onState('live')
          } else if (parsed.ok === false && parsed.code === codeUnavailable) {
            const detail = parsed.error ?? ''
            if (detail.startsWith('network unreachable')) {
              h.onUnreachable?.('network', detail)
            } else if (detail.startsWith('server unreachable')) {
              h.onUnreachable?.('server', detail)
            }
          }
          return
        }
        h.onEvent(parsed)
      } catch {
        // A frame we cannot parse is not worth tearing the stream down for.
      }
    }
    ws.onerror = () => ws.close()
    ws.onclose = (ev) => {
      socket = null
      // The token watch closes 1008 with this reason when the bearer token is
      // revoked or expired; reconnecting would carry the same dead token, so
      // stop and say so instead of looping at the backoff cap forever. Every
      // other 1008 (subscribe refused, membership check hiccup) is retried.
      if (ev.code === policyClose && ev.reason === deadTokenReason) {
        closed = true
        h.onDead?.(ev.reason)
        h.onState('offline')
        return
      }
      // 4000 is the gateway dropping a client whose backlog overflowed: the
      // cure is an immediate resubscribe from our last seq, not backoff.
      if (ev.code === 4000) attempt = 0
      retry()
    }
  }

  const retry = () => {
    if (closed) return
    h.onState(attempt > 3 ? 'offline' : 'reconnecting')
    timer = setTimeout(open, backoff(attempt))
    attempt++
  }

  open()

  return () => {
    closed = true
    if (timer) clearTimeout(timer)
    socket?.close()
  }
}

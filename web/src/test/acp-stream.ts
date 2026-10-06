import { act } from '@testing-library/react'
import type { SessionItem, SessionState, SessionStreamAck } from '@/lib/session-types'
import { StubSocket } from '@/test/stub-socket'

let seq = 0
const start = Date.parse('2026-10-06T10:00:00Z')

export function resetItems(): void {
  seq = 0
}

export function item(kind: SessionItem['kind'], turn: number, fields: Partial<SessionItem> = {}): SessionItem {
  seq++
  return { seq, epoch: 0, time: new Date(start + seq * 1000).toISOString(), turn, kind, ...fields }
}

export const say = (turn: number, role: 'user' | 'assistant', text: string, id = `m${seq + 1}`) =>
  item('message', turn, { message: { role, message_id: id, text, complete: true } })

export const tool = (turn: number, id: string, kind: string, title: string, status: string, extra: Partial<NonNullable<SessionItem['tool_call']>> = {}) =>
  item('tool_call', turn, { tool_call: { id, title, tool_kind: kind, status, ...extra } })

export const state = (over: Partial<SessionState> = {}): SessionState => ({
  turn_in_flight: false,
  queued: 0,
  pending: [],
  last_activity: new Date(start).toISOString(),
  ...over,
})

export class ScriptedSession {
  constructor(readonly socket: StubSocket) {}

  static last(): ScriptedSession {
    return new ScriptedSession(StubSocket.last())
  }

  header(): Record<string, unknown> {
    return this.socket.frames()[0] as Record<string, unknown>
  }

  open(ack: Partial<SessionStreamAck> = {}, items: SessionItem[] = []): this {
    act(() => {
      this.socket.onopen?.()
      const head: SessionStreamAck = {
        ok: true,
        seq: items.at(-1)?.seq ?? 0,
        replay: items.length,
        epoch: 0,
        live: true,
        has_control: false,
        state: state(),
        ...ack,
      }
      this.socket.onmessage?.({ data: [head, ...items.map((it) => ({ seq: it.seq, item: it }))].map((f) => JSON.stringify(f)).join('\n') })
    })
    return this
  }

  send(...frames: unknown[]): this {
    act(() => {
      this.socket.onmessage?.({ data: frames.map((f) => JSON.stringify(f)).join('\n') })
    })
    return this
  }

  items(...items: SessionItem[]): this {
    return this.send(...items.map((it) => ({ seq: it.seq, item: it })))
  }

  close(code: number, reason = ''): this {
    act(() => this.socket.onclose?.({ code, reason }))
    return this
  }
}

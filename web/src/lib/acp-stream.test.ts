import { act } from '@testing-library/react'
import { connectSessionStream, type SessionStreamHandlers } from '@/lib/acp-stream'
import type { SessionFrame } from '@/lib/session-types'
import { item, resetItems, ScriptedSession } from '@/test/acp-stream'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', () => ({ api: { acpSocket: (runID: string) => `ws://localhost/ws/acp/${runID}` } }))

function handlers(over: Partial<SessionStreamHandlers> = {}) {
  let seq = 0
  const frames: SessionFrame[] = []
  const states: string[] = []
  const h: SessionStreamHandlers = {
    afterSeq: () => seq,
    lease: () => ({ control_session_id: 'tab-1' }),
    onAck: vi.fn(),
    onFrames: (batch) => {
      frames.push(...batch)
      seq = batch.at(-1)?.seq ?? seq
    },
    onControl: vi.fn(),
    onTakeover: vi.fn(),
    onState: (state) => states.push(state),
    ...over,
  }
  return { h, frames, states }
}

beforeEach(() => {
  StubSocket.install()
  resetItems()
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('the session stream client', () => {
  it('asks from the last applied seq after the server ends the stream', () => {
    const { h, frames } = handlers()
    const stream = connectSessionStream('run_1', h)
    const first = ScriptedSession.last().open({}, [item('turn_start', 1), item('message', 1)])
    expect(first.header()).toEqual({ after_seq: 0, control_session_id: 'tab-1' })
    expect(frames.map((f) => f.seq)).toEqual([1, 2])

    first.close(1012, 'session stream ended; resubscribe with after_seq')
    vi.advanceTimersByTime(0)
    expect(StubSocket.opened).toHaveLength(2)
    expect(ScriptedSession.last().open().header()).toEqual({ after_seq: 2, control_session_id: 'tab-1' })
    stream.close()
  })

  it('passes a reset frame through so the store drops what it holds', () => {
    const { h, frames } = handlers()
    connectSessionStream('run_1', h)
    ScriptedSession.last().open({ epoch: 1 }).send({ reset: true, epoch: 1 }, { seq: 1, item: item('turn_start', 1) })
    expect(frames[0]).toEqual({ reset: true, epoch: 1 })
    expect(frames[1]?.seq).toBe(1)
  })

  it('reclaims a held lease in the header and sends control frames once acknowledged', () => {
    const { h } = handlers({ lease: () => ({ write: true, control_session_id: 'tab-1', control_generation: 7 }) })
    const stream = connectSessionStream('run_1', h)
    expect(stream.control(true)).toBe(false)
    const session = ScriptedSession.last().open({ has_control: true, control_generation: 7 })
    expect(session.header()).toEqual({ after_seq: 0, write: true, control_session_id: 'tab-1', control_generation: 7 })
    expect(stream.control(false, { generation: 7 })).toBe(true)
    expect(session.socket.frames()[1]).toEqual({ type: 'control', write: false, control_generation: 7, request_id: 1 })
    session.send({ type: 'control', control_generation: 7, revocation_reason: 'takeover' })
    expect(h.onControl).toHaveBeenCalledWith({ type: 'control', control_generation: 7, revocation_reason: 'takeover' })
  })

  it('reconnects as a viewer when the held lease was taken meanwhile', () => {
    const { h } = handlers({ lease: () => ({ write: true, control_session_id: 'tab-1', control_generation: 7 }) })
    connectSessionStream('run_1', h)
    const refused = ScriptedSession.last()
    act(() => {
      refused.socket.onopen?.()
      refused.socket.onmessage?.({ data: JSON.stringify({ ok: false, code: -32003, error: 'run control is held by another session' }) })
    })
    vi.advanceTimersByTime(0)
    expect(ScriptedSession.last().open().header()).toEqual({ after_seq: 0, control_session_id: 'tab-1' })
  })

  it('stops for good when the run is not served over ACP', () => {
    const { h, states } = handlers()
    connectSessionStream('run_1', h)
    const session = ScriptedSession.last()
    act(() => {
      session.socket.onopen?.()
      session.socket.onmessage?.({ data: JSON.stringify({ ok: false, code: -32602, error: 'run run_1 does not run its agent over ACP' }) })
    })
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)
    expect(states.at(-1)).toBe('refused')
  })
})

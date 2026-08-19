import { connectEvents, type ConnectionState } from '@/lib/stream'
import type { Event } from '@/lib/types'
import { StubSocket } from '@/test/stub-socket'

let seq = 0
let states: ConnectionState[] = []
let received: Event[] = []

function open() {
  seq = 0
  states = []
  received = []
  return connectEvents({
    onEvent: (ev) => received.push(ev),
    onState: (s) => states.push(s),
    afterSeq: () => seq,
  })
}

beforeEach(() => {
  StubSocket.install()
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('connectEvents', () => {
  it('subscribes live on the first connect and streams events', () => {
    const stop = open()
    const socket = StubSocket.last()
    socket.onopen?.()

    expect(socket.frames()[0]).toEqual({ replay: false, after_seq: 0 })
    // An open socket is not yet a subscription.
    expect(states).not.toContain('live')

    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    expect(states).toContain('live')

    socket.onmessage?.({
      data: JSON.stringify({ seq: 3, type: 'run.status', run_id: 'run_1' }),
    })

    expect(received.map((e) => e.seq)).toEqual([3])
    stop()
  })

  it('replays from the last applied seq after a dropped socket', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    seq = 12

    StubSocket.last().onclose?.({ code: 1006 })
    expect(StubSocket.opened).toHaveLength(1) // backs off, does not hammer

    vi.advanceTimersByTime(500)
    expect(StubSocket.opened).toHaveLength(2)

    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).toEqual({ replay: true, after_seq: 12 })
    stop()
  })

  it('resubscribes after a 4000 close (backlog dropped)', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    seq = 7

    StubSocket.last().onclose?.({ code: 4000 })
    vi.advanceTimersByTime(500)

    StubSocket.last().onopen?.()
    expect(StubSocket.opened).toHaveLength(2)
    expect(StubSocket.last().frames()[0]).toEqual({ replay: true, after_seq: 7 })
    stop()
  })

  it('stops reconnecting once disposed', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    stop()

    StubSocket.last().onclose?.({ code: 1006 })
    vi.advanceTimersByTime(60_000)

    expect(StubSocket.opened).toHaveLength(1)
  })
})

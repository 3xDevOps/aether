import { connectEvents, type ConnectionState } from '@/lib/stream'
import type { Event } from '@/lib/types'
import { StubSocket } from '@/test/stub-socket'
import { fire, fireHidden } from '@/test/wake'

let seq = 0
let states: ConnectionState[] = []
let received: Event[] = []
// Every stream this file opens, closed in afterEach. A test that fails
// before its own `stop()` would otherwise leave its wake listeners on
// `document` and open a socket during the next test.
let streams: Array<() => void> = []

function open() {
  seq = 0
  states = []
  received = []
  const stop = connectEvents({
    onEvent: (ev) => received.push(ev),
    onState: (s) => states.push(s),
    afterSeq: () => seq,
  })
  streams.push(stop)
  return stop
}

beforeEach(() => {
  StubSocket.install()
  vi.useFakeTimers()
})

afterEach(() => {
  for (const stop of streams) stop()
  streams = []
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

  it.each(['visibilitychange', 'online'] as const)(
    'reopens at once on %s instead of waiting out the backoff',
    (event) => {
      const stop = open()
      StubSocket.last().onopen?.()

      // Four failed attempts put the next retry at the far end of the
      // backoff, which is where a pocketed phone comes back from.
      for (let n = 0; n < 4; n++) {
        StubSocket.last().onclose?.({ code: 1006 })
        vi.advanceTimersByTime(30_000)
      }
      const before = StubSocket.opened.length
      StubSocket.last().onclose?.({ code: 1006 })
      expect(states.at(-1)).toBe('offline')

      fire(event)

      expect(StubSocket.opened).toHaveLength(before + 1)
      expect(states.at(-1)).toBe('connecting')
      // The timer the close scheduled was cleared, not left to fire a
      // second socket on top of this one.
      vi.advanceTimersByTime(60_000)
      expect(StubSocket.opened).toHaveLength(before + 1)
      stop()
    },
  )

  it('leaves a subscribed socket alone when the tab comes back', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })

    fire('visibilitychange')
    fire('online')

    expect(StubSocket.opened).toHaveLength(1)
    stop()
  })

  it('drops a socket that never subscribed when the network returns', () => {
    const stop = open()
    StubSocket.last().onopen?.()

    // A wifi-to-cellular switch leaves the socket half open: the browser
    // still reports it as connected, and it will never be acknowledged.
    // Coming back to the foreground says nothing about the network, so only
    // `online` may throw it away.
    fire('visibilitychange')
    expect(StubSocket.opened).toHaveLength(1)

    fire('online')

    expect(StubSocket.opened).toHaveLength(2)
    expect(StubSocket.opened[0].closed).toBe(true)
    stop()
  })

  it('resets the backoff on a wake that lands before the queued close', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    for (let n = 0; n < 5; n++) {
      StubSocket.last().onclose?.({ code: 1006 })
      vi.advanceTimersByTime(30_000)
    }
    const before = StubSocket.opened.length

    // The newest socket died while the tab was frozen and the browser has
    // not delivered its close yet, so the wake finds a socket and leaves it.
    // The reset must happen anyway: otherwise the close that arrives next
    // schedules the pre-suspend backoff, which is the wait this exists to
    // remove.
    fire('visibilitychange')
    expect(StubSocket.opened).toHaveLength(before)

    StubSocket.last().onclose?.({ code: 1006 })
    vi.advanceTimersByTime(600)

    expect(StubSocket.opened).toHaveLength(before + 1)
    stop()
  })

  it('ignores the visibilitychange that backgrounds the tab', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    StubSocket.last().onclose?.({ code: 1006 })
    const before = StubSocket.opened.length

    fireHidden()

    expect(StubSocket.opened).toHaveLength(before)
    stop()
  })

  it('ignores a foreground return after disposal', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    StubSocket.last().onclose?.({ code: 1006 })
    stop()

    fire('visibilitychange')

    expect(StubSocket.opened).toHaveLength(1)
  })
})

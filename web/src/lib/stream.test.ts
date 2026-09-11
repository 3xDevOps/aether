import { connectEvents, type ConnectionState } from '@/lib/stream'
import type { Event } from '@/lib/types'
import { StubSocket } from '@/test/stub-socket'

let seq = 0
let states: ConnectionState[] = []
let received: Event[] = []

/** A foreground return or a network return, as the browser delivers it. */
function fire(event: string) {
  const target = event === 'online' ? window : document
  target.dispatchEvent(new Event(event))
}

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

  it.each(['visibilitychange', 'online'])(
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

  it('leaves a connected socket alone when the tab comes back', () => {
    const stop = open()
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })

    fire('visibilitychange')
    fire('online')

    expect(StubSocket.opened).toHaveLength(1)
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

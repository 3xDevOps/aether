import { type Attachment, codeDenied, connectAttach } from '@/routes/terminal/attach'
import type { ConnectionState } from '@/lib/stream'
import { StubSocket } from '@/test/stub-socket'

let output: string[] = []
let states: ConnectionState[] = []
let attaches = 0
let refusal: string | null = null
let denied = false
let write = false

function attach(url: string | (() => string) = '/ws/attach/run_1'): Attachment {
  output = []
  states = []
  attaches = 0
  refusal = null
  denied = false
  const socketURL = typeof url === 'function' ? url : () => url
  return connectAttach(socketURL, {
    onData: (chunk) => output.push(new TextDecoder().decode(chunk)),
    onAttached: () => {
      attaches++
    },
    onState: (s) => states.push(s),
    onRefused: (m) => {
      refusal = m
    },
    onWriteDenied: () => {
      denied = true
      write = false
    },
    geometry: () => ({ cols: 120, rows: 40 }),
    wantsWrite: () => write,
  })
}

function ack(over: Record<string, unknown> = {}) {
  StubSocket.last().onmessage?.({
    data: JSON.stringify({ ok: true, cols: 120, rows: 40, ...over }),
  })
}

beforeEach(() => {
  StubSocket.install()
  vi.useFakeTimers()
  write = false
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('connectAttach', () => {
  it('attaches as a read-only mirror and delivers the transcript replay', () => {
    const a = attach()
    const socket = StubSocket.last()
    socket.onopen?.()

    expect(socket.url).toContain('/ws/attach/run_1')
    expect(socket.frames()[0]).toEqual({ cols: 120, rows: 40 })

    ack()
    socket.onmessage?.({ data: new TextEncoder().encode('$ ls\r\n').buffer })

    expect(attaches).toBe(1)
    expect(states).toContain('live')
    expect(output.join('')).toBe('$ ls\r\n')

    // A mirror's keystrokes still reach the socket; the server drops them.
    a.send('x')
    expect(socket.frames()[1]).toEqual({ type: 'input', data: 'x' })
    a.close()
  })
  it('reads the caller URL again when reconnecting', () => {
    let tab = 'main'
    const a = attach(() => `/ws/terminal?tab=${tab}`)
    const first = StubSocket.last()
    expect(first.url).toBe('/ws/terminal?tab=main')
    first.onopen?.()
    ack()

    tab = 'logs'
    first.onclose?.({ code: 1006 })
    vi.advanceTimersByTime(1_000)

    expect(StubSocket.last().url).toBe('/ws/terminal?tab=logs')
    a.close()
  })


  it('reattaches with write once the caller asks to steer', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    write = true
    a.reopen()
    StubSocket.last().onopen?.()

    expect(StubSocket.opened).toHaveLength(2)
    expect(StubSocket.last().frames()[0]).toEqual({
      cols: 120,
      rows: 40,
      write: true,
    })
    a.close()
  })

  it('falls back to a mirror when the server refuses the write', () => {
    write = true
    const a = attach()
    StubSocket.last().onopen?.()

    StubSocket.last().onmessage?.({
      data: JSON.stringify({
        ok: false,
        code: codeDenied,
        error: 'run.attach: permission denied',
      }),
    })
    expect(denied).toBe(true)

    // The server closes a refused attach with a policy close; that is not the
    // token watch firing, so the reconnect goes ahead as a plain mirror.
    StubSocket.last().onclose?.({ code: 1008 })
    vi.advanceTimersByTime(1000)
    StubSocket.last().onopen?.()

    expect(StubSocket.last().frames()[0]).toEqual({ cols: 120, rows: 40 })
    expect(refusal).toBeNull()
    a.close()
  })

  it('stops for good when a write attach is refused for a dead token', () => {
    write = true
    const a = attach()
    StubSocket.last().onopen?.()

    // Same code as a steer denial; only the message says the token died,
    // so this must not downgrade to a mirror that retries forever.
    StubSocket.last().onmessage?.({
      data: JSON.stringify({
        ok: false,
        code: codeDenied,
        error: 'dashboard token revoked or expired',
      }),
    })
    StubSocket.last().onclose?.({ code: 1008 })
    vi.advanceTimersByTime(60_000)

    expect(StubSocket.opened).toHaveLength(1)
    expect(denied).toBe(false)
    expect(refusal).toBe('dashboard token revoked or expired')
    expect(states.at(-1)).toBe('offline')
    a.close()
  })

  it('backs off after a dropped socket and resends the geometry', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    StubSocket.last().onclose?.({ code: 1006 })
    expect(StubSocket.opened).toHaveLength(1) // waits, does not hammer
    expect(states).toContain('reconnecting')

    vi.advanceTimersByTime(500)
    expect(StubSocket.opened).toHaveLength(2)

    StubSocket.last().onopen?.()
    ack()
    expect(StubSocket.last().frames()[0]).toEqual({ cols: 120, rows: 40 })
    expect(attaches).toBe(2)
    a.close()
  })

  it('stops reconnecting when the attach itself is refused', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: false, code: -32004, error: 'no live terminal' }),
    })
    StubSocket.last().onclose?.({ code: 1008 })

    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)
    expect(refusal).toBe('no live terminal')
    expect(states.at(-1)).toBe('offline')

    // Retrying is the user's call, and it reconnects.
    a.reopen()
    expect(StubSocket.opened).toHaveLength(2)
    a.close()
  })

  it('gives up when the gateway closes a live attach on a dead token', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    // A post-attach 1008 is the authorization watch; a dead token would be
    // rejected at every handshake from here on.
    StubSocket.last().onclose?.({
      code: 1008,
      reason: 'dashboard token revoked or expired',
    })
    vi.advanceTimersByTime(60_000)

    expect(StubSocket.opened).toHaveLength(1)
    expect(refusal).toBe('dashboard token revoked or expired')
    expect(states.at(-1)).toBe('offline')
    a.close()
  })

  it('gives up with the withdrawal message when membership is revoked mid-attach', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    StubSocket.last().onclose?.({ code: 1008, reason: 'membership withdrawn' })
    vi.advanceTimersByTime(60_000)

    expect(StubSocket.opened).toHaveLength(1)
    expect(refusal).toBe('membership withdrawn')
    expect(states.at(-1)).toBe('offline')
    a.close()
  })

  it('reconnects as a mirror when steer is withdrawn mid-attach', () => {
    write = true
    const a = attach()
    StubSocket.last().onopen?.()
    ack()
    expect(attaches).toBe(1)

    StubSocket.last().onclose?.({ code: 1008, reason: 'steer permission withdrawn' })
    expect(denied).toBe(true)

    vi.advanceTimersByTime(1000)
    StubSocket.last().onopen?.()
    ack()

    expect(StubSocket.opened).toHaveLength(2)
    expect(StubSocket.last().frames()[0]).toEqual({ cols: 120, rows: 40 })
    expect(refusal).toBeNull()
    expect(attaches).toBe(2)
    a.close()
  })

  it('splits a large paste into ordered frames under the 64KB limit', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    // An emoji straddles the first chunk boundary so a naive split would
    // tear its surrogate pair apart.
    const paste = 'a'.repeat(8 * 1024 - 1) + '\u{1f600}' + 'b'.repeat(12_000)
    a.send(paste)

    const inputs = StubSocket.last()
      .frames()
      .filter((f) => (f as { type: string }).type === 'input') as { data: string }[]
    expect(inputs.length).toBeGreaterThan(1)
    expect(inputs.map((f) => f.data).join('')).toBe(paste)
    for (const raw of StubSocket.last().sent) {
      expect(new TextEncoder().encode(raw).length).toBeLessThan(64 * 1024)
    }
    a.close()
  })

  it('drops input and resizes while detached', () => {
    const a = attach()
    a.send('x')
    a.resize(80, 24)
    expect(StubSocket.last().sent).toHaveLength(0)

    StubSocket.last().onopen?.()
    ack()
    a.resize(80, 24)
    expect(StubSocket.last().frames()[1]).toEqual({
      type: 'resize',
      cols: 80,
      rows: 24,
    })
    a.close()
  })

  it('stops reconnecting once closed', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()
    a.close()

    StubSocket.last().onclose?.({ code: 1006 })
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)
  })
  it('reports a normal shell exit without reconnecting', () => {
    let exited = false
    const a = connectAttach(() => '/ws/attach/run_1?shell=t1', {
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      onExit: () => {
        exited = true
      },
      geometry: () => ({ cols: 120, rows: 40 }),
      wantsWrite: () => true,
    })

    const socket = StubSocket.last()
    socket.onopen?.()
    ack()
    socket.onclose?.({ code: 1000 })
    vi.advanceTimersByTime(60_000)

    expect(exited).toBe(true)
    expect(StubSocket.opened).toHaveLength(1)
    a.close()
  })
})

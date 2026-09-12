import { type Attachment, codeDenied, connectAttach, replayGate } from '@/routes/terminal/attach'
import type { ConnectionState } from '@/lib/stream'
import { StubSocket } from '@/test/stub-socket'
import { fire } from '@/test/wake'

let output: string[] = []
let outputKinds: Array<[string, string]> = []
let states: ConnectionState[] = []
let attaches = 0
let refusal: string | null = null
let refusalCode: number | undefined
let denied = false
let write = false
let sessionPending = false
// Every attachment this file opens, closed in afterEach. A test that fails
// before its own `close()` would otherwise leave its wake listeners on
// `document` and open a socket during the next test.
let attachments: Attachment[] = []

function attach(url: string | (() => string) = '/ws/attach/run_1'): Attachment {
  output = []
  outputKinds = []
  states = []
  attaches = 0
  refusal = null
  refusalCode = undefined
  denied = false
  const socketURL = typeof url === 'function' ? url : () => url
  const attachment = connectAttach(socketURL, {
    onData: (chunk, kind) => {
      const text = new TextDecoder().decode(chunk)
      output.push(text)
      outputKinds.push([kind, text])
    },
    onAttached: () => {
      attaches++
    },
    onState: (s) => states.push(s),
    onRefused: (m, code) => {
      refusal = m
      refusalCode = code
    },
    onWriteDenied: () => {
      denied = true
      write = false
    },
    sessionPending: () => sessionPending,
    geometry: () => ({ cols: 120, rows: 40 }),
    wantsWrite: () => write,
  })
  attachments.push(attachment)
  return attachment
}

// A missing-session refusal, plus the 1008 close the gateway sends behind
// every refusal frame and the wait for the reconnect it schedules.
function refuseMissingSession() {
  StubSocket.last().onopen?.()
  StubSocket.last().onmessage?.({
    data: JSON.stringify({ ok: false, code: -32004, error: 'ptyhost: no session for run' }),
  })
  StubSocket.last().onclose?.({ code: 1008 })
  vi.advanceTimersByTime(60_000)
}

/**
 * The gateway's missing-session refusal and the 1008 close behind it, on
 * whichever socket is open now. No timers advance, so the reconnect it
 * schedules is still pending when this returns.
 */
function refuseSession() {
  StubSocket.last().onopen?.()
  StubSocket.last().onmessage?.({
    data: JSON.stringify({ ok: false, code: -32004, error: 'ptyhost: no session for run' }),
  })
  StubSocket.last().onclose?.({ code: 1008 })
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
  sessionPending = false
})

afterEach(() => {
  for (const attachment of attachments) attachment.close()
  attachments = []
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
  it('rebinds lifecycle callbacks when a persistent socket gets a new host', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    const nextAttached = vi.fn()
    const nextOutput: string[] = []
    a.rebind({
      onData: (chunk) => nextOutput.push(new TextDecoder().decode(chunk)),
      onAttached: nextAttached,
      onState: vi.fn(),
      onRefused: vi.fn(),
      onWriteDenied: vi.fn(),
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    a.reopen()
    StubSocket.last().onopen?.()
    ack()
    StubSocket.last().onmessage?.({ data: new TextEncoder().encode('new host').buffer })

    expect(nextAttached).toHaveBeenCalledOnce()
    expect(nextOutput).toEqual(['new host'])
    a.close()
  })
  it('resumes from where the live output left it, so the gap is asked for', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack({ cursor: 100 })
    StubSocket.last().onmessage?.({ data: new TextEncoder().encode('live!').buffer })

    a.reopen({ resume: true })
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).toMatchObject({ resume: true, cursor: 105 })
    a.close()
  })

  it('clears the screen when the server could not serve the resume', () => {
    const resumedFlags: Array<boolean | undefined> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: () => {},
      onAttached: (_write, _size, resumed) => resumedFlags.push(resumed),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 120, rows: 40 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    StubSocket.last().onopen?.()
    ack()

    a.reopen({ resume: true })
    StubSocket.last().onopen?.()
    // The ring had dropped the bytes this client was missing.
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: true, cols: 120, rows: 40, resumed: false }),
    })
    expect(resumedFlags[resumedFlags.length - 1]).toBe(false)
    a.close()
  })

  it('asks for the full replay when the attach it would resume is already gone', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    // The socket dropped: whatever the session did next never reached this
    // screen, so resuming it would leave a hole only a replay can fill.
    StubSocket.last().onclose?.({ code: 1006, reason: '' } as CloseEvent)
    a.reopen({ resume: true })
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('resume')
    a.close()
  })

  it('splits replay bytes from live output at the acknowledged boundary', () => {
    const a = attach()
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 5 })

    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('defg').buffer })

    expect(outputKinds).toEqual([
      ['replay', 'abc'],
      ['replay-end', 'de'],
      ['live', 'fg'],
    ])
    a.close()
  })

  it('marks output live when an attach ack has no replay', () => {
    const a = attach()
    const socket = StubSocket.last()
    socket.onopen?.()
    ack()

    socket.onmessage?.({ data: new TextEncoder().encode('live').buffer })

    expect(outputKinds).toEqual([['live', 'live']])
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

  it('hands the refusal code to the caller with the message', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: false, code: -32004, error: 'ptyhost: no session for run' }),
    })

    expect(refusalCode).toBe(-32004)

    // A refusal the gateway delivers by closing the socket carries no code.
    a.reopen()
    StubSocket.last().onclose?.({ code: 1008 })
    expect(refusal).toBe('the gateway refused the attach')
    expect(refusalCode).toBeUndefined()
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

  // internal/sshd/attach.go refuses rather than waits while a run that can
  // still gain a session has none, and says the client's retry is what
  // resolves it: recovery starts the PTY session under a row that already
  // reads running.
  it('waits out a missing session while the run can still gain one', () => {
    sessionPending = true
    const a = attach()

    for (let n = 0; n < 4; n++) refuseMissingSession()
    expect(StubSocket.opened).toHaveLength(5)
    expect(refusal).toBeNull()
    expect(states).not.toContain('offline')

    // Past the bound the refusal is the server's answer, not a race.
    refuseMissingSession()
    expect(StubSocket.opened).toHaveLength(5)
    expect(refusal).toBe('ptyhost: no session for run')
    expect(states.at(-1)).toBe('offline')
    a.close()
  })

  it('reports a missing session as soon as the run can no longer gain one', () => {
    sessionPending = true
    const a = attach()

    refuseMissingSession()
    refuseMissingSession()
    expect(StubSocket.opened).toHaveLength(3)
    expect(refusal).toBeNull()

    // The run ended mid-retry, so the budget stops applying to it.
    sessionPending = false
    refuseMissingSession()

    expect(StubSocket.opened).toHaveLength(3)
    expect(refusal).toBe('ptyhost: no session for run')
    expect(states.at(-1)).toBe('offline')
    a.close()
  })

  it('keeps the missing-session budget clear of ordinary reconnects', () => {
    sessionPending = true
    const a = attach()

    // Dropped sockets with no successful attach in between must not spend
    // the budget a later missing session is owed.
    for (let n = 0; n < 3; n++) {
      StubSocket.last().onclose?.({ code: 1006 })
      vi.advanceTimersByTime(60_000)
    }
    expect(StubSocket.opened).toHaveLength(4)

    for (let n = 0; n < 4; n++) refuseMissingSession()
    expect(refusal).toBeNull()
    // The wait is deliberate, so nothing in it may paint a red Offline the
    // reconnect behind it clears seconds later.
    expect(states).not.toContain('offline')

    refuseMissingSession()
    expect(refusal).toBe('ptyhost: no session for run')
    a.close()
  })

  it('gives up on an unnamed policy close of a live attach', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    // A post-attach 1008 is the authorization watch. With no reason there is
    // nothing to name, so the message says only what is known.
    StubSocket.last().onclose?.({ code: 1008 })
    vi.advanceTimersByTime(60_000)

    expect(StubSocket.opened).toHaveLength(1)
    expect(refusal).toBe('the gateway refused the attach')
    expect(states.at(-1)).toBe('offline')
    a.close()
  })

  it.each(['visibilitychange', 'online'] as const)(
    'reattaches at once on %s instead of waiting out the backoff',
    (event) => {
      const a = attach()
      StubSocket.last().onopen?.()
      ack()

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
      // The timer the close scheduled was cleared, not left to open a second
      // socket on top of this one.
      vi.advanceTimersByTime(60_000)
      expect(StubSocket.opened).toHaveLength(before + 1)
      a.close()
    },
  )

  it('leaves a live attach and a refused one alone when the tab comes back', () => {
    const live = attach()
    StubSocket.last().onopen?.()
    ack()
    fire('visibilitychange')
    expect(StubSocket.opened).toHaveLength(1)
    live.close()

    const a = attach()
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: false, code: -32004, error: 'no live terminal' }),
    })
    StubSocket.last().onclose?.({ code: 1008 })
    const before = StubSocket.opened.length

    fire('visibilitychange')
    fire('online')

    // A refusal is the server's answer, not a dropped socket: coming back to
    // the foreground must not re-ask a question already answered.
    expect(StubSocket.opened).toHaveLength(before)
    a.close()
  })

  it('keeps the missing-session budget for the waits it was sized for', () => {
    sessionPending = true
    const a = attach()
    refuseSession()

    // Four app switches while the run is still provisioning. A wake reopens
    // for free, so without a guard each one would spend a try of a budget
    // sized for four backoff waits, and the deliberate wait would report
    // itself as a failure inside a second.
    for (let n = 0; n < 4; n++) {
      fire('visibilitychange')
      fire('online')
      expect(StubSocket.opened).toHaveLength(1)
    }
    expect(refusal).toBeNull()

    // The wait is delayed by the freeze, never cancelled: its own reconnect
    // still runs and the budget is whole.
    for (let n = 0; n < 3; n++) {
      vi.advanceTimersByTime(60_000)
      refuseSession()
    }
    expect(refusal).toBeNull()

    vi.advanceTimersByTime(60_000)
    refuseSession()
    expect(refusal).toBe('ptyhost: no session for run')
    a.close()
  })

  it('leaves a finished session parked until the caller reopens it', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    // The run's terminal session ended: the gateway names the close, and the
    // client parks rather than looping attach -> EOF -> reattach.
    StubSocket.last().onclose?.({ code: 1000, reason: 'session ended' })
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)

    fire('visibilitychange')
    fire('online')

    // Re-attaching would re-serve the whole finished transcript and rewrite
    // the pane, on every app switch, for output that cannot change again.
    expect(StubSocket.opened).toHaveLength(1)

    // An explicit reopen is still the way back in.
    a.reopen()
    expect(StubSocket.opened).toHaveLength(2)
    a.close()
  })

  it.each([
    ['never attached', false],
    ['already attached', true],
  ])('replaces a socket that %s when the network returns', (_when, attached) => {
    const a = attach()
    StubSocket.last().onopen?.()
    if (attached) ack()

    fire('visibilitychange')
    expect(StubSocket.opened).toHaveLength(1)

    // A wifi-to-cellular switch leaves the socket half open whatever state
    // it reached: the browser goes on reporting it as connected and no close
    // ever arrives, so `online` is the only evidence it is dead. An attached
    // one costs a re-attach and its replay, which is the cheaper mistake.
    fire('online')

    expect(StubSocket.opened).toHaveLength(2)
    expect(StubSocket.opened[0].closed).toBe(true)
    a.close()
  })

  it('resets the backoff on a wake that lands before the queued close', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()
    for (let n = 0; n < 5; n++) {
      StubSocket.last().onclose?.({ code: 1006 })
      vi.advanceTimersByTime(30_000)
    }
    const before = StubSocket.opened.length

    // The newest socket died while the tab was frozen and its close has not
    // been delivered yet. The reset must happen anyway, or the close that
    // arrives next schedules the pre-suspend backoff.
    fire('visibilitychange')
    expect(StubSocket.opened).toHaveLength(before)

    StubSocket.last().onclose?.({ code: 1006 })
    vi.advanceTimersByTime(600)

    expect(StubSocket.opened).toHaveLength(before + 1)
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

  it('stops without reconnecting when the server ends the session', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()
    StubSocket.last().onmessage?.({
      data: new TextEncoder().encode('replayed history').buffer,
    })

    // The gateway names a clean session end; a reconnect could only replay
    // the same bytes again.
    StubSocket.last().onclose?.({ code: 1000, reason: 'session ended' })
    vi.advanceTimersByTime(5000)

    expect(StubSocket.opened).toHaveLength(1)
    expect(states[states.length - 1]).toBe('offline')
    expect(refusal).toBeNull()
    a.close()
  })
})

describe('replayGate', () => {
  it('mutes input from the first replay byte until the replay-end write has parsed', () => {
    const done: Array<(() => void) | undefined> = []
    const gate = replayGate((_chunk, cb) => done.push(cb))
    const bytes = new Uint8Array([1])

    expect(gate.muted()).toBe(false)
    gate.write(bytes, 'replay')
    expect(gate.muted()).toBe(true)
    gate.write(bytes, 'replay-end')
    expect(gate.muted()).toBe(true)
    // Live output arriving before xterm has parsed the replay must not unmute.
    gate.write(bytes, 'live')
    expect(gate.muted()).toBe(true)

    expect(done[0]).toBeUndefined()
    done[1]?.()
    expect(gate.muted()).toBe(false)
  })

  it('unmutes on demand so a dropped socket mid-replay never leaves input dead', () => {
    const gate = replayGate(() => {})
    gate.write(new Uint8Array([1]), 'replay')
    gate.unmute()
    expect(gate.muted()).toBe(false)
  })
})

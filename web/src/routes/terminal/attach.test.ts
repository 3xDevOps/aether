import {
  type Attachment,
  type ControlMetadata,
  type ControlResult,
  codeConflict,
  codeDenied,
  connectAttach,
  replayGate,
} from '@/routes/terminal/attach'
import type { ConnectionState } from '@/lib/stream'
import { StubSocket } from '@/test/stub-socket'
import { fire } from '@/test/wake'

let output: string[] = []
let outputKinds: Array<[string, string]> = []
let states: ConnectionState[] = []
let attaches = 0
let ackIncarnation = 0
let refusalCode: number | undefined
let denied = false
let controlLost = false
let write = false
let sessionPending = false
// Every attachment this file opens, closed in afterEach. A test that fails
// before its own `close()` would otherwise leave its wake listeners on
// `document` and open a socket during the next test.
let receivedControl: ControlMetadata | null = null
let attachments: Attachment[] = []
let refusal: string | null = null

function attach(url: string | (() => string) = '/ws/attach/run_1'): Attachment {
  output = []
  outputKinds = []
  states = []
  attaches = 0
  refusal = null
  refusalCode = undefined
  denied = false
  controlLost = false
  receivedControl = null
  const socketURL = typeof url === 'function' ? url : () => url
  const attachment = connectAttach(socketURL, {
    onData: (chunk, kind, settled) => {
      const text = new TextDecoder().decode(chunk)
      output.push(text)
      outputKinds.push([kind, text])
      settled?.()
    },
    onAttached: () => {
      attaches++
    },
    onControl: (metadata) => {
      receivedControl = metadata
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
    onControlLost: () => {
      controlLost = true
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
  const resume_id = `pty-incarnation-${++ackIncarnation}`
  StubSocket.last().onmessage?.({
    data: JSON.stringify({ ok: true, cols: 120, rows: 40, resume_id, ...over }),
  })
}

beforeEach(() => {
  StubSocket.install()
  vi.useFakeTimers()
  output = []
  outputKinds = []
  states = []
  attaches = 0
  ackIncarnation = 0
  refusal = null
  refusalCode = undefined
  denied = false
  controlLost = false
  receivedControl = null
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
    expect(socket.frames()[0]).toEqual({ cols: 120, rows: 40, control_session_id: expect.any(String) })

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
  it('exposes the lease and fences takeover and release handshakes', () => {
    write = true
    const a = attach()
    StubSocket.last().onopen?.()
    ack({ has_control: true, control_generation: 4 })
    expect(receivedControl).toMatchObject({
      control_session_id: a.controlMetadata!().control_session_id,
      control_generation: 4,
      has_control: true,
    })
    expect(a.controlMetadata!()).toMatchObject({
      control_session_id: expect.any(String),
      control_generation: 4,
      has_control: true,
    })

    a.reopen({ resume: true })
    const reconnect = StubSocket.last()
    reconnect.onopen?.()
    expect(reconnect.frames()[0]).toMatchObject({
      write: true,
      takeover: true,
      resume: true,
      resume_id: 'pty-incarnation-1',
      cursor: 0,
      control_session_id: a.controlMetadata!().control_session_id,
      control_generation: 4,
    })
    ack({ has_control: true, control_generation: 5 })

    a.reopen({ resume: true, takeover: true })
    const takeover = StubSocket.last()
    takeover.onopen?.()
    expect(takeover.frames()[0]).toMatchObject({
      write: true,
      takeover: true,
      resume: true,
      resume_id: 'pty-incarnation-2',
      cursor: 0,
      control_session_id: a.controlMetadata!().control_session_id,
      control_generation: 5,
    })
    ack({ has_control: true, control_generation: 6 })

    a.reopen({ resume: true, releaseControl: true })
    const release = StubSocket.last()
    release.onopen?.()
    expect(release.frames()[0]).toMatchObject({
      release_control: true,
      resume: true,
      resume_id: 'pty-incarnation-3',
      cursor: 0,
      control_session_id: a.controlMetadata!().control_session_id,
      control_generation: 6,
    })
    ack({ has_control: false, control_generation: 6 })
    expect(a.controlMetadata!().has_control).toBe(false)
  })

  it('rebinds lifecycle callbacks when a persistent socket gets a new host', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack()

    const nextAttached = vi.fn()
    const nextOutput: string[] = []
    a.rebind({
      onData: (chunk) => {
        nextOutput.push(new TextDecoder().decode(chunk))
      },
      onAttached: nextAttached,
      onState: vi.fn(),
      onRefused: vi.fn(),
      onWriteDenied: vi.fn(),
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    expect(StubSocket.opened).toHaveLength(2)
    StubSocket.last().onopen?.()
    ack()
    StubSocket.last().onmessage?.({ data: new TextEncoder().encode('new host').buffer })

    expect(nextAttached).toHaveBeenCalledOnce()
    expect(nextOutput).toEqual(['new host'])
    a.close()
  })
  it('cancels a stale drain and starts one full replay when rebound', async () => {
    let finishOld!: () => void
    const oldOutput: string[] = []
    const nextOutput: string[] = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk, kind) => {
        oldOutput.push(`${kind}:${new TextDecoder().decode(chunk)}`)
        return new Promise<void>((resolve) => {
          finishOld = resolve
        })
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const oldSocket = StubSocket.last()
    oldSocket.onopen?.()
    oldSocket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 3, resume_id: 'pty-incarnation-old' }) })
    oldSocket.onmessage?.({ data: new TextEncoder().encode('old').buffer })
    expect(oldOutput).toEqual(['replay-end:old'])

    a.rebind({
      onData: (chunk, kind) => {
        nextOutput.push(`${kind}:${new TextDecoder().decode(chunk)}`)
      },
      onAttached: vi.fn(),
      onState: vi.fn(),
      onRefused: vi.fn(),
      onWriteDenied: vi.fn(),
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })

    expect(oldSocket.closed).toBe(true)
    expect(StubSocket.opened).toHaveLength(2)
    const freshSocket = StubSocket.last()
    freshSocket.onopen?.()
    expect(freshSocket.frames()[0]).not.toHaveProperty('resume')
    freshSocket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 3, resume_id: 'pty-incarnation-new' }) })
    freshSocket.onmessage?.({ data: new TextEncoder().encode('new').buffer })
    expect(nextOutput).toEqual(['replay-end:new'])

    finishOld()
    await Promise.resolve()
    expect(nextOutput).toEqual(['replay-end:new'])
    a.close()
  })
  it('resumes from where the live output left it, so the gap is asked for', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack({ cursor: 100 })
    StubSocket.last().onmessage?.({ data: new TextEncoder().encode('live!').buffer })

    a.reopen({ resume: true })
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-1',
      cursor: 105,
    })
    a.close()
  })

  it('falls back to a full attach when no successful ack supplied a fence', () => {
    const a = attach()
    const first = StubSocket.last()
    first.onopen?.()
    first.onmessage?.({
      data: JSON.stringify({ ok: true, cursor: 100, replay: 0 }),
    })

    a.reopen({ resume: true })
    const replacement = StubSocket.last()
    replacement.onopen?.()

    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    expect(replacement.frames()[0]).not.toHaveProperty('resume_id')
    expect(replacement.frames()[0]).not.toHaveProperty('cursor')
    a.close()
  })

  it('uses the fallback ack incarnation for the next resume', () => {
    const a = attach()
    StubSocket.last().onopen?.()
    ack({ cursor: 100, resume_id: 'pty-incarnation-a' })

    a.reopen({ resume: true })
    const attempted = StubSocket.last()
    attempted.onopen?.()
    expect(attempted.frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-a',
      cursor: 100,
    })
    ack({ cursor: 200, resumed: false, resume_id: 'pty-incarnation-b' })

    a.reopen({ resume: true })
    const next = StubSocket.last()
    next.onopen?.()
    expect(next.frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-b',
      cursor: 200,
    })
    a.close()
  })
  it('waits for a live settled callback before resuming with its parsed cursor', async () => {
    let settle!: () => void
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, _kind, done) => {
        settle = done!
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const first = StubSocket.last()
    first.onopen?.()
    first.onmessage?.({ data: JSON.stringify({ ok: true, cursor: 100, replay: 0, resume_id: 'pty-incarnation-a' }) })
    first.onmessage?.({ data: new TextEncoder().encode('live!').buffer })

    a.reopen({ resume: true })
    expect(first.closed).toBe(true)
    expect(StubSocket.opened).toHaveLength(1)

    // The replacement must wait until xterm has parsed all old live bytes.
    settle()
    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-a',
      cursor: 105,
    })
    a.close()
  })

  it('waits for live parsing before resuming and then sends the parsed cursor', async () => {
    let settle!: () => void
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, _kind, done) => {
        settle = done!
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const first = StubSocket.last()
    first.onopen?.()
    first.onmessage?.({
      data: JSON.stringify({ ok: true, cursor: 100, replay: 0, has_control: true, resume_id: 'pty-incarnation-a' }),
    })
    const sessionID = a.controlMetadata?.().control_session_id
    first.onmessage?.({ data: new TextEncoder().encode('live!').buffer })

    a.suspend()
    a.resume()
    expect(StubSocket.opened).toHaveLength(1)
    settle()
    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-a',
      cursor: 105,
      control_session_id: sessionID,
    })
    a.close()
  })

  it('suspends without retrying or waking a dropped transport', () => {
    const a = attach()
    const socket = StubSocket.last()
    socket.onopen?.()
    ack()

    a.suspend()
    expect(socket.closed).toBe(true)
    fire('visibilitychange')
    fire('online')
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)
    a.close()
  })

  it('cancels replay on suspend and resumes with a fresh full replay', async () => {
    const a = attach()
    const first = StubSocket.last()
    first.onopen?.()
    ack({ cursor: 100, replay: 4 })
    first.onmessage?.({ data: new TextEncoder().encode('old').buffer })

    a.suspend()
    a.resume()
    await Promise.resolve()
    expect(StubSocket.opened).toHaveLength(2)
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    a.close()
  })
  it('keeps a socket-aborted replay invalid across suspend and resume', async () => {
    const a = attach()
    const first = StubSocket.last()
    first.onopen?.()
    ack({ cursor: 100, replay: 4 })
    first.onmessage?.({ data: new TextEncoder().encode('old').buffer })
    first.onclose?.({ code: 1006, reason: '' })

    // Suspending after the close must not forget that the xterm only parsed a
    // prefix. The next attach has to replace the screen with a full replay.
    a.suspend()
    a.resume()
    await Promise.resolve()
    expect(StubSocket.opened).toHaveLength(2)
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
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
      data: JSON.stringify({ ok: true, cols: 120, rows: 40, resumed: false, resume_id: 'pty-incarnation-b' }),
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

  it('delivers frame-sized replay slices and a straddling suffix in order', () => {
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

  it('starts replay parsing on each frame before the declared boundary', () => {
    const a = attach()
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 6 })

    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })
    expect(outputKinds).toEqual([['replay', 'abc']])
    socket.onmessage?.({ data: new TextEncoder().encode('def').buffer })

    expect(outputKinds).toEqual([
      ['replay', 'abc'],
      ['replay-end', 'def'],
    ])
    a.close()
  })

  it('parses a large partial replay incrementally without making it visible', () => {
    const visibility: boolean[] = []
    const parsed: string[] = []
    const gate = replayGate(
      (_chunk, done) => {
        done?.()
      },
      (replaying) => visibility.push(replaying),
    )
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk, kind) => {
        parsed.push(`${kind}:${new TextDecoder().decode(chunk)}`)
        return gate.write(chunk, kind)
      },
      onAttached: () => {},
      onReplayStart: (bytes) => (bytes > 0 ? gate.start() : gate.unmute()),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 786_432 })
    socket.onmessage?.({ data: new TextEncoder().encode('first frame').buffer })

    expect(parsed).toEqual(['replay:first frame'])
    expect(visibility).toEqual([true])
    expect(gate.muted()).toBe(true)
    a.close()
  })

  it('delivers a replay split across frames exactly once and byte-identically', () => {
    const a = attach()
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 6 })

    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('def').buffer })

    expect(outputKinds).toEqual([
      ['replay', 'abc'],
      ['replay-end', 'def'],
    ])
    a.close()
  })
  it('queues geometry and live frames behind segmented replay in wire order', async () => {
    const events: string[] = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk, kind, settled) => {
        events.push(`${kind}:${new TextDecoder().decode(chunk)}`)
        settled?.()
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      onGeometry: (cols, rows) => events.push(`geometry:${cols}x${rows}`),
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({
      data: JSON.stringify({ ok: true, cursor: 10, replay: 3, cols: 80, rows: 24, resume_id: 'pty-incarnation-a' }),
    })
    socket.onmessage?.({ data: new TextEncoder().encode('a').buffer })
    socket.onmessage?.({ data: JSON.stringify({ type: 'geometry', cols: 100, rows: 30, ok: true }) })
    socket.onmessage?.({ data: new TextEncoder().encode('bcLIVE').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('next').buffer })

    await vi.waitFor(() =>
      expect(events).toEqual([
        'replay:a',
        'geometry:100x30',
        'replay-end:bc',
        'live:LIVE',
        'live:next',
      ]),
    )
    a.reopen({ resume: true })
    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-a',
      cursor: 18,
    })
    a.close()
  })
  it('drains geometry queued before the first replay frame', () => {
    const geometries: Array<[number, number]> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      onGeometry: (cols, rows) => geometries.push([cols, rows]),
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({
      data: JSON.stringify({ ok: true, replay: 1, resume_id: 'pty-incarnation-a' }),
    })
    socket.onmessage?.({
      data: JSON.stringify({
        type: 'geometry',
        cols: 100,
        rows: 30,
      }),
    })

    expect(geometries).toEqual([[100, 30]])
    a.close()
  })

  it('stops old replay operations when closed during the first callback', async () => {
    const events: string[] = []
    let finish!: () => void
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk, kind) => {
        events.push(`${kind}:${new TextDecoder().decode(chunk)}`)
        if (kind === 'replay') {
          return new Promise<void>((resolve) => {
            finish = resolve
          })
        }
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 4 })
    socket.onmessage?.({ data: new TextEncoder().encode('ab').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('cdLIVE').buffer })

    expect(events).toEqual(['replay:ab'])
    a.close()
    finish()
    await Promise.resolve()

    expect(events).toEqual(['replay:ab'])
  })

  it('waits for each queued live settled callback before the next live frame', async () => {
    const events: string[] = []
    const settled: Array<() => void> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk, kind, done) => {
        events.push(`${kind}:${new TextDecoder().decode(chunk)}`)
        if (kind === 'live' && done) settled.push(done)
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 1, resume_id: 'pty-incarnation-a' })
    socket.onmessage?.({ data: new TextEncoder().encode('rONE').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('TWO').buffer })

    expect(events).toEqual(['replay-end:r', 'live:ONE'])
    expect(settled).toHaveLength(1)
    settled[0]()
    await Promise.resolve()
    await Promise.resolve()

    expect(events).toEqual(['replay-end:r', 'live:ONE', 'live:TWO'])
    expect(settled).toHaveLength(2)
    settled[1]()
    await Promise.resolve()
    await Promise.resolve()
    a.reopen({ resume: true })
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-a',
      cursor: 6,
    })
    a.close()

  })
  it('completes a deferred reopen when queued live output has no handler', () => {
    const a = connectAttach(() => '/ws/attach/run_1', {
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({
      data: JSON.stringify({ ok: true, replay: 1, resume_id: 'pty-incarnation-a' }),
    })

    // The reopen is deferred while the replay boundary is still arriving.
    a.reopen({ resume: true })
    expect(StubSocket.opened).toHaveLength(1)
    socket.onmessage?.({ data: new TextEncoder().encode('rLIVE').buffer })

    expect(StubSocket.opened).toHaveLength(2)
    a.close()
  })

  it('awaits a Promise returned by queued live output before the next operation', async () => {
    const events: string[] = []
    const finish: Array<() => void> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk, kind) => {
        events.push(`${kind}:${new TextDecoder().decode(chunk)}`)
        if (kind === 'live') {
          return new Promise<void>((resolve) => finish.push(resolve))
        }
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({
      data: JSON.stringify({ ok: true, replay: 1, resume_id: 'pty-incarnation-a' }),
    })
    socket.onmessage?.({ data: new TextEncoder().encode('rONE').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('TWO').buffer })

    expect(events).toEqual(['replay-end:r', 'live:ONE'])
    expect(finish).toHaveLength(1)
    finish[0]()
    await Promise.resolve()
    await Promise.resolve()

    expect(events).toEqual(['replay-end:r', 'live:ONE', 'live:TWO'])
    expect(finish).toHaveLength(2)
    finish[1]()
    await Promise.resolve()
    a.close()
  })

  it('passes a settled callback to direct live output', () => {
    const callbacks: Array<(() => void) | undefined> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, _kind, settled) => {
        callbacks.push(settled)
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 0 })
    socket.onmessage?.({ data: new TextEncoder().encode('live').buffer })

    expect(callbacks[0]).toEqual(expect.any(Function))
    a.close()
  })
  it('turns a direct live rendering throw into a final refusal', () => {
    const replaySignals: number[] = []
    let liveFailure: string | null = null
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: () => {
        throw new Error('live output failed')
      },
      onAttached: () => {},
      onReplayStart: (bytes) => replaySignals.push(bytes),
      onState: (state) => states.push(state),
      onRefused: (message) => {
        liveFailure = message
      },
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 0 })
    socket.onmessage?.({ data: new TextEncoder().encode('live').buffer })

    expect(liveFailure).toBe('terminal live output failed: live output failed')
    expect(replaySignals).toEqual([0, 0])
    expect(states.at(-1)).toBe('offline')
    expect(socket.closed).toBe(true)
    a.close()
  })


  it('waits for each replay Promise before delivering queued live output or reopening', async () => {
    const events: string[] = []
    const finish: Array<() => void> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk, kind, settled) => {
        events.push(`${kind}:${new TextDecoder().decode(chunk)}`)
        if (kind === 'live') settled?.()
        if (kind === 'replay' || kind === 'replay-end') {
          return new Promise<void>((resolve) => finish.push(resolve))
        }
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 6, resume_id: 'pty-incarnation-a' }) })
    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('def').buffer })
    socket.onmessage?.({ data: new TextEncoder().encode('live').buffer })
    a.reopen({ resume: true })

    expect(events).toEqual(['replay:abc'])
    expect(finish).toHaveLength(1)
    expect(StubSocket.opened).toHaveLength(1)
    finish[0]()
    await Promise.resolve()
    await Promise.resolve()
    expect(events).toEqual(['replay:abc', 'replay-end:def'])
    expect(finish).toHaveLength(2)
    expect(StubSocket.opened).toHaveLength(1)
    finish[1]()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(events).toEqual(['replay:abc', 'replay-end:def', 'live:live'])
    expect(StubSocket.opened).toHaveLength(2)
    a.close()
  })
  it('defers an unexpected reconnect until replay parsing settles', async () => {
    let finishReplay!: () => void
    const replayParsed = new Promise<void>((resolve) => {
      finishReplay = resolve
    })
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, kind) => (kind === 'replay-end' ? replayParsed : undefined),
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 3, resume_id: 'pty-incarnation-a' }) })
    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })
    socket.onclose?.({ code: 1006 })

    expect(StubSocket.opened).toHaveLength(1)
    finishReplay()
    await Promise.resolve()
    await Promise.resolve()
    expect(StubSocket.opened).toHaveLength(1)
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(2)
    a.close()
  })


  it('refuses an invalid replay length and stops that socket', () => {
    const a = attach()
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: -1 })

    expect(refusal).toBe('invalid replay length: -1')
    expect(states.at(-1)).toBe('offline')
    expect(socket.closed).toBe(true)
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)
    a.close()
  })
  it('refuses a replay when queued geometry throws and releases its gate', () => {
    const replaySignals: number[] = []
    let replayFailure: string | null = null
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: () => {},
      onGeometry: () => {
        throw new Error('geometry failed')
      },
      onAttached: () => {},
      onReplayStart: (bytes) => replaySignals.push(bytes),
      onState: (state) => states.push(state),
      onRefused: (message) => {
        replayFailure = message
      },
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 2 })
    socket.onmessage?.({ data: new TextEncoder().encode('a').buffer })
    socket.onmessage?.({ data: JSON.stringify({ type: 'geometry', cols: 100, rows: 30 }) })
    socket.onmessage?.({ data: new TextEncoder().encode('b').buffer })

    expect(replayFailure).toBe('terminal replay failed: geometry failed')
    expect(replaySignals).toEqual([2, 0])
    expect(states.at(-1)).toBe('offline')
    expect(socket.closed).toBe(true)
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)
    a.close()
  })

  it('refuses a replay when output throws instead of swallowing the error', () => {
    const replaySignals: number[] = []
    let replayFailure: string | null = null
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: () => {
        throw new Error('output failed')
      },
      onAttached: () => {},
      onReplayStart: (bytes) => replaySignals.push(bytes),
      onState: (state) => states.push(state),
      onRefused: (message) => {
        replayFailure = message
      },
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 1 })
    socket.onmessage?.({ data: new TextEncoder().encode('a').buffer })

    expect(replayFailure).toBe('terminal replay failed: output failed')
    expect(replaySignals).toEqual([1, 0])
    expect(states.at(-1)).toBe('offline')
    expect(socket.closed).toBe(true)
    a.close()
  })

  it('refuses a replay when an output completion rejects', async () => {
    const replaySignals: number[] = []
    let replayFailure: string | null = null
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: () => Promise.reject(new Error('completion failed')),
      onAttached: () => {},
      onReplayStart: (bytes) => replaySignals.push(bytes),
      onState: (state) => states.push(state),
      onRefused: (message) => {
        replayFailure = message
      },
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 1 })
    socket.onmessage?.({ data: new TextEncoder().encode('a').buffer })
    await Promise.resolve()
    await Promise.resolve()

    expect(replayFailure).toBe('terminal replay failed: completion failed')
    expect(replaySignals).toEqual([1, 0])
    expect(states.at(-1)).toBe('offline')
    expect(socket.closed).toBe(true)
    a.close()
  })

  it('keeps an incomplete replay hidden across socket close until replacement ack', () => {
    const replaySignals: number[] = []
    const replayed: string[] = []
    const visibility: boolean[] = []
    const gate = replayGate(
      (chunk, done) => {
        replayed.push(new TextDecoder().decode(chunk))
        done?.()
      },
      (replaying) => visibility.push(replaying),
    )
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: gate.write,
      onAttached: () => {},
      onReplayAbort: gate.cancel,
      onReplayStart: (bytes) => {
        replaySignals.push(bytes)
        if (bytes > 0) gate.start()
        else gate.unmute()
      },
      onState: (state) => states.push(state),
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 2 })
    socket.onmessage?.({ data: new TextEncoder().encode('a').buffer })
    socket.onclose?.({ code: 1006 })

    expect(replayed).toEqual(['a'])
    expect(replaySignals).toEqual([2])
    expect(gate.muted()).toBe(true)
    expect(visibility).toEqual([true, true])
    expect(states.at(-1)).toBe('reconnecting')

    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(2)
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('resume')
    ack({ replay: 0 })
    expect(replaySignals).toEqual([2, 0])
    expect(gate.muted()).toBe(false)
    expect(visibility).toEqual([true, true, false])
    a.close()
  })


  it('mutes at the replay ack before the first replay byte and through parsing', () => {
    let finish: (() => void) | undefined
    const gate = replayGate((_chunk, done) => {
      finish = done
    })
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: gate.write,
      onAttached: () => {},
      onReplayStart: (bytes) => (bytes > 0 ? gate.start() : gate.unmute()),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 3 })

    expect(gate.muted()).toBe(true)
    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })
    expect(gate.muted()).toBe(true)
    finish?.()
    expect(gate.muted()).toBe(false)
    vi.runAllTimers()
    expect(gate.muted()).toBe(false)
    a.close()
  })
  it('ignores a stale replay-end callback from an older generation', () => {
    const visibility: boolean[] = []
    const done: Array<() => void> = []
    const gate = replayGate((_chunk, cb) => {
      if (cb) done.push(cb)
    }, (replaying) => visibility.push(replaying))

    gate.start()
    gate.write(new Uint8Array([1]), 'replay-end')
    gate.start()
    done[0]()
    expect(gate.muted()).toBe(true)
    expect(visibility).toEqual([true, true])

    gate.write(new Uint8Array([2]), 'replay-end')
    done[1]()
    expect(gate.muted()).toBe(false)
    vi.runAllTimers()
    expect(gate.muted()).toBe(false)
    expect(visibility).toEqual([true, true, false])
  })

  it('defers the latest steering or release reopen until replay parsing completes', async () => {
    let finishReplay!: () => void
    const replayParsed = new Promise<void>((resolve) => {
      finishReplay = resolve
    })
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, kind) => (kind === 'replay-end' ? replayParsed : undefined),
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 3, cursor: 10 })
    socket.onmessage?.({ data: new TextEncoder().encode('a').buffer })

    a.reopen({ resume: true, takeover: true })
    a.reopen({ resume: true, releaseControl: true })
    expect(StubSocket.opened).toHaveLength(1)

    socket.onmessage?.({ data: new TextEncoder().encode('bc').buffer })
    expect(StubSocket.opened).toHaveLength(1)
    finishReplay()
    await Promise.resolve()
    await Promise.resolve()

    expect(StubSocket.opened).toHaveLength(2)
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).toMatchObject({
      release_control: true,
      resume: true,
      resume_id: 'pty-incarnation-1',
      cursor: 10,
    })
    a.close()
  })
  it('reconnects as a mirror after control loss clears a deferred release', async () => {
    write = true
    controlLost = false
    receivedControl = null
    let finishReplay!: () => void
    const replayParsed = new Promise<void>((resolve) => {
      finishReplay = resolve
    })
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, kind) => (kind === 'replay-end' ? replayParsed : undefined),
      onAttached: () => {},
      onControl: (metadata) => {
        receivedControl = metadata
      },
      onState: (state) => states.push(state),
      onRefused: (message, code) => {
        refusal = message
        refusalCode = code
      },
      onWriteDenied: () => {
        denied = true
        write = false
      },
      onControlLost: () => {
        controlLost = true
        write = false
      },
      geometry: () => ({ cols: 120, rows: 40 }),
      wantsWrite: () => write,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 3, has_control: true, control_generation: 4 })
    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })

    a.reopen({ resume: true, releaseControl: true })
    socket.onclose?.({ code: 1008, reason: 'control taken over' })

    expect(controlLost).toBe(true)
    expect(receivedControl).toMatchObject({ has_control: false, control_generation: 4 })
    expect(StubSocket.opened).toHaveLength(1)
    expect(refusal).toBeNull()
    expect(states).not.toContain('offline')

    finishReplay()
    await Promise.resolve()
    await Promise.resolve()

    expect(StubSocket.opened).toHaveLength(2)
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).toEqual({
      cols: 120,
      rows: 40,
      control_session_id: expect.any(String),
    })
    a.close()
  })

  it('latches permission withdrawal while clearing a deferred takeover', async () => {
    write = true
    denied = false
    controlLost = false
    receivedControl = null
    let finishReplay!: () => void
    const replayParsed = new Promise<void>((resolve) => {
      finishReplay = resolve
    })
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, kind) => (kind === 'replay-end' ? replayParsed : undefined),
      onAttached: () => {},
      onControl: (metadata) => {
        receivedControl = metadata
      },
      onState: (state) => states.push(state),
      onRefused: (message, code) => {
        refusal = message
        refusalCode = code
      },
      onWriteDenied: () => {
        denied = true
        write = false
      },
      onControlLost: () => {
        controlLost = true
        write = false
      },
      geometry: () => ({ cols: 120, rows: 40 }),
      wantsWrite: () => write,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 3, has_control: true, control_generation: 7 })
    socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })

    a.reopen({ resume: true, takeover: true })
    socket.onclose?.({ code: 1008, reason: 'steer permission withdrawn' })

    expect(denied).toBe(true)
    expect(controlLost).toBe(false)
    expect(receivedControl).toMatchObject({ has_control: false, control_generation: 7 })
    expect(StubSocket.opened).toHaveLength(1)

    finishReplay()
    await Promise.resolve()
    await Promise.resolve()

    expect(StubSocket.opened).toHaveLength(2)
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).toEqual({
      cols: 120,
      rows: 40,
      control_session_id: expect.any(String),
    })
    ack({ has_control: false, control_generation: 8 })

    // The latch survives the mirror reconnect and suppresses later write
    // requests, even when the caller asks for control again.
    write = true
    a.reopen({ takeover: true })
    const retry = StubSocket.last()
    retry.onopen?.()
    expect(retry.frames()[0]).not.toHaveProperty('write')
    expect(denied).toBe(true)
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
      control_session_id: expect.any(String),
    })
    a.close()
  })
  it('drops controls while a replacement is connecting or awaiting ack', () => {
    const a = attach()
    const first = StubSocket.last()
    first.onopen?.()
    ack()

    a.reopen({ resume: true })
    const replacement = StubSocket.last()
    expect(first.closed).toBe(true)
    a.send('while connecting')
    a.resize(90, 30)
    expect(replacement.sent).toHaveLength(0)

    replacement.onopen?.()
    expect(replacement.frames()[0]).toMatchObject({ resume: true })
    a.send('before ack')
    a.resize(91, 31)
    expect(replacement.sent).toHaveLength(1)

    ack()
    a.send('after ack')
    a.resize(92, 32)
    expect(replacement.frames().slice(1)).toEqual([
      { type: 'input', data: 'after ack' },
      { type: 'resize', cols: 92, rows: 32 },
    ])
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

    expect(StubSocket.last().frames()[0]).toEqual({
      cols: 120,
      rows: 40,
      control_session_id: expect.any(String),
    })
    expect(refusal).toBeNull()
    a.close()
  })

  it('reconnects as a mirror after an occupied writable lease conflict', () => {
    write = true
    const a = attach()
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({
        ok: false,
        code: codeConflict,
        error: 'run.attach: control lease is occupied',
      }),
    })

    expect(controlLost).toBe(true)
    expect(denied).toBe(false)
    expect(refusal).toBeNull()

    StubSocket.last().onclose?.({ code: 1008 })
    vi.advanceTimersByTime(1000)
    StubSocket.last().onopen?.()

    expect(StubSocket.last().frames()[0]).toEqual({
      cols: 120,
      rows: 40,
      control_session_id: expect.any(String),
    })
    expect(refusal).toBeNull()
    a.close()
  })

  it('falls back to a mirror if a forced reconnect was already fenced', () => {
    write = true
    const a = attach()
    StubSocket.last().onopen?.()
    ack({ has_control: true, control_generation: 4 })

    a.reopen({ resume: true })
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).toMatchObject({
      write: true,
      takeover: true,
      control_generation: 4,
    })
    StubSocket.last().onmessage?.({
      data: JSON.stringify({
        ok: false,
        code: codeConflict,
        error: 'run.attach: control generation is stale',
      }),
    })
    expect(controlLost).toBe(true)
    expect(refusal).toBeNull()

    StubSocket.last().onclose?.({ code: 1008 })
    vi.advanceTimersByTime(1000)
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
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
    expect(StubSocket.last().frames()[0]).toEqual({
      cols: 120,
      rows: 40,
      control_session_id: expect.any(String),
    })
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
  it('cancels an online replay without revealing partial history', () => {
    const replaySignals: number[] = []
    const gate = replayGate(() => {})
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: gate.write,
      onAttached: () => {},
      onReplayAbort: gate.cancel,
      onReplayStart: (bytes) => {
        replaySignals.push(bytes)
        if (bytes > 0) gate.start()
        else gate.unmute()
      },
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 4 })
    socket.onmessage?.({ data: new TextEncoder().encode('old').buffer })
    expect(gate.muted()).toBe(true)

    fire('online')

    expect(gate.muted()).toBe(true)
    expect(socket.closed).toBe(true)
    expect(StubSocket.opened).toHaveLength(2)
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    ack({ replay: 2 })
    expect(replaySignals).toEqual([4, 2])
    expect(gate.muted()).toBe(true)
    a.close()
  })
  it('cancels a boundary-complete drain before online replacement', async () => {
    let finish!: () => void
    let aborted = false
    const replayParsed = new Promise<void>((resolve) => {
      finish = resolve
    })
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, kind) => (kind === 'replay-end' ? replayParsed : undefined),
      onAttached: () => {},
      onReplayAbort: () => {
        aborted = true
      },
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    ack({ replay: 1 })
    socket.onmessage?.({ data: new TextEncoder().encode('x').buffer })
    await Promise.resolve()
    expect(StubSocket.opened).toHaveLength(1)

    fire('online')

    expect(aborted).toBe(true)
    expect(socket.closed).toBe(true)
    expect(StubSocket.opened).toHaveLength(2)
    finish()
    await Promise.resolve()
    await Promise.resolve()
    expect(StubSocket.opened).toHaveLength(2)
    a.close()
  })

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
    expect(StubSocket.last().frames()[0]).toEqual({
      cols: 120,
      rows: 40,
      control_session_id: expect.any(String),
    })
    expect(refusal).toBeNull()
    expect(attaches).toBe(2)
    // Unlike control takeover, an actual permission withdrawal remains
    // denied on every explicit retry.
    write = true
    a.reopen()
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
    a.close()
  })
  it('reconnects as a mirror after control is taken and can ask again', () => {
    write = true
    const a = attach()
    StubSocket.last().onopen?.()
    ack({ has_control: true, control_generation: 9 })

    StubSocket.last().onclose?.({ code: 1008, reason: 'control taken over' })
    expect(controlLost).toBe(true)
    expect(receivedControl).toMatchObject({ has_control: false, control_generation: 9 })
    expect(denied).toBe(false)
    expect(write).toBe(false)

    vi.advanceTimersByTime(1000)
    StubSocket.last().onopen?.()
    ack({ has_control: false, control_generation: 10 })
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')

    // Control loss is not a permanent permission denial: an explicit retry
    // asks for writable control again.
    write = true
    a.reopen({ resume: true })
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })
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
  it('bounds a screen live backlog at 4 MiB and recovers with a full compact replay', async () => {
    const settled: Array<() => void> = []
    const replayStarts: Array<[number, boolean]> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, kind, done) => {
        if (kind === 'live' && done) settled.push(done)
      },
      onAttached: () => {},
      onReplayStart: (bytes, full) => replayStarts.push([bytes, full]),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
      screen: () => true,
    })
    attachments.push(a)
    const oldSocket = StubSocket.last()
    oldSocket.onopen?.()
    oldSocket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, resume_id: 'pty-incarnation-a' }) })
    const oldMessage = oldSocket.onmessage!
    const frame = new Uint8Array(1024 * 1024).buffer
    for (let n = 0; n < 4; n++) oldMessage({ data: frame })

    expect(settled).toHaveLength(4)
    expect(oldSocket.closed).toBe(true)
    // The detached old transport cannot add a fifth write after overflow.
    oldMessage({ data: new Uint8Array([1]).buffer })
    expect(settled).toHaveLength(4)

    settled.forEach((done) => done())
    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).toMatchObject({ screen: true })
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    replacement.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, resume_id: 'pty-incarnation-b' }) })
    expect(replayStarts.at(-1)).toEqual([0, true])
    a.close()
  })

  it('parks overflow recovery across suspend and resumes the invalid screen intentionally', async () => {
    const finish: Array<() => void> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: () => new Promise<void>((resolve) => finish.push(resolve)),
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
      screen: () => true,
    })
    attachments.push(a)
    const oldSocket = StubSocket.last()
    oldSocket.onopen?.()
    oldSocket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0 }) })
    const frame = new Uint8Array(1024 * 1024).buffer
    for (let n = 0; n < 4; n++) oldSocket.onmessage?.({ data: frame })
    expect(finish).toHaveLength(4)

    a.suspend()
    finish.forEach((done) => done())
    await Promise.resolve()
    await Promise.resolve()
    expect(StubSocket.opened).toHaveLength(1)

    a.resume()
    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    StubSocket.last().onopen?.()
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('resume')
    a.close()
  })

  it('does not reopen an overflowed transport after close', async () => {
    const finish: Array<() => void> = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onData: (_chunk, _kind, done) => {
        if (done) finish.push(done)
      },
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
      screen: () => true,
    })
    attachments.push(a)
    const oldSocket = StubSocket.last()
    oldSocket.onopen?.()
    oldSocket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0 }) })
    const frame = new Uint8Array(1024 * 1024).buffer
    for (let n = 0; n < 4; n++) oldSocket.onmessage?.({ data: frame })
    expect(oldSocket.closed).toBe(true)

    a.close()
    finish.forEach((done) => done())
    await Promise.resolve()
    await Promise.resolve()
    vi.advanceTimersByTime(60_000)
    expect(StubSocket.opened).toHaveLength(1)
  })

  it('sends screen and interactive options and updates control in place', () => {
    const metadata: ControlMetadata[] = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onAttached: () => {},
      onControl: (value) => metadata.push(value),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
      screen: () => true,
      interactive: () => true,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    expect(socket.frames()[0]).toMatchObject({ screen: true, interactive: true })
    socket.onmessage?.({
      data: JSON.stringify({
        ok: true,
        replay: 0,
        control_session_id: a.controlMetadata!().control_session_id,
        control_generation: 3,
        has_control: false,
      }),
    })

    a.setControl(true)
    expect(socket.frames()[1]).toMatchObject({
      type: 'control',
      request_id: 1,
      write: true,
    })
    expect(socket.frames()[1]).not.toHaveProperty('control_generation')
    socket.onmessage?.({
      data: JSON.stringify({
        type: 'control',
        request_id: 1,
        ok: true,
        has_control: true,
        control_session_id: a.controlMetadata!().control_session_id,
        control_generation: 4,
      }),
    })
    expect(metadata[metadata.length - 1]).toMatchObject({ has_control: true, control_generation: 4 })
    a.send('x')
    expect(socket.frames()[2]).toMatchObject({ type: 'input', control_generation: 4 })
    a.close()
  })
  it('clears authority on an unsolicited negative current-fence record', () => {
    const metadata: ControlMetadata[] = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onAttached: () => {},
      onControl: (value) => metadata.push(value),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
      interactive: () => true,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    const sessionID = a.controlMetadata!().control_session_id
    socket.onmessage?.({
      data: JSON.stringify({
        ok: true,
        replay: 0,
        control_session_id: sessionID,
        control_generation: 5,
        has_control: true,
      }),
    })

    // A Go omitempty response can omit both false-valued fields. A stale
    // fence must still be ignored before deriving the current revocation.
    socket.onmessage?.({
      data: JSON.stringify({
        type: 'control',
        control_session_id: sessionID,
        control_generation: 4,
      }),
    })
    expect(a.controlMetadata!().has_control).toBe(true)

    socket.onmessage?.({
      data: JSON.stringify({
        type: 'control',
        control_session_id: sessionID,
        control_generation: 5,
      }),
    })
    expect(metadata.at(-1)).toMatchObject({ control_generation: 5, has_control: false })
    expect(a.controlMetadata!().has_control).toBe(false)
    a.close()
  })


  it('keeps a mirror owner fence for an explicit takeover', () => {
    const a = connectAttach(() => '/ws/attach/run_1', {
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
      interactive: () => true,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, control_generation: 7, has_control: false }) })
    a.setControl(true, true)
    expect(socket.frames()[1]).toMatchObject({
      type: 'control',
      write: true,
      takeover: true,
      control_generation: 7,
    })
    a.close()
  })

  it('accepts a fresh zero-generation denial and permits the next mirror acquisition', () => {
    const metadata: ControlMetadata[] = []
    const results: ControlResult[] = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onAttached: () => {},
      onControl: (value) => metadata.push(value),
      onControlResult: (value) => results.push(value),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
      interactive: () => true,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, control_generation: 3, has_control: false }) })

    a.setControl(true)
    expect(socket.frames()[1]).not.toHaveProperty('control_generation')
    socket.onmessage?.({
      data: JSON.stringify({
        type: 'control',
        request_id: 1,
        ok: false,
        code: codeConflict,
        control_generation: 0,
        has_control: false,
      }),
    })
    expect(results[0]).toMatchObject({ ok: false, control_generation: 0, has_control: false })
    expect(metadata.at(-1)).toMatchObject({ control_generation: 0, has_control: false })

    a.setControl(true)
    expect(socket.frames()[2]).not.toHaveProperty('control_generation')
    socket.onmessage?.({
      data: JSON.stringify({
        type: 'control',
        request_id: 2,
        ok: true,
        control_generation: 4,
        has_control: true,
      }),
    })
    expect(metadata.at(-1)).toMatchObject({ control_generation: 4, has_control: true })
    a.close()
  })

  it('ignores stale control acknowledgements and revocations', () => {
    const results: ControlResult[] = []
    const a = connectAttach(() => '/ws/attach/run_1', {
      onAttached: () => {},
      onControlResult: (value) => results.push(value),
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: 80, rows: 24 }),
      wantsWrite: () => false,
    })
    attachments.push(a)
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, control_generation: 5, has_control: true }) })
    a.setControl(false)
    socket.onmessage?.({
      data: JSON.stringify({ type: 'control', request_id: 99, ok: false, has_control: false, control_generation: 4 }),
    })
    expect(a.controlMetadata!().has_control).toBe(true)
    expect(results).toHaveLength(0)
    a.close()
  })
})

describe('replayGate', () => {
  it('mutes input and completes every replay write independently', () => {
    const done: Array<() => void> = []
    const gate = replayGate((_chunk, cb) => {
      if (cb) done.push(cb)
    })
    const bytes = new Uint8Array([1])

    expect(gate.muted()).toBe(false)
    const first = gate.write(bytes, 'replay')
    expect(first).toBeInstanceOf(Promise)
    expect(gate.muted()).toBe(true)
    const final = gate.write(bytes, 'replay-end')
    expect(final).toBeInstanceOf(Promise)
    expect(gate.muted()).toBe(true)
    // Live output arriving before xterm has parsed the replay must not unmute.
    expect(gate.write(bytes, 'live')).toBeUndefined()
    expect(gate.muted()).toBe(true)

    expect(done).toHaveLength(2)
    done[0]()
    expect(gate.muted()).toBe(true)
    done[1]()
    expect(gate.muted()).toBe(false)
    vi.runAllTimers()
    expect(gate.muted()).toBe(false)
  })
  it('reveals after two animation frames and ignores stale generations', () => {
    const visibility: boolean[] = []
    const done: Array<() => void> = []
    const frames: Array<() => void> = []
    vi.stubGlobal('requestAnimationFrame', (callback: () => void) => {
      frames.push(callback)
      return frames.length
    })
    const gate = replayGate((_chunk, cb) => {
      if (cb) done.push(cb)
    }, (replaying) => visibility.push(replaying))

    gate.start()
    gate.write(new Uint8Array([1]), 'replay-end')
    gate.start()
    done[0]()
    expect(gate.muted()).toBe(true)
    expect(visibility).toEqual([true, true])

    gate.write(new Uint8Array([2]), 'replay-end')
    done[1]()
    expect(gate.muted()).toBe(false)
    expect(visibility).toEqual([true, true])
    expect(frames).toHaveLength(1)

    frames.shift()?.()
    expect(gate.muted()).toBe(false)
    expect(frames).toHaveLength(1)
    frames.shift()?.()
    expect(gate.muted()).toBe(false)
    expect(visibility).toEqual([true, true, false])
  })

  it('mutes resumed deltas without hiding the warm screen', () => {
    const visibility: boolean[] = []
    const done: Array<() => void> = []
    const gate = replayGate(
      (_chunk, callback) => {
        if (callback) done.push(callback)
      },
      (visible) => visibility.push(visible),
    )
    gate.start('delta')
    expect(gate.muted()).toBe(true)
    expect(visibility).toEqual([true])
    gate.write(new Uint8Array([1]), 'replay-end')
    done[0]()
    expect(gate.muted()).toBe(false)
    expect(visibility).toEqual([true, false])
  })
  it('unmutes on demand so a dropped socket mid-replay never leaves input dead', () => {
    const gate = replayGate(() => {})
    gate.write(new Uint8Array([1]), 'replay')
    gate.unmute()
    expect(gate.muted()).toBe(false)
  })
})

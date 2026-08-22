import { connectShell, dirtyClose, type ShellSession } from '@/routes/shell/client'
import type { WorkspaceShellReq } from '@/store/shell'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', () => ({
  api: { shellSocket: () => 'ws://localhost/ws/shell' },
}))

const setupReq: WorkspaceShellReq = {
  workspace: { id: 'wsp_1' },
  mode: 'agent-setup',
  harness: 'mycli',
  tui_args: ['mycli', '{task}'],
  headless_args: ['mycli', '-p', '{task}'],
}

let output: string[] = []
let attaches = 0
let refusal: { message: string; code?: number } | null = null
let exits: { clean: boolean; reason?: string }[] = []

function connect(req: WorkspaceShellReq = setupReq): ShellSession {
  output = []
  attaches = 0
  refusal = null
  exits = []
  return connectShell(req, {
    onData: (chunk) => output.push(new TextDecoder().decode(chunk)),
    onAttached: () => {
      attaches++
    },
    onRefused: (message, code) => {
      refusal = { message, code }
    },
    onExit: (clean, reason) => exits.push({ clean, reason }),
    geometry: () => ({ cols: 120, rows: 40 }),
  })
}

function ack() {
  StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })
}

beforeEach(() => {
  StubSocket.install()
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('connectShell', () => {
  it('sends the full shell request as the header frame', () => {
    const s = connect()
    const socket = StubSocket.last()
    socket.onopen?.()

    expect(socket.url).toBe('ws://localhost/ws/shell')
    expect(socket.frames()[0]).toEqual({
      workspace: { id: 'wsp_1' },
      mode: 'agent-setup',
      harness: 'mycli',
      tui_args: ['mycli', '{task}'],
      headless_args: ['mycli', '-p', '{task}'],
      cols: 120,
      rows: 40,
    })
    s.close()
  })

  it('omits absent keys and carries resume/reset when set', () => {
    const s = connect({
      workspace: { name: 'default' },
      mode: 'bootstrap-tools',
      resume: true,
    })
    StubSocket.last().onopen?.()

    expect(StubSocket.last().frames()[0]).toEqual({
      workspace: { name: 'default' },
      mode: 'bootstrap-tools',
      resume: true,
      cols: 120,
      rows: 40,
    })
    s.close()
  })

  it('attaches on the ack and delivers binary output', () => {
    const s = connect()
    const socket = StubSocket.last()
    socket.onopen?.()
    ack()
    socket.onmessage?.({ data: new TextEncoder().encode('$ npm i\r\n').buffer })

    expect(attaches).toBe(1)
    expect(output.join('')).toBe('$ npm i\r\n')

    s.send('exit\r')
    expect(socket.frames()[1]).toEqual({ type: 'input', data: 'exit\r' })
    s.resize(80, 24)
    expect(socket.frames()[2]).toEqual({ type: 'resize', cols: 80, rows: 24 })
    s.close()
  })

  it('reports a clean exit on close 1000', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    ack()
    StubSocket.last().onclose?.({ code: 1000 })

    expect(exits).toEqual([{ clean: true, reason: undefined }])
    s.close()
  })

  it('reports a dirty exit with the reason on close 4001', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    ack()
    StubSocket.last().onclose?.({ code: dirtyClose, reason: 'shell exited with status 1' })

    expect(exits).toEqual([{ clean: false, reason: 'shell exited with status 1' }])
    s.close()
  })

  it('names the abandoned session when 4001 carries no reason', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    ack()
    StubSocket.last().onclose?.({ code: dirtyClose })

    expect(exits).toEqual([
      { clean: false, reason: 'shell exited without registering' },
    ])
    s.close()
  })

  it('refuses for good and never reconnects', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: false, code: -32001, error: 'workspace not found' }),
    })
    // The server closes a refused shell too; that close is not a second
    // outcome, and nothing schedules a retry.
    StubSocket.last().onclose?.({ code: 1008 })
    vi.advanceTimersByTime(60_000)

    expect(refusal).toEqual({ message: 'workspace not found', code: -32001 })
    expect(exits).toEqual([])
    expect(StubSocket.opened).toHaveLength(1)
    s.close()
  })

  it('never reconnects after any exit', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    ack()
    StubSocket.last().onclose?.({ code: 1006, reason: '' })
    vi.advanceTimersByTime(60_000)

    expect(exits).toEqual([{ clean: false, reason: 'connection closed (1006)' }])
    expect(StubSocket.opened).toHaveLength(1)
    s.close()
  })

  it('splits a large paste into ordered frames without tearing surrogates', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    ack()

    const paste = 'a'.repeat(8 * 1024 - 1) + '\u{1f600}' + 'b'.repeat(12_000)
    s.send(paste)

    const isInput = (f: unknown): f is { type: string; data: string } =>
      typeof f === 'object' && f !== null && 'type' in f && f.type === 'input'
    const inputs = StubSocket.last().frames().filter(isInput)
    expect(inputs.length).toBeGreaterThan(1)
    expect(inputs.map((f) => f.data).join('')).toBe(paste)
    for (const raw of StubSocket.last().sent) {
      expect(new TextEncoder().encode(raw).length).toBeLessThan(64 * 1024)
    }
    s.close()
  })

  it('drops input until the ack lands', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    s.send('early')
    expect(StubSocket.last().frames()).toHaveLength(1) // header only
    s.close()
  })

  it('reports no outcome for a close it was asked for', () => {
    const s = connect()
    StubSocket.last().onopen?.()
    ack()
    s.close()
    StubSocket.last().onclose?.({ code: 1000 })
    expect(exits).toEqual([])
  })
})

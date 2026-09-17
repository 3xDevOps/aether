import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import type * as apiModule from '@/lib/api'
import type { Run } from '@/lib/types'
import type { RouteProps } from '@/routes/registry'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import { codeDenied } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { initialTerminal, type TerminalState } from '@/store/terminal'
import { bob, run, serverInfo } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'
import { fire } from '@/test/wake'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

function terminalRoute() {
  const View = lookupRoute('terminal')
  if (!View) throw new Error('terminal route not registered')
  return View
}

function mount(
  seed: Partial<TerminalState> = {},
  over: Partial<Run> = {},
  route: Partial<RouteProps> = {},
) {
  const View = terminalRoute()
  useStore.getState().upsertRun(run(over))
  useStore.setState({
    info: serverInfo,
    terminals: { run_1: { ...initialTerminal, ...seed } },
    terminalControlTaken: false,
  })
  return render(<View {...route} params={{ runId: 'run_1' }} />)
}

function attached(size = { cols: 80, rows: 24 }, resume_id = 'pty-incarnation-run') {
  act(() => {
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: true, ...size, resume_id }),
    })
  })
}

beforeEach(() => {
  StubSocket.install()
  useStore.setState({ runs: {} })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('terminal view', () => {
  it('steers by default and lets the user return to a mirror', () => {
    const view = mount()
    attached()

    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })
    expect(screen.getByText('Attached')).toBeDefined()
    expect(screen.getByText('Steering')).toBeDefined()

    fireEvent.click(screen.getByText('Steering'))
    attached()

    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
    expect(screen.getByText('Take control')).toBeDefined()
    view.unmount()
  })

  it('opens another member run as a mirror until they take control', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ member_id: bob.id })))
    attached()

    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
    expect(screen.getByText('Take control')).toBeDefined()
    expect(screen.queryByText('Steering')).toBeNull()
    view.unmount()
  })

  it('says what a mirror is until the member has taken control once', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ member_id: bob.id })))
    attached()

    const hint = 'Read-only mirror. Take control to type into the agent.'
    expect(screen.getByText(hint)).toBeDefined()

    // Asking is not being granted: the flag waits for the reattach's ack, so
    // a member the server refuses keeps the hint.
    fireEvent.click(screen.getByText('Take control'))
    expect(useStore.getState().terminalControlTaken).toBe(false)
    attached()

    expect(useStore.getState().terminalControlTaken).toBe(true)
    expect(screen.queryByText(hint)).toBeNull()
    view.unmount()
  })
  it('downgrades a displaced writer to a mirror without permanent denial', () => {
    const view = mount({}, { member_id: bob.id })
    attached()

    fireEvent.click(screen.getByText('Take control'))
    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onmessage?.({
        data: JSON.stringify({
          ok: true,
          cols: 80,
          rows: 24,
          has_control: true,
          control_generation: 7,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })
    expect(screen.getByText('Steering')).toBeDefined()

    act(() => StubSocket.last().onclose?.({ code: 1008, reason: 'control taken over' }))
    expect(screen.getByText('Take control')).toBeDefined()
    expect(screen.queryByText('You cannot steer this run.')).toBeNull()

    // The mirror can explicitly ask for control again; reopen clears the
    // bounded reconnect timer and starts this request immediately.
    fireEvent.click(screen.getByText('Take control'))
    act(() => StubSocket.last().onopen?.())
    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })
    view.unmount()
  })

  it('does not count an owner run automatic steer as taking control', () => {
    const view = mount()
    attached()

    // The owner's attach asks for write on its own and the server grants it.
    // Nobody pressed anything, so the hint is still owed to them on the first
    // run they only watch.
    expect(screen.getByText('Steering')).toBeDefined()
    expect(useStore.getState().terminalControlTaken).toBe(false)
    view.unmount()
  })

  it('keeps the mirror hint when the server refuses the request', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ member_id: bob.id })))
    attached()

    fireEvent.click(screen.getByText('Take control'))
    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: codeDenied, error: 'permission denied' }),
      })
    })

    expect(useStore.getState().terminalControlTaken).toBe(false)
    view.unmount()
  })

  it('keeps a live stalled run in steering mode', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ status: 'needs-attention' })))
    attached()

    expect(screen.getByText('Steering')).toBeDefined()
    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })
    view.unmount()
  })

  it('keeps one control session across live status transitions', () => {
    const view = mount()
    attached()
    const socket = StubSocket.last()
    const opened = StubSocket.opened.length

    act(() => useStore.getState().upsertRun(run({ status: 'needs-attention' })))
    expect(StubSocket.opened).toHaveLength(opened)
    expect(StubSocket.last()).toBe(socket)

    act(() => useStore.getState().upsertRun(run({ status: 'running' })))
    expect(StubSocket.opened).toHaveLength(opened)
    expect(StubSocket.last()).toBe(socket)
    view.unmount()
  })

  it('disables the toggle and says why when the server denies steering', () => {
    const view = mount()
    act(() => StubSocket.last().onopen?.())

    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: codeDenied,
          error: 'run.attach: permission denied',
        }),
      }),
    )

    const toggle = screen.getByText('Take control') as HTMLButtonElement
    expect(toggle.disabled).toBe(true)
    expect(screen.getByText('You cannot steer this run.')).toBeDefined()
    view.unmount()
  })

  it('starts every attach from the server, not from the last one', () => {
    // What a previous visit to this tab left behind: a steer denial, refusal,
    // and message. A fresh attach must ask to steer and answer for itself.
    const view = mount({
      steerDenied: true,
      refused: true,
      message: 'no live terminal',
    })
    attached()

    const toggle = screen.getByText('Steering') as HTMLButtonElement
    expect(toggle.disabled).toBe(false)
    expect(screen.queryByText('no live terminal')).toBeNull()
    expect(screen.queryByText('Retry')).toBeNull()
    view.unmount()
  })

  it('writes PTY output frames into the terminal', () => {
    // The blank-terminal regression: the attach delivered frames but the
    // view never handed them to xterm, so the pane stayed empty forever.
    const write = vi.spyOn(Terminal.prototype, 'write')
    const view = mount()
    attached()

    const chunk = new TextEncoder().encode('agent says hi').buffer
    act(() => StubSocket.last().onmessage?.({ data: chunk }))

    const written = write.mock.calls.map(([data]) =>
      typeof data === 'string' ? data : new TextDecoder().decode(data),
    )
    expect(written).toContain('agent says hi')
    write.mockRestore()
    view.unmount()
  })
  it('does not initialize a cached run until its first active visit', () => {
    const view = mount({}, {}, { active: false })
    expect(StubSocket.opened).toHaveLength(0)

    const View = terminalRoute()
    view.rerender(<View params={{ runId: 'run_1' }} active />)
    expect(StubSocket.opened).toHaveLength(1)
    view.unmount()
  })
  it('unmounts parked Run Dock, Run Room, and run header while retaining the primary pane', async () => {
    const view = mount()
    attached()
    await waitFor(() => expect(document.querySelector('.xterm')).toBeDefined())
    expect(screen.getByRole('button', { name: 'Open Run Room' })).toBeDefined()
    expect(screen.getByRole('region', { name: 'Terminal dock' })).toBeDefined()
    expect(screen.getByRole('tablist', { name: 'Run tabs' })).toBeDefined()
    expect(screen.getByRole('tabpanel')).toBeDefined()
    const pane = document.querySelector('.xterm')

    const View = terminalRoute()
    view.rerender(<View params={{ runId: 'run_1' }} active={false} />)

    expect(screen.queryByRole('button', { name: 'Open Run Room' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Terminal dock' })).toBeNull()
    expect(screen.queryByRole('tablist', { name: 'Run tabs' })).toBeNull()
    expect(screen.queryByRole('tabpanel')).toBeNull()
    expect(pane?.isConnected).toBe(true)

    view.rerender(<View params={{ runId: 'run_1' }} active />)
    expect(screen.getByRole('button', { name: 'Open Run Room' })).toBeDefined()
    expect(screen.getByRole('region', { name: 'Terminal dock' })).toBeDefined()
    expect(screen.getByRole('tablist', { name: 'Run tabs' })).toBeDefined()
    expect(screen.getByRole('tabpanel')).toBeDefined()
    view.unmount()
  })

  it('parks a live run without a hidden socket and resumes its parsed output', async () => {
    const view = mount()
    attached({ cols: 20, rows: 4 })
    const pane = () => document.querySelector('.xterm-rows')?.textContent ?? ''
    act(() =>
      StubSocket.last().onmessage?.({
        data: new TextEncoder().encode('retained output').buffer,
      }),
    )
    await vi.waitFor(() => expect(pane()).toContain('retained output'))

    const socket = StubSocket.last()
    const weight = vi.fn()
    const View = terminalRoute()
    view.rerender(
      <View
        params={{ runId: 'run_1' }}
        active={false}
        onTerminalWeight={weight}
      />,
    )

    expect(socket.closed).toBe(true)
    expect(StubSocket.opened).toHaveLength(1)
    expect(weight).toHaveBeenCalledWith(expect.any(Number))
    expect(weight.mock.calls[0][0]).toBeGreaterThan(0)

    view.rerender(
      <View
        params={{ runId: 'run_1' }}
        active
        onTerminalWeight={weight}
      />,
    )
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const resumed = StubSocket.last()
    act(() => {
      resumed.onopen?.()
      resumed.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          cols: 20,
          rows: 4,
          resumed: true,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })
    expect(resumed.frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-incarnation-run',
    })
    expect(pane()).toContain('retained output')
    view.unmount()
  })
  it('reports retained normal and alternate buffer weight', async () => {
    const open = vi.spyOn(Terminal.prototype, 'open')
    const view = mount()
    const terminal = open.mock.contexts[0] as Terminal
    attached({ cols: 20, rows: 4 })
    await new Promise<void>((done) => terminal.write('normal output', done))
    await new Promise<void>((done) => terminal.write('\x1b[?1049halt output', done))

    const weight = vi.fn()
    const View = terminalRoute()
    view.rerender(
      <View
        params={{ runId: 'run_1' }}
        active={false}
        onTerminalWeight={weight}
      />,
    )

    const expected = (terminal.buffer.normal.length + terminal.buffer.alternate.length) * terminal.cols
    expect(terminal.buffer.normal.length).toBeGreaterThan(0)
    expect(terminal.buffer.alternate.length).toBeGreaterThan(0)
    expect(weight).toHaveBeenCalledWith(expected)
    open.mockRestore()
    view.unmount()
  })
  it('reports the larger weight when a deferred live multiline write settles while parked', async () => {
    const originalWrite = Terminal.prototype.write
    const write = vi.spyOn(Terminal.prototype, 'write')
    write.mockImplementation(function (this: Terminal, chunk, done) {
      if (chunk instanceof Uint8Array && done) {
        // Let xterm parse the bytes, but defer the completion observed by the
        // attach until the next turn so parking can happen first.
        return originalWrite.call(this, chunk, () => setTimeout(done, 0))
      }
      return originalWrite.call(this, chunk, done)
    })

    const view = mount()
    attached({ cols: 20, rows: 4 })
    const weight = vi.fn()
    const View = terminalRoute()
    const socket = StubSocket.last()
    act(() => {
      socket.onmessage?.({
        data: new TextEncoder().encode('line one\nline two\nline three').buffer,
      })
      view.rerender(
        <View
          params={{ runId: 'run_1' }}
          active={false}
          onTerminalWeight={weight}
        />,
      )
    })

    const terminal = write.mock.instances.find(
      (instance): instance is Terminal => instance instanceof Terminal,
    )
    if (!terminal) throw new Error('xterm terminal did not receive the live write')
    const parkedCalls = weight.mock.calls.length
    await waitFor(() => expect(weight.mock.calls.length).toBeGreaterThan(parkedCalls))
    const expected = (terminal.buffer.normal.length + terminal.buffer.alternate.length) * terminal.cols
    expect(weight.mock.calls.at(-1)?.[0]).toBe(expected)
    write.mockRestore()
    view.unmount()
  })
  it('does not reopen an ended completed run when its cached view is revisited', () => {
    const view = mount({}, { status: 'completed' })
    attached()
    const socket = StubSocket.last()
    act(() => socket.onclose?.({ code: 1000, reason: 'session ended' }))

    const View = terminalRoute()
    view.rerender(<View params={{ runId: 'run_1' }} active={false} />)
    view.rerender(<View params={{ runId: 'run_1' }} active />)

    expect(StubSocket.opened).toHaveLength(1)
    view.unmount()
  })
  it('full-attaches an active nonowner mirror after a same-run relaunch', async () => {
    const view = mount({}, { status: 'completed', member_id: bob.id })
    attached(undefined, 'pty-incarnation-ended')
    const endedSocket = StubSocket.last()
    act(() => endedSocket.onclose?.({ code: 1000, reason: 'session ended' }))

    act(() => useStore.getState().upsertRun(run({ status: 'running', member_id: bob.id })))
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))

    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    expect(replacement.frames()[0]).not.toHaveProperty('cursor')
    view.unmount()
  })

  it('records a parked same-run relaunch and full-attaches only on activation', async () => {
    const view = mount({}, { status: 'completed', member_id: bob.id })
    attached(undefined, 'pty-incarnation-ended')
    const endedSocket = StubSocket.last()
    act(() => endedSocket.onclose?.({ code: 1000, reason: 'session ended' }))

    const View = terminalRoute()
    view.rerender(<View params={{ runId: 'run_1' }} active={false} />)
    act(() => useStore.getState().upsertRun(run({ status: 'running', member_id: bob.id })))
    expect(StubSocket.opened).toHaveLength(1)

    view.rerender(<View params={{ runId: 'run_1' }} active />)
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    expect(replacement.frames()[0]).not.toHaveProperty('cursor')
    view.unmount()
  })

  it('coalesces a relaunch with a simultaneous phone follow change', async () => {
    const resize = atViewport(1024, { height: 844, pointer: 'coarse' })
    const view = mount({}, { status: 'completed' })
    attached(undefined, 'pty-incarnation-ended')
    const endedSocket = StubSocket.last()
    act(() => {
      endedSocket.onclose?.({ code: 1000, reason: 'session ended' })
      resize(390)
      useStore.getState().upsertRun(run({ status: 'running' }))
    })

    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    replacement.onopen?.()
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    expect(replacement.frames()[0]).toMatchObject({ follow: true })
    view.unmount()
  })

  it('keeps an in-flight replay hidden and requests a full replay after parking', async () => {
    const write = vi.spyOn(Terminal.prototype, 'write')
    const callbacks: Array<() => void> = []
    write.mockImplementation((chunk, done) => {
      if (chunk instanceof Uint8Array && done) callbacks.push(done)
    })
    const view = mount({}, { status: 'completed' })
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 3,
          cols: 80,
          rows: 24,
          resume_id: 'pty-incarnation-run',
        }),
      })
      socket.onmessage?.({ data: new TextEncoder().encode('old').buffer })
    })
    const host = document.querySelector('.min-h-0.flex-1.bg-background') as HTMLElement
    expect(callbacks).toHaveLength(1)
    expect(host.style.visibility).toBe('hidden')
    const View = terminalRoute()
    view.rerender(<View params={{ runId: 'run_1' }} active={false} />)
    expect(host.style.visibility).toBe('hidden')
    view.rerender(<View params={{ runId: 'run_1' }} active />)
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    act(() => replacement.onopen?.())
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    write.mockRestore()
    view.unmount()
  })

  it('disposes the attachment and xterm when the route is evicted', () => {
    const dispose = vi.spyOn(Terminal.prototype, 'dispose')
    const view = mount()
    attached()
    const socket = StubSocket.last()
    view.unmount()

    expect(socket.closed).toBe(true)
    expect(dispose).toHaveBeenCalled()
    dispose.mockRestore()
  })

  it('resets cached output and invalidates it on a final refusal', async () => {
    const reset = vi.spyOn(Terminal.prototype, 'reset')
    const invalidate = vi.fn()
    const view = mount({}, {}, { onTerminalInvalidate: invalidate })
    attached()
    const pane = () => document.querySelector('.xterm-rows')?.textContent ?? ''
    act(() =>
      StubSocket.last().onmessage?.({
        data: new TextEncoder().encode('stale output').buffer,
      }),
    )
    await vi.waitFor(() => expect(pane()).toContain('stale output'))

    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32000, error: 'final refusal' }),
      })
    })

    expect(reset).toHaveBeenCalled()
    expect(invalidate).toHaveBeenCalledTimes(1)
    expect(pane()).not.toContain('stale output')
    reset.mockRestore()
    view.unmount()
  })


  it('draws a desktop replay and live redraw at the shared PTY geometry', async () => {
    const open = vi.spyOn(Terminal.prototype, 'open')
    const view = mount()
    const terminal = open.mock.contexts[0] as Terminal
    attached({ cols: 20, rows: 4 })
    act(() => StubSocket.last().onmessage?.({
      data: new TextEncoder().encode(`${'a'.repeat(20)}B`).buffer,
    }))
    await vi.waitFor(() => expect(terminal.buffer.active.getLine(1)?.translateToString().trimEnd()).toBe('B'))

    act(() => {
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ type: 'geometry', cols: 30, rows: 4 }),
      })
      StubSocket.last().onmessage?.({
        data: new TextEncoder().encode(`\x1b[3;1H${'c'.repeat(30)}D`).buffer,
      })
    })
    await vi.waitFor(() => expect(terminal.buffer.active.getLine(3)?.translateToString().trimEnd()).toBe('D'))
    expect(terminal.buffer.active.getLine(2)?.translateToString().trimEnd()).toBe('c'.repeat(30))
    view.unmount()
    open.mockRestore()
  })

  it('keeps a completed session hidden until the final replay callback after going offline', async () => {
    const originalWrite = Terminal.prototype.write
    const write = vi.spyOn(Terminal.prototype, 'write')
    const callbacks: Array<() => void> = []
    write.mockImplementation(function (this: Terminal, chunk, done) {
      if (chunk instanceof Uint8Array && done) {
        callbacks.push(done)
        return
      }
      return originalWrite.call(this, chunk, done)
    })
    const view = mount({}, { status: 'completed' })
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 3,
          cols: 80,
          rows: 24,
          has_control: true,
          control_generation: 1,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })

    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    const host = document.querySelector('.min-h-0.flex-1.bg-background') as HTMLElement
    expect(host.style.visibility).toBe('hidden')

    act(() => socket.onmessage?.({ data: new TextEncoder().encode('old').buffer }))
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    expect(callbacks).toHaveLength(1)

    // A completed session parks offline after the exact replay boundary, but
    // the pane stays hidden while xterm parses the final replay frame.
    act(() => socket.onclose?.({ code: 1000, reason: 'session ended' }))
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    expect(host.style.visibility).toBe('hidden')

    act(() => callbacks[0]?.())
    await waitFor(() =>
      expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull(),
    )
    expect(host.style.visibility).toBe('')
    write.mockRestore()
    view.unmount()
  })
  it('finishes an ended replay after the route is parked and reveals it on revisit', async () => {
    const originalWrite = Terminal.prototype.write
    const write = vi.spyOn(Terminal.prototype, 'write')
    const callbacks: Array<() => void> = []
    write.mockImplementation(function (this: Terminal, chunk, done) {
      if (chunk instanceof Uint8Array && done) {
        callbacks.push(done)
        return
      }
      return originalWrite.call(this, chunk, done)
    })
    const view = mount({}, { status: 'completed' })
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 3,
          cols: 80,
          rows: 24,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    act(() => socket.onmessage?.({ data: new TextEncoder().encode('old').buffer }))
    act(() => socket.onclose?.({ code: 1000, reason: 'session ended' }))

    expect(callbacks).toHaveLength(1)
    const host = document.querySelector('.min-h-0.flex-1.bg-background') as HTMLElement
    expect(host.style.visibility).toBe('hidden')
    const View = terminalRoute()
    view.rerender(<View params={{ runId: 'run_1' }} active={false} />)
    expect(host.style.visibility).toBe('hidden')

    act(() => callbacks[0]?.())
    await waitFor(() =>
      expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull(),
    )
    view.rerender(<View params={{ runId: 'run_1' }} active />)
    expect(host.style.visibility).toBe('')
    write.mockRestore()
    view.unmount()
  })
  it('reveals a completed session when an offline close aborts before replay ends', () => {
    const view = mount({}, { status: 'completed' })
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 3,
          cols: 80,
          rows: 24,
          has_control: true,
          control_generation: 1,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })

    const host = document.querySelector('.min-h-0.flex-1.bg-background') as HTMLElement
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    expect(host.style.visibility).toBe('hidden')

    // No replay-end frame arrived: the close's explicit replay abort settles
    // the gate instead of leaving an incomplete transcript latched.
    act(() => {
      socket.onmessage?.({ data: new TextEncoder().encode('ol').buffer })
      socket.onclose?.({ code: 1000, reason: 'session ended' })
    })

    expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull()
    expect(host.style.visibility).toBe('')
    view.unmount()
  })
  it('settles the hidden replay overlay when wake replacement is refused', () => {
    const view = mount({}, { status: 'completed' })
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 3,
          cols: 80,
          rows: 24,
          has_control: true,
          control_generation: 1,
          resume_id: 'pty-incarnation-run',
        }),
      })
      socket.onmessage?.({ data: new TextEncoder().encode('ol').buffer })
    })

    act(() => fire('online'))
    const replacement = StubSocket.last()
    expect(replacement).not.toBe(socket)
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    const host = document.querySelector('.min-h-0.flex-1.bg-background') as HTMLElement
    expect(host.style.visibility).toBe('hidden')
    act(() => {
      replacement.onopen?.()
      replacement.onmessage?.({
        data: JSON.stringify({ ok: false, code: -32002, error: 'replacement refused' }),
      })
    })

    expect(host.style.visibility).toBe('')
    expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull()
    expect(screen.getByText('replacement refused')).toBeDefined()
    view.unmount()
  })

  it('preserves existing terminal output when Steering is released with resume', async () => {
    const reset = vi.spyOn(Terminal.prototype, 'reset')
    const write = vi.spyOn(Terminal.prototype, 'write')
    const view = mount()
    attached({ cols: 20, rows: 4 })
    const pane = () => document.querySelector('.xterm-rows')?.textContent ?? ''

    act(() =>
      StubSocket.last().onmessage?.({
        data: new TextEncoder().encode('existing output').buffer,
      }),
    )
    await vi.waitFor(() => expect(pane()).toContain('existing output'))
    const resetCount = reset.mock.calls.length
    const writeCount = write.mock.calls.length

    fireEvent.click(screen.getByText('Steering'))
    expect(StubSocket.opened).toHaveLength(2)
    const release = StubSocket.last()
    act(() => {
      release.onopen?.()
      release.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 20, rows: 4, resumed: true, resume_id: 'pty-incarnation-run' }),
      })
    })

    expect(reset.mock.calls).toHaveLength(resetCount)
    const writesAfterAck = write.mock.calls.slice(writeCount)
    expect(
      writesAfterAck.filter(([data]) => typeof data === 'string' ? data.length > 0 : data.byteLength > 0),
    ).toHaveLength(0)
    expect(pane()).toContain('existing output')
    act(() => {
      release.onmessage?.({
        data: new TextEncoder().encode('after release').buffer,
      })
    })
    await vi.waitFor(() => expect(pane()).toContain('after release'))
    expect(pane()).toContain('existing output')
    reset.mockRestore()
    write.mockRestore()
    view.unmount()
  })


  it('offers a retry when the attach is refused outright', () => {
    const view = mount()
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32000, error: 'unknown run' }),
      }),
    )

    expect(screen.getByText('unknown run')).toBeDefined()
    fireEvent.click(screen.getByText('Retry'))
    expect(StubSocket.opened).toHaveLength(2)
    view.unmount()
  })

  // Switching runs keeps the same view mounted, so the pane has to be cleared
  // by the switch: a finished run with no recorded terminal is refused, and a
  // refusal never acks, so it would show the previous run's output for good.
  it('clears the previous run before showing the next one', async () => {
    const view = mount()
    attached()
    const pane = () => document.querySelector('.xterm-rows')?.textContent ?? ''

    act(() =>
      StubSocket.last().onmessage?.({
        data: new TextEncoder().encode('run one output').buffer,
      }),
    )
    await vi.waitFor(() => expect(pane()).toContain('run one output'))

    act(() => useStore.getState().upsertRun(run({ id: 'run_2', status: 'merged' })))
    const View = terminalRoute()
    view.rerender(<View params={{ runId: 'run_2' }} />)
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: -32004,
          error: 'ptyhost: no session for run',
        }),
      }),
    )

    expect(pane()).not.toContain('run one output')
    view.unmount()
  })

  // The switch has to land even while the previous run is still draining:
  // xterm parses in slices, and a reset leaves whatever is already queued to
  // arrive after it.
  it('shows nothing of the previous run when the switch lands mid-replay', async () => {
    const view = mount()
    attached()
    const pane = () => document.querySelector('.xterm-rows')?.textContent ?? ''

    const chunk = new TextEncoder().encode('RUN-ONE-OUTPUT '.repeat(4000)).buffer
    for (let i = 0; i < 16; i++) {
      act(() => StubSocket.last().onmessage?.({ data: chunk }))
    }

    const View = terminalRoute()
    act(() => useStore.getState().upsertRun(run({ id: 'run_2', status: 'merged' })))
    view.rerender(<View params={{ runId: 'run_2' }} />)
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: -32004,
          error: 'ptyhost: no session for run',
        }),
      }),
    )

    await new Promise((done) => setTimeout(done, 200))
    expect(pane()).not.toContain('RUN-ONE-OUTPUT')
    view.unmount()
  })

  it.each(['queued', 'provisioning'] as const)(
    'waits for the container instead of attaching to a %s run',
    (status) => {
      const view = mount({}, { status })

      expect(StubSocket.opened).toHaveLength(0)
      expect(screen.getByText("Starting the run's container")).toBeDefined()
      expect(screen.queryByText('Offline')).toBeNull()
      expect(screen.queryByText('Retry')).toBeNull()
      expect(screen.queryByText('This run is not running')).toBeNull()
      view.unmount()
    },
  )

  it('attaches as soon as the container is up', () => {
    const view = mount({}, { status: 'provisioning' })
    expect(StubSocket.opened).toHaveLength(0)

    act(() => useStore.getState().upsertRun(run({ status: 'running' })))
    attached()

    expect(StubSocket.opened).toHaveLength(1)
    expect(screen.queryByText("Starting the run's container")).toBeNull()
    expect(screen.getByText('Attached')).toBeDefined()
    view.unmount()
  })

  it('drops the container spinner when the run fails while provisioning', () => {
    const view = mount({}, { status: 'provisioning' })
    expect(StubSocket.opened).toHaveLength(0)

    act(() =>
      useStore.getState().upsertRun(
        run({
          status: 'failed',
          started_at: null,
          reason: 'provisioning: create checkout: no space left',
        }),
      ),
    )
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32004, error: 'ptyhost: no session for run' }),
      }),
    )

    expect(screen.queryByText("Starting the run's container")).toBeNull()
    expect(screen.getByText('provisioning: create checkout: no space left')).toBeDefined()
    expect(screen.queryByText('Retry')).toBeNull()
    view.unmount()
  })

  it('waits out a missing session on a running run rather than failing it', () => {
    const view = mount()
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32004, error: 'ptyhost: no session for run' }),
      }),
    )

    expect(screen.queryByText('Offline')).toBeNull()
    expect(screen.queryByText('ptyhost: no session for run')).toBeNull()
    expect(screen.queryByText('Retry')).toBeNull()
    view.unmount()
  })

  // The reader is standing on the Terminal tab when a launch fails, and a
  // Retry that can never succeed is the dead end this ticket is about.
  it('gives a run that died before it started its reason, not a retry', () => {
    const view = mount(
      {},
      { status: 'failed', started_at: null, reason: 'provisioning: create checkout: no space left' },
    )
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32004, error: 'ptyhost: no session for run' }),
      }),
    )

    expect(
      screen.getByText('provisioning: create checkout: no space left'),
    ).toBeDefined()
    expect(screen.queryByText('Retry')).toBeNull()
    view.unmount()
  })

  // A refusal that is not a missing session says nothing about the run, so
  // the tab must not answer it with a sentence about the run.
  it('shows the gateway refusal itself when the session is not the problem', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ status: 'completed' })))
    act(() => StubSocket.last().onopen?.())
    act(() => StubSocket.last().onclose?.({ code: 1008 }))

    expect(screen.getByText('the gateway refused the attach')).toBeDefined()
    expect(
      screen.queryByText('This run has ended and left no recorded terminal to replay.'),
    ).toBeNull()
    view.unmount()
  })

  it('says a completed run has ended instead of echoing the refusal', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ status: 'completed' })))
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: -32004,
          error: 'ptyhost: no session for run',
        }),
      }),
    )

    expect(
      screen.getByText('This run has ended and left no recorded terminal to replay.'),
    ).toBeDefined()
    expect(screen.queryByText('ptyhost: no session for run')).toBeNull()
    view.unmount()
  })

  // A run waiting on its supervisor has not ended: it goes back to running,
  // so telling it that it ended and taking its retry away is the dead end.
  it('lets a stalled run be steered without calling it not running', () => {
    const view = mount({}, { status: 'needs-attention' })
    attached()

    const toggle = screen.getByText('Steering') as HTMLButtonElement
    expect(toggle.disabled).toBe(false)
    expect(screen.queryByText('This run is not running')).toBeNull()
    view.unmount()
  })

  // A run waiting on a human is not a finished one: the server keeps it on
  // the live side of its own replay gate, so the tab must not answer its
  // refusals with a sentence about a transcript, or take the retry away.
  it('keeps the retry on a run that is only waiting for attention', () => {
    const view = mount({}, { status: 'needs-attention' })
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32000, error: 'unknown run' }),
      }),
    )

    expect(screen.getByText('unknown run')).toBeDefined()
    expect(
      screen.queryByText('This run has ended and left no recorded terminal to replay.'),
    ).toBeNull()
    fireEvent.click(screen.getByText('Retry'))
    expect(StubSocket.opened).toHaveLength(2)
    view.unmount()
  })
})

// A phone renders the session the way the writers see it and keeps its own
// width to itself. See the terminal section of docs/dashboard-frontend.md.
describe('the terminal on a phone', () => {
  const phone = () => atViewport(390, { height: 844, pointer: 'coarse' })

  it('follows the session geometry and sends no size of its own', async () => {
    phone()
    const resize = vi.spyOn(Terminal.prototype, 'resize')
    const view = mount()
    attached({ cols: 132, rows: 43 })

    // The flag is what keeps this client out of the minimum the PTY is
    // sized to; the geometry beside it is only what a session with no PTY
    // of its own - a finished run's replay - is laid out at.
    expect(StubSocket.last().frames()[0]).toEqual({
      follow: true,
      cols: 80,
      rows: 24,
      control_session_id: expect.any(String),
    })
    await vi.waitFor(() => expect(resize).toHaveBeenCalledWith(132, 43))
    expect(StubSocket.last().frames().some((frame) => 'type' in (frame as object))).toBe(
      false,
    )

    // Someone with a bigger screen resizes the session: the phone redraws
    // at what the server reports rather than at what its ack once said.
    act(() => {
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ type: 'geometry', cols: 120, rows: 40 }),
      })
    })
    await vi.waitFor(() => expect(resize).toHaveBeenCalledWith(120, 40))
    resize.mockRestore()
    view.unmount()
  })

  it('keeps following while it steers, which is what the flag is for', () => {
    phone()
    const view = mount()
    attached({ cols: 132, rows: 43 })

    fireEvent.click(screen.getByText('Take control'))
    attached({ cols: 132, rows: 43 })

    expect(StubSocket.last().frames()[0]).toEqual({
      write: true,
      follow: true,
      resume: true,
      resume_id: 'pty-incarnation-run',
      cursor: 0,
      cols: 80,
      rows: 24,
      control_session_id: expect.any(String),
    })
    view.unmount()
  })

  it('opens the member own run as a mirror that cannot be typed into', async () => {
    phone()
    const view = mount()
    attached()

    // On a desktop this run auto-steers; here taking control is a decision.
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
    expect(screen.getByText('Take control')).toBeDefined()
    // A read-only textarea is what keeps a tap from raising the keyboard.
    const input = () =>
      document.querySelector('.xterm-helper-textarea') as HTMLTextAreaElement | null
    await vi.waitFor(() => expect(input()?.readOnly).toBe(true))
    expect(screen.queryByRole('toolbar', { name: 'Terminal keys' })).toBeNull()

    fireEvent.click(screen.getByText('Take control'))
    attached()

    await vi.waitFor(() => expect(input()?.readOnly).toBe(false))
    // The keys a soft keyboard has not got arrive with the ability to type.
    expect(screen.getByRole('toolbar', { name: 'Terminal keys' })).toBeDefined()
    view.unmount()
  })
  it('reattaches once when a desktop owner crosses the phone breakpoint', async () => {
    const resize = atViewport(1024, { height: 844, pointer: 'coarse' })
    const view = mount()
    attached()
    expect(StubSocket.opened).toHaveLength(1)

    resize(390)
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const reopened = StubSocket.last()
    act(() => {
      reopened.onopen?.()
      reopened.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          cols: 80,
          rows: 24,
          resumed: true,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })

    expect(reopened.frames()).toHaveLength(1)
    expect(reopened.frames()[0]).toMatchObject({ follow: true, resume: true })
    view.unmount()
  })

  it('reattaches on a phone follow change even after an explicit mirror choice', async () => {
    const resize = atViewport(1024, { height: 844, pointer: 'coarse' })
    const view = mount()
    attached()

    fireEvent.click(screen.getByText('Steering'))
    const released = StubSocket.last()
    act(() => {
      released.onopen?.()
      released.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          cols: 80,
          rows: 24,
          resumed: true,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })
    expect(StubSocket.opened).toHaveLength(2)

    resize(390)
    await waitFor(() => expect(StubSocket.opened).toHaveLength(3))
    const reopened = StubSocket.last()
    act(() => reopened.onopen?.())
    expect(reopened.frames()).toHaveLength(1)
    expect(reopened.frames()[0]).toMatchObject({ follow: true, resume: true })
    expect(reopened.frames()[0]).not.toHaveProperty('write')
    view.unmount()
  })
})

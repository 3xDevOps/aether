import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import * as presentation from '@/components/terminal-presentation'
import type * as apiModule from '@/lib/api'
import type { Run } from '@/lib/types'
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
) {
  const View = terminalRoute()
  useStore.getState().upsertRun(run(over))
  useStore.setState({
    info: serverInfo,
    terminals: { run_1: { ...initialTerminal, ...seed } },
    terminalControlTaken: false,
  })
  return render(<View params={{ runId: 'run_1' }} />)
}

function attached(
  size = { cols: 80, rows: 24 },
  resume_id = 'pty-incarnation-run',
  over: Record<string, unknown> = {},
) {
  act(() => {
    const socket = StubSocket.last()
    socket.onopen?.()
    const header = socket.frames()[0]
    const hasControl =
      typeof header === 'object' &&
      header !== null &&
      'write' in header &&
      header.write === true
    socket.onmessage?.({
      data: JSON.stringify({
        ok: true,
        ...size,
        resume_id,
        has_control: hasControl,
        control_generation: hasControl ? 1 : 0,
        ...over,
      }),
    })
  })
}

function controlAck(
  has_control: boolean,
  request_id: number,
  control_generation: number,
  over: Record<string, unknown> = {},
) {
  act(() => {
    StubSocket.last().onmessage?.({
      data: JSON.stringify({
        type: 'control',
        request_id,
        ok: true,
        has_control,
        control_generation,
        ...over,
      }),
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
    expect(StubSocket.opened).toHaveLength(1)
    expect(useStore.getState().terminals.run_1.write).toBe(true)
    expect(StubSocket.last().frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: false })
    controlAck(false, 1, 2)

    expect(useStore.getState().terminals.run_1.write).toBe(false)
    expect(screen.getByText('Take control')).toBeDefined()
    view.unmount()
  })

  it('waits for a control acknowledgement without replacing the output socket', () => {
    const view = mount({}, { member_id: bob.id })
    attached()
    const socket = StubSocket.last()
    const opened = StubSocket.opened.length

    fireEvent.click(screen.getByText('Take control'))
    expect(StubSocket.opened).toHaveLength(opened)
    expect(useStore.getState().terminals.run_1.write).toBe(false)
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: true })
    controlAck(true, 1, 1)

    expect(useStore.getState().terminals.run_1.write).toBe(true)
    expect(screen.getByText('Steering')).toBeDefined()

    fireEvent.click(screen.getByText('Steering'))
    expect(StubSocket.opened).toHaveLength(opened)
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 2, write: false })
    controlAck(false, 2, 1)
    fireEvent.click(screen.getByText('Take control'))
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 3, write: true })
    expect(socket.frames().at(-1)).not.toHaveProperty('control_generation')
    view.unmount()
  })


  it('opens another member run as a mirror until they take control', () => {
    const view = mount({}, { member_id: bob.id })
    attached()

    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
    expect(screen.getByText('Take control')).toBeDefined()
    expect(screen.queryByText('Steering')).toBeNull()
    view.unmount()
  })

  it('says what a mirror is until the member has taken control once', () => {
    const view = mount({}, { member_id: bob.id })
    attached()

    const hint = 'Read-only mirror. Take control to type into the agent.'
    expect(screen.getByText(hint)).toBeDefined()

    // Asking is not being granted optimistically: the flag changes only after
    // the ordered control response acknowledges the lease.
    fireEvent.click(screen.getByText('Take control'))
    expect(useStore.getState().terminalControlTaken).toBe(false)
    expect(StubSocket.last().frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: true })
    controlAck(true, 1, 1)

    expect(useStore.getState().terminalControlTaken).toBe(true)
    expect(screen.queryByText(hint)).toBeNull()
    view.unmount()
  })
  it('downgrades a displaced writer to a mirror without permanent denial', () => {
    const view = mount({}, { member_id: bob.id })
    attached()

    fireEvent.click(screen.getByText('Take control'))
    controlAck(true, 1, 1)
    expect(screen.getByText('Steering')).toBeDefined()

    act(() => StubSocket.last().onclose?.({ code: 1008, reason: 'control taken over' }))
    expect(screen.getByText('Take control')).toBeDefined()
    expect(screen.queryByText('You cannot steer this run.')).toBeNull()

    // The mirror remains eligible for a later explicit request; no
    // permission-denial latch is set by lease displacement.
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
    const view = mount({}, { member_id: bob.id })
    attached()

    fireEvent.click(screen.getByText('Take control'))
    expect(StubSocket.last().frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: true })
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({
          type: 'control',
          request_id: 1,
          ok: false,
          code: codeDenied,
          error: 'permission denied',
          has_control: false,
          control_generation: 1,
        }),
      }),
    )

    expect(useStore.getState().terminalControlTaken).toBe(false)
    expect(screen.getByText('You cannot steer this run.')).toBeDefined()
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

  it('retries control after unmounting and revisiting with new authority', async () => {
    let view = mount({}, { member_id: bob.id })
    attached()

    fireEvent.click(screen.getByText('Take control'))
    const denied = StubSocket.last()
    act(() =>
      denied.onmessage?.({
        data: JSON.stringify({
          type: 'control',
          request_id: 1,
          ok: false,
          code: codeDenied,
          error: 'run.attach: permission denied',
          has_control: false,
          control_generation: 1,
        }),
      }),
    )
    expect((screen.getByText('Take control') as HTMLButtonElement).disabled).toBe(true)

    view.unmount()
    act(() => useStore.getState().upsertRun(run()))
    const View = terminalRoute()
    view = render(<View params={{ runId: 'run_1' }} />)

    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const authorized = StubSocket.last()
    act(() => authorized.onopen?.())
    expect(authorized.frames()[0]).toMatchObject({ write: true, screen: true })
    expect(authorized.frames()[0]).not.toHaveProperty('resume')
    expect(screen.queryByText('You cannot steer this run.')).toBeNull()
    view.unmount()
  })
  it('retries control on an authority change without replacing the stream', () => {
    const view = mount()
    const socket = StubSocket.last()
    attached()

    fireEvent.click(screen.getByText('Steering'))
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: false })
    controlAck(false, 1, 1)

    fireEvent.click(screen.getByText('Take control'))
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 2, write: true })
    act(() =>
      socket.onmessage?.({
        data: JSON.stringify({
          type: 'control',
          request_id: 2,
          ok: false,
          code: codeDenied,
          error: 'run.attach: permission denied',
          has_control: false,
          control_generation: 0,
        }),
      }),
    )
    expect((screen.getByText('Take control') as HTMLButtonElement).disabled).toBe(true)

    act(() =>
      useStore.setState({
        info: {
          ...serverInfo,
          member: { ...serverInfo.member, role: 'collaborator' },
        },
      }),
    )

    expect(StubSocket.opened).toHaveLength(1)
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 3, write: true })
    controlAck(true, 3, 2)
    expect(screen.queryByText('You cannot steer this run.')).toBeNull()
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

  it('writes PTY output frames into the terminal', async () => {
    // The blank-terminal regression: the attach delivered frames but the
    // view never handed them to xterm, so the pane stayed empty forever.
    const write = vi.spyOn(Terminal.prototype, 'write')
    const view = mount()
    attached()

    const chunk = new TextEncoder().encode('agent says hi').buffer
    act(() => StubSocket.last().onmessage?.({ data: chunk }))

    await waitFor(() => {
      const written = write.mock.calls.map(([data]) =>
        typeof data === 'string' ? data : new TextDecoder().decode(data),
      )
      expect(written).toContain('agent says hi')
    })
    write.mockRestore()
    view.unmount()
  })
  it('initializes the terminal for a known mounted run', () => {
    const view = mount()
    const socket = StubSocket.last()

    expect(StubSocket.opened).toHaveLength(1)

    view.unmount()
    expect(socket.closed).toBe(true)
  })
  it('renders the complete route UI only while mounted', async () => {
    const view = mount()
    attached()
    await waitFor(() => expect(document.querySelector('.xterm')).toBeDefined())
    expect(screen.getByRole('button', { name: 'Open Run Room' })).toBeDefined()
    expect(screen.getByRole('region', { name: 'Terminal dock' })).toBeDefined()
    expect(screen.getByRole('tablist', { name: 'Run tabs' })).toBeDefined()
    expect(screen.getByRole('tabpanel')).toBeDefined()
    const pane = document.querySelector('.xterm')

    view.unmount()

    expect(screen.queryByRole('button', { name: 'Open Run Room' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Terminal dock' })).toBeNull()
    expect(screen.queryByRole('tablist', { name: 'Run tabs' })).toBeNull()
    expect(screen.queryByRole('tabpanel')).toBeNull()
    expect(pane?.isConnected).toBe(false)
  })

  it('blocks historical DOM input while answering live terminal queries and refocuses at the live end', async () => {
    vi.spyOn(presentation, 'captureTerminalPresentation').mockReturnValue({
      rows: ['<span>pinned output</span>', '<span>second row</span>'],
      cols: 80, viewportY: 0, baseY: 0, cellWidth: 8, cellHeight: 16,
      fontFamily: 'monospace', fontSize: 12, letterSpacing: 0,
    })
    const opened = vi.spyOn(Terminal.prototype, 'open')
    const view = mount()
    const terminal = opened.mock.contexts[0] as Terminal
    attached()
    const socket = StubSocket.last()
    const host = terminal.element!.parentElement!
    const input = terminal.textarea!
    // Saved-view hydration can finish before the attach's ordered geometry,
    // empty replay write and reveal frames have enabled DOM input.
    await waitFor(() => {
      expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull()
      expect(host.hasAttribute('inert')).toBe(false)
      expect(input.readOnly).toBe(false)
    })
    fireEvent.paste(input, { clipboardData: { getData: () => 'live input' } })
    expect(socket.frames()).toContainEqual(expect.objectContaining({ type: 'input', data: 'live input' }))

    fireEvent.wheel(host, { deltaY: -80 })
    const history = await screen.findByRole('region', { name: 'Terminal scrollback' })
    expect(screen.getByText('pinned output')).toBeDefined()
    expect(host.hasAttribute('inert')).toBe(true)
    expect(host.style.visibility).toBe('hidden')
    const sent = socket.sent.length
    fireEvent.keyDown(history, { key: 'x', code: 'KeyX', keyCode: 88 })
    fireEvent.paste(history, { clipboardData: { getData: () => 'historical input' } })
    fireEvent.keyDown(input, { key: 'x', code: 'KeyX', keyCode: 88 })
    fireEvent.paste(input, { clipboardData: { getData: () => 'hidden input' } })
    fireEvent.keyDown(history, { key: 'PageUp', code: 'PageUp' })
    expect(socket.sent).toHaveLength(sent)
    // These replies originate in the parser, not terminal.input()/paste().
    act(() => socket.onmessage?.({ data: new TextEncoder().encode('new live output\x1b[6n\x1b[c').buffer }))
    await waitFor(() => expect(terminal.buffer.active.getLine(0)?.translateToString()).toContain('new live output'))
    await waitFor(() => {
      expect(socket.frames()).toContainEqual(expect.objectContaining({ type: 'input', data: expect.stringMatching(/^\x1b\[\d+;\d+R$/) }))
      expect(socket.frames()).toContainEqual(expect.objectContaining({ type: 'input', data: expect.stringMatching(/^\x1b\[\?[\d;]+c$/) }))
    })
    expect(screen.getByText('pinned output')).toBeDefined()
    expect(socket.closed).toBe(false)

    fireEvent.keyDown(history, { key: 'End', code: 'End' })
    await waitFor(() => expect(document.activeElement).toBe(input))
    expect(host.hasAttribute('inert')).toBe(false)
    expect(host.style.visibility).toBe('')
    fireEvent.paste(input, { clipboardData: { getData: () => 'resumed input' } })
    expect(socket.frames()).toContainEqual(expect.objectContaining({ type: 'input', data: 'resumed input' }))
    view.unmount()
    expect(socket.closed).toBe(true)
  })

  it('unmounts a live run and remounts a fresh screen snapshot', async () => {
    let view = mount()
    attached({ cols: 20, rows: 4 })
    const pane = () => document.querySelector('.xterm-rows')?.textContent ?? ''
    act(() =>
      StubSocket.last().onmessage?.({
        data: new TextEncoder().encode('old output').buffer,
      }),
    )
    await vi.waitFor(() => expect(pane()).toContain('old output'))
    const oldPane = document.querySelector('.xterm')
    const socket = StubSocket.last()

    view.unmount()
    expect(socket.closed).toBe(true)
    expect(oldPane?.isConnected).toBe(false)

    const View = terminalRoute()
    view = render(<View params={{ runId: 'run_1' }} />)
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    act(() => replacement.onopen?.())
    expect(replacement.frames()[0]).toMatchObject({ screen: true })
    expect(replacement.frames()[0]).not.toHaveProperty('resume')

    act(() => {
      replacement.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 14,
          cols: 20,
          rows: 4,
          resume_id: 'pty-incarnation-run',
        }),
      })
      replacement.onmessage?.({ data: new TextEncoder().encode('current output').buffer })
    })
    await vi.waitFor(() => expect(pane()).toContain('current output'))
    expect(pane()).not.toContain('old output')
    view.unmount()
  })
  it('fresh-attaches an ended completed run when revisited', async () => {
    let view = mount({}, { status: 'completed' })
    attached()
    const socket = StubSocket.last()
    act(() => socket.onclose?.({ code: 1000, reason: 'session ended' }))

    view.unmount()
    const View = terminalRoute()
    view = render(<View params={{ runId: 'run_1' }} />)

    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    act(() => replacement.onopen?.())
    expect(replacement.frames()[0]).toMatchObject({ screen: true })
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    view.unmount()
  })
  it('does not reopen an ended completed run when follow mode changes', () => {
    const resize = atViewport(1024, { height: 844, pointer: 'coarse' })
    const view = mount({}, { status: 'completed' })
    attached()
    act(() => StubSocket.last().onclose?.({ code: 1000, reason: 'session ended' }))

    resize(390)

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

  it('full-attaches a same-run relaunch after an unmounted visit', async () => {
    let view = mount({}, { status: 'completed', member_id: bob.id })
    attached(undefined, 'pty-incarnation-ended')
    const endedSocket = StubSocket.last()
    act(() => endedSocket.onclose?.({ code: 1000, reason: 'session ended' }))

    view.unmount()
    act(() => useStore.getState().upsertRun(run({ status: 'running', member_id: bob.id })))
    const View = terminalRoute()
    view = render(<View params={{ runId: 'run_1' }} />)

    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    act(() => replacement.onopen?.())
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

  it('cancels an in-flight replay on unmount and requests a fresh replay on revisit', async () => {
    const write = vi.spyOn(Terminal.prototype, 'write')
    const callbacks: Array<() => void> = []
    write.mockImplementation((chunk, done) => {
      if (chunk instanceof Uint8Array && done) callbacks.push(done)
      else done?.()
    })
    let view = mount({}, { status: 'completed' })
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
    await waitFor(() => expect(callbacks).toHaveLength(1))
    expect(host.style.visibility).toBe('hidden')

    view.unmount()
    expect(socket.closed).toBe(true)
    act(() => callbacks[0]?.())

    const View = terminalRoute()
    view = render(<View params={{ runId: 'run_1' }} />)
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    act(() => replacement.onopen?.())
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    write.mockRestore()
    view.unmount()
  })

  it('disposes the attachment and xterm when the route unmounts', () => {
    const dispose = vi.spyOn(Terminal.prototype, 'dispose')
    const view = mount()
    attached()
    const socket = StubSocket.last()
    view.unmount()

    expect(socket.closed).toBe(true)
    expect(dispose).toHaveBeenCalled()
    dispose.mockRestore()
  })

  it('resets mounted output on a final refusal', async () => {
    const reset = vi.spyOn(Terminal.prototype, 'reset')
    const view = mount()
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
    await waitFor(() => {
      expect(reset).toHaveBeenCalled()
      expect(pane()).not.toContain('stale output')
    })
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
    await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
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
    const host = document.querySelector('.xterm')!.parentElement!
    expect(host.style.visibility).toBe('hidden')

    act(() => socket.onmessage?.({ data: new TextEncoder().encode('old').buffer }))
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    await waitFor(() => expect(callbacks).toHaveLength(1))

    // A completed session stays offline after the exact replay boundary, but
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
  it('cancels an ended replay on unmount and replays afresh on revisit', async () => {
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
    let view = mount({}, { status: 'completed' })
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
    await waitFor(() => expect(callbacks).toHaveLength(1))
    act(() => socket.onclose?.({ code: 1000, reason: 'session ended' }))

    view.unmount()
    act(() => callbacks[0]?.())
    const View = terminalRoute()
    view = render(<View params={{ runId: 'run_1' }} />)

    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const replacement = StubSocket.last()
    act(() => replacement.onopen?.())
    expect(replacement.frames()[0]).not.toHaveProperty('resume')
    write.mockRestore()
    view.unmount()
  })
  it('reveals a completed session when an offline close aborts before replay ends', async () => {
    const view = mount({}, { status: 'completed' })
    await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
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

    const host = document.querySelector('.xterm')!.parentElement!
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    expect(host.style.visibility).toBe('hidden')

    // No replay-end frame arrived: ending the session synthesizes the boundary
    // so reveal still waits for paint and structural completion.
    act(() => {
      socket.onmessage?.({ data: new TextEncoder().encode('ol').buffer })
      socket.onclose?.({ code: 1000, reason: 'session ended' })
    })

    await waitFor(() => {
      expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull()
      expect(host.style.visibility).toBe('')
    })
    view.unmount()
  })
  it('settles the hidden replay overlay when wake replacement is refused', async () => {
    const view = mount({}, { status: 'completed' })
    await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
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
    const host = document.querySelector('.xterm')!.parentElement!
    expect(host.style.visibility).toBe('hidden')
    act(() => {
      replacement.onopen?.()
      replacement.onmessage?.({
        data: JSON.stringify({ ok: false, code: -32002, error: 'replacement refused' }),
      })
    })

    await waitFor(() => {
      expect(host.style.visibility).toBe('')
      expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull()
    })
    expect(screen.getByText('replacement refused')).toBeDefined()
    view.unmount()
  })

  it('preserves existing terminal output when Steering is released', async () => {
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
    expect(StubSocket.opened).toHaveLength(1)
    const release = StubSocket.last()
    expect(release.frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: false })
    controlAck(false, 1, 2)

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
      screen: true,
      interactive: true,
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

    const socket = StubSocket.last()
    fireEvent.click(screen.getByText('Take control'))
    expect(StubSocket.opened).toHaveLength(1)
    expect(socket.frames()[0]).toMatchObject({ follow: true, cols: 80, rows: 24 })
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: true })
    controlAck(true, 1, 1)
    expect(screen.getByText('Steering')).toBeDefined()
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
    expect(StubSocket.last().frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: true })
    controlAck(true, 1, 1)
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
    expect(StubSocket.opened).toHaveLength(1)
    const released = StubSocket.last()
    expect(released.frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: false })
    controlAck(false, 1, 2)

    resize(390)
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const reopened = StubSocket.last()
    act(() => reopened.onopen?.())
    expect(reopened.frames()).toHaveLength(1)
    expect(reopened.frames()[0]).toMatchObject({ follow: true, resume: true })
    expect(reopened.frames()[0]).not.toHaveProperty('write')
    view.unmount()
  })
})

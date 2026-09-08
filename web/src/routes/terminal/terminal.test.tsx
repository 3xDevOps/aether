import { act, fireEvent, render, screen } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import { codeDenied } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { initialTerminal, type TerminalState } from '@/store/terminal'
import { bob, run, serverInfo } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// jsdom has no layout engine, so the fit addon has nothing to observe.
class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function terminalRoute() {
  const View = lookupRoute('terminal')
  if (!View) throw new Error('terminal route not registered')
  return View
}

function mount(seed: Partial<TerminalState> = {}) {
  const View = terminalRoute()
  useStore.getState().upsertRun(run())
  useStore.setState({
    info: serverInfo,
    terminals: { run_1: { ...initialTerminal, ...seed } },
    terminalControlTaken: false,
  })
  return render(<View params={{ runId: 'run_1' }} />)
}

function attached() {
  act(() => {
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: true, cols: 80, rows: 24 }),
    })
  })
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => {
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

<<<<<<< HEAD
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

  it('keeps replay-only finished runs out of steering mode', () => {
=======
  it('keeps a live stalled run in steering mode', () => {
>>>>>>> 2d7500d (fix: reconcile run lifecycle and terminal actions)
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ status: 'needs-attention' })))
    attached()

<<<<<<< HEAD
    const toggle = screen.getByText('Take control') as HTMLButtonElement
    expect(toggle.disabled).toBe(true)
    // A finished run cannot be steered at all, so the reason is on screen
    // rather than in a title the disabled button would never show, and the
    // mirror hint that leads nowhere stays away.
    expect(screen.getByText('This run is not running')).toBeDefined()
    expect(screen.queryByText('Read-only mirror. Take control to type into the agent.')).toBeNull()
    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
=======
    expect(screen.getByText('Steering')).toBeDefined()
    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })
>>>>>>> 2d7500d (fix: reconcile run lifecycle and terminal actions)
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

  it('offers a retry when the attach is refused outright', () => {
    const view = mount()
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32004, error: 'no live terminal' }),
      }),
    )

    expect(screen.getByText('no live terminal')).toBeDefined()
    fireEvent.click(screen.getByText('Retry'))
    expect(StubSocket.opened).toHaveLength(2)
    view.unmount()
  })

<<<<<<< HEAD
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

  it('says a finished run has ended instead of echoing the refusal', () => {
=======
  it('says a completed run has ended instead of echoing the refusal', () => {
>>>>>>> 2d7500d (fix: reconcile run lifecycle and terminal actions)
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
})

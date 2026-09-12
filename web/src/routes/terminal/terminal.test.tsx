import { act, fireEvent, render, screen } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import type { Run } from '@/lib/types'
import type * as apiModule from '@/lib/api'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import { codeDenied } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { initialTerminal, type TerminalState } from '@/store/terminal'
import { bob, run, serverInfo } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'
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

function mount(seed: Partial<TerminalState> = {}, over: Partial<Run> = {}) {
  const View = terminalRoute()
  useStore.getState().upsertRun(run(over))
  useStore.setState({
    info: serverInfo,
    terminals: { run_1: { ...initialTerminal, ...seed } },
    terminalControlTaken: false,
  })
  return render(<View params={{ runId: 'run_1' }} />)
}

function attached(size = { cols: 80, rows: 24 }) {
  act(() => {
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: true, ...size }),
    })
  })
}

beforeEach(() => {
  StubSocket.install()
  useStore.setState({ runs: {} })
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

  it('keeps a live stalled run in steering mode', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ status: 'needs-attention' })))
    attached()

    expect(screen.getByText('Steering')).toBeDefined()
    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })
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
    expect(StubSocket.last().frames()[0]).toEqual({ follow: true, cols: 80, rows: 24 })
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
      cursor: 0,
      cols: 80,
      rows: 24,
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
})

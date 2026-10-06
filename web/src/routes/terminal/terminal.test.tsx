import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import * as presentation from '@/components/terminal-presentation'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { RoomStatusResult, Run } from '@/lib/types'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import { codeDenied, type TakeoverState } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { initialTerminal, type TerminalState } from '@/store/terminal'
import { alice, bob, run, serverInfo } from '@/test/fixtures'
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

function receiveTakeover(phase: TakeoverState['phase'], over: Partial<TakeoverState> = {}) {
  const socket = StubSocket.last()
  const header = socket.frames()[0] as { control_session_id: string }
  const takeover: TakeoverState = {
    id: 'takeover-request',
    requester_member_id: bob.id,
    requester_session_id: 'requester-session',
    holder_session_id: header.control_session_id,
    holder_generation: 7,
    phase,
    hold_started_at: '2026-10-03T12:00:00Z',
    hold_deadline: '2026-10-03T12:00:05Z',
    decision_deadline: '2026-10-03T12:00:12Z',
    server_now: phase === 'holding' ? '2026-10-03T12:00:00Z' : '2026-10-03T12:00:05Z',
    ...over,
  }
  act(() => socket.onmessage?.({ data: JSON.stringify({ type: 'takeover', ok: true, takeover }) }))
}

const occupiedRoom: RoomStatusResult = {
  workspace_id: run().workspace_id,
  run_id: 'run_1',
  protected: false,
  controller: { member_id: bob.id, connected: true, acquired_at: run().created_at },
  watchers: [alice.id, bob.id],
  queued_steers: 0,
}

beforeEach(() => {
  StubSocket.install()
  vi.mocked(api.runRoomStatus).mockReset()
  useStore.setState({ runs: {}, roomStatusControl: {} })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('terminal view', () => {
  it('shows every named viewer and distinguishes another session owned by this member', async () => {
    const view = mount({}, { member_id: bob.id })
    attached()
    const status = {
      workspace_id: run().workspace_id,
      run_id: 'run_1',
      protected: false,
      controller: { member_id: alice.id, connected: true, acquired_at: run().created_at },
      watchers: [alice.id, bob.id, 'mem_unknown', 'mem_four', 'mem_five'],
      queued_steers: 0,
    }
    // Let the initial status fetch finish before supplying a newer room snapshot.
    await act(async () => {})
    act(() => useStore.setState({
      members: { [alice.id]: alice, [bob.id]: bob },
      roomStatus: { run_1: status },
      roomStatusError: {},
    }))
    const presence = within(screen.getByRole('group', { name: 'Run presence' }))
    expect(presence.getByText('(another session)')).toBeDefined()
    expect(presence.queryByText('(this tab)')).toBeNull()
    for (const name of ['Alice', 'Bob', 'mem_unknown', 'mem_four', 'mem_five']) {
      expect(presence.getAllByText(name)).toHaveLength(name === 'Alice' ? 2 : 1)
    }

    fireEvent.click(screen.getByRole('button', { name: 'Take control' }))
    expect(presence.queryByText('(this tab)')).toBeNull()
    controlAck(true, 1, 1)
    expect(presence.getByText('(this tab)')).toBeDefined()
    expect(presence.queryByText('(another session)')).toBeNull()
    act(() => StubSocket.last().onclose?.({ code: 1006, reason: '' }))
    expect(presence.queryByText('(this tab)')).toBeNull()
    view.unmount()
  })

  it('does not invent another controller session from pre-release presence', async () => {
    const occupied = {
      workspace_id: run().workspace_id,
      run_id: 'run_1',
      protected: false,
      controller: { member_id: alice.id, connected: true, acquired_at: run().created_at },
      watchers: [alice.id, bob.id],
      queued_steers: 0,
    }
    const status = vi.spyOn(api, 'runRoomStatus').mockResolvedValue(occupied)
    const view = mount({}, { member_id: bob.id })
    attached()
    await act(async () => {})
    const presence = within(screen.getByRole('group', { name: 'Run presence' }))
    const refresh = Promise.withResolvers<typeof occupied>()
    status.mockReturnValue(refresh.promise)
    fireEvent.click(screen.getByRole('button', { name: 'Take control' }))
    controlAck(true, 1, 1)
    expect(presence.getByText('(this tab)')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Release' }))
    controlAck(false, 2, 1)
    expect(presence.queryByText('(another session)')).toBeNull()
    expect(presence.getByText('(last known)')).toBeDefined()
    await act(async () => {
      refresh.reject(new Error('presence service unavailable'))
      await Promise.resolve()
    })
    expect(presence.queryByText('(another session)')).toBeNull()
    expect(presence.getByText('(last known)')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Take control' })).toBeDefined()
    view.unmount()
  })

  it('keeps unloaded and unavailable presence distinct from an empty room', async () => {
    const view = mount({}, { member_id: bob.id })
    attached()
    await act(async () => {})
    act(() => useStore.setState({ roomStatus: {}, roomStatusError: {} }))
    const presence = within(screen.getByRole('group', { name: 'Run presence' }))
    expect(presence.getAllByText('Loading…')).toHaveLength(2)
    expect(presence.queryByText('Nobody')).toBeNull()
    act(() => useStore.setState({ roomStatusError: { run_1: 'status unavailable' } }))
    expect(presence.getAllByText('Unavailable')).toHaveLength(2)
    expect(presence.queryByText('Nobody')).toBeNull()
    act(() => useStore.setState({
      roomStatus: { run_1: {
        workspace_id: run().workspace_id,
        run_id: 'run_1',
        protected: false,
        watchers: [],
        queued_steers: 0,
      } },
      roomStatusError: {},
    }))
    expect(presence.getByText('Nobody')).toBeDefined()
    expect(presence.getByText('None')).toBeDefined()
    expect(presence.queryByText('Unavailable')).toBeNull()
    act(() => useStore.setState({ roomStatusError: { run_1: 'status unavailable' } }))
    expect(presence.getByText('Last known presence')).toBeDefined()
    expect(screen.getByText('status unavailable')).toBeDefined()
    act(() => {
      useStore.setState({ roomStatusError: {} })
      StubSocket.last().onclose?.({ code: 1006, reason: '' })
    })
    expect(presence.queryByText('Nobody')).toBeNull()
    expect(presence.queryByText('None')).toBeNull()
    expect(presence.getByText('Last known presence')).toBeDefined()
    view.unmount()
  })

  it('steers by default and lets the user return to a mirror', () => {
    const view = mount()
    attached()

    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })

    fireEvent.click(screen.getByRole('button', { name: 'Release' }))
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

    fireEvent.click(screen.getByRole('button', { name: 'Release' }))
    expect(StubSocket.opened).toHaveLength(opened)
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 2, write: false })
    controlAck(false, 2, 1)
    fireEvent.click(screen.getByText('Take control'))
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 3, write: true })
    expect(socket.frames().at(-1)).not.toHaveProperty('control_generation')
    view.unmount()
  })

  it.each(['toolbar', 'phone Room'] as const)('reports an occupied short-click conflict from the %s without transferring control', async (source) => {
    if (source === 'phone Room') atViewport(390, { height: 844, pointer: 'coarse' })
    vi.spyOn(api, 'runRoomStatus').mockResolvedValue(occupiedRoom)
    const view = mount({}, { member_id: bob.id })
    attached(undefined, undefined, { control_generation: 7 })
    await act(async () => {})
    const socket = StubSocket.last()
    if (source === 'phone Room') fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    const controlButton = source === 'phone Room'
      ? within(screen.getByRole('dialog', { name: 'Run Room' })).getByRole('button', { name: 'Take control' })
      : screen.getByRole('button', { name: 'Take control' })
    fireEvent.click(controlButton)
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', request_id: 1, write: true })
    expect(socket.frames().at(-1)).not.toHaveProperty('takeover')
    controlAck(false, 1, 7, { ok: false, error: 'run control is held by another session' })
    if (source === 'phone Room') fireEvent.click(screen.getByRole('button', { name: 'Close Run Room' }))
    expect(screen.getByText('run control is held by another session')).toBeDefined()
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(useStore.getState().terminals.run_1.write).toBe(false)
    expect(StubSocket.last()).toBe(socket)
    view.unmount()
  })

  it.each(['toolbar', 'phone Room'] as const)('requires a continuous hold from the %s and waits for server ownership', async (source) => {
    if (source === 'phone Room') atViewport(390, { height: 844, pointer: 'coarse' })
    vi.spyOn(api, 'runRoomStatus').mockResolvedValue(occupiedRoom)
    const view = mount({}, { member_id: bob.id })
    attached(undefined, undefined, { control_generation: 7 })
    await act(async () => {})
    const socket = StubSocket.last()
    const header = socket.frames()[0] as { control_session_id: string }
    if (source === 'phone Room') fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    const controlButton = source === 'phone Room'
      ? within(screen.getByRole('dialog', { name: 'Run Room' })).getByRole('button', { name: 'Take control' })
      : screen.getByRole('button', { name: 'Take control' })
    vi.useFakeTimers()
    try {
      fireEvent.keyDown(controlButton, { key: ' ' })
      await act(async () => { await vi.advanceTimersByTimeAsync(180) })
      const start = socket.frames().at(-1) as { takeover_id: string }
      expect(start).toMatchObject({ type: 'takeover', action: 'start' })
      const request = { id: start.takeover_id, requester_member_id: alice.id, requester_session_id: header.control_session_id, holder_session_id: 'holder-session' }
      receiveTakeover('holding', request)
      await act(async () => { await vi.advanceTimersByTimeAsync(4999) })
      expect(socket.frames()).not.toContainEqual(expect.objectContaining({ action: 'confirm' }))
      expect(useStore.getState().terminals.run_1.write).toBe(false)
      await act(async () => { await vi.advanceTimersByTimeAsync(41) })
      expect(socket.frames().at(-1)).toMatchObject({ type: 'takeover', action: 'confirm', takeover_id: start.takeover_id })
      fireEvent.keyUp(controlButton, { key: ' ' })
      expect(socket.frames()).not.toContainEqual(expect.objectContaining({ action: 'cancel' }))
      receiveTakeover('review', request)
      expect(controlButton.getAttribute('aria-disabled')).toBe('true')
      expect(useStore.getState().terminals.run_1.write).toBe(false)
      receiveTakeover('granted', request)
      act(() => socket.onmessage?.({ data: JSON.stringify({ type: 'control', ok: true, has_control: true, control_generation: 8 }) }))
      expect(useStore.getState().terminals.run_1.write).toBe(true)
      expect(StubSocket.last()).toBe(socket)
    } finally {
      view.unmount()
      vi.useRealTimers()
    }
  })

  it.each(['authority', 'connection', 'generation', 'lifecycle'] as const)(
    'invalidates the holder decision after a %s change',
    async (change) => {
      const view = mount()
      attached(undefined, undefined, { control_generation: 7 })
      receiveTakeover('holding')
      receiveTakeover('review')
      const approval = within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Accept' })
      const socket = StubSocket.last()
      act(() => {
        switch (change) {
          case 'authority':
            useStore.getState().upsertRun(run({ protected: true }))
            break
          case 'connection':
            socket.onclose?.({ code: 1006, reason: '' })
            break
          case 'generation':
            socket.onmessage?.({ data: JSON.stringify({
              type: 'control', ok: false, has_control: false,
              control_generation: 7, error: 'control taken over', revocation_reason: 'takeover',
            }) })
            break
          case 'lifecycle':
            useStore.getState().upsertRun(run({ status: 'completed' }))
            break
        }
      })
      await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
      fireEvent.click(approval)
      expect(socket.frames()).not.toContainEqual(expect.objectContaining({ type: 'takeover', action: 'accept' }))
      expect(screen.queryByRole('button', { name: 'Release' })).toBeNull()
      view.unmount()
    },
  )

  it('returns the holder decision to interrupted visible scrollback', async () => {
    vi.spyOn(presentation, 'captureTerminalPresentation').mockReturnValue({
      rows: ['<span>retained output</span>', '<span>second row</span>'],
      cols: 80, viewportY: 0, baseY: 0, cellWidth: 8, cellHeight: 16,
      fontFamily: 'monospace', fontSize: 12, letterSpacing: 0,
    })
    const opened = vi.spyOn(Terminal.prototype, 'open')
    const view = mount()
    const terminal = opened.mock.contexts[0] as Terminal
    attached(undefined, undefined, { control_generation: 7 })
    const host = terminal.element!.parentElement!
    await waitFor(() => expect(host.hasAttribute('inert')).toBe(false))
    fireEvent.wheel(host, { deltaY: -80 })
    const history = await screen.findByRole('region', { name: 'Terminal scrollback' })
    act(() => history.focus())
    receiveTakeover('holding')
    receiveTakeover('review')
    const deny = within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Deny' })
    await waitFor(() => expect(document.activeElement).toBe(deny))
    fireEvent.click(deny)
    expect(StubSocket.last().frames().at(-1)).toMatchObject({
      type: 'takeover', action: 'deny', takeover_id: 'takeover-request', control_generation: 7,
    })
    receiveTakeover('denied')
    await waitFor(() => expect(document.activeElement).toBe(history))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(useStore.getState().terminals.run_1.write).toBe(true)
    expect(host.hasAttribute('inert')).toBe(true)
    expect(host.style.visibility).toBe('hidden')
    view.unmount()
  })

  it.each(['missing', 'unavailable'] as const)('never treats %s presence as permission for takeover', async (presence) => {
    const status = Promise.withResolvers<RoomStatusResult>()
    vi.spyOn(api, 'runRoomStatus').mockReturnValue(status.promise)
    const view = mount({}, { member_id: bob.id })
    attached()
    if (presence === 'unavailable') {
      await act(async () => status.reject(new Error('presence service unavailable')))
      expect(screen.getByText('presence service unavailable')).toBeDefined()
    }
    fireEvent.click(screen.getByRole('button', { name: 'Take control' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByText('Nobody')).toBeNull()
    expect(StubSocket.last().frames().at(-1)).toMatchObject({ type: 'control', write: true })
    expect(StubSocket.last().frames().at(-1)).not.toHaveProperty('takeover')
    view.unmount()
  })


  it('opens another member run as a mirror until they take control', () => {
    const view = mount({}, { member_id: bob.id })
    attached()

    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
    expect(screen.getByText('Take control')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Release' })).toBeNull()
    view.unmount()
  })

  it('downgrades a displaced writer to a mirror without permanent denial', () => {
    const view = mount({}, { member_id: bob.id })
    attached()

    fireEvent.click(screen.getByText('Take control'))
    controlAck(true, 1, 1)

    act(() => StubSocket.last().onclose?.({ code: 1008, reason: 'control taken over' }))
    expect(screen.getByText('Take control')).toBeDefined()
    expect(screen.queryByText('You cannot steer this run.')).toBeNull()

    // Lease displacement sets no permission-denial latch.
    view.unmount()
  })


  it('reports when the server refuses the control request', () => {
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

    expect(screen.getByText('You cannot steer this run.')).toBeDefined()
    view.unmount()
  })

  it('keeps a live stalled run in steering mode', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ status: 'needs-attention' })))
    attached()

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

    const toggle = screen.getByRole('button', { name: 'Take control' })
    expect(toggle.getAttribute('aria-disabled')).toBe('true')
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
    expect(screen.getByRole('button', { name: 'Take control' }).getAttribute('aria-disabled')).toBe('true')

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

    fireEvent.click(screen.getByRole('button', { name: 'Release' }))
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
    expect(screen.getByRole('button', { name: 'Take control' }).getAttribute('aria-disabled')).toBe('true')

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
    // State a previous visit left behind; a fresh attach must not trust it.
    const view = mount({
      steerDenied: true,
      refused: true,
      message: 'no live terminal',
    })
    attached()

    const toggle = screen.getByRole('button', { name: 'Release' }) as HTMLButtonElement
    expect(toggle.getAttribute('aria-disabled')).toBe('false')
    expect(screen.queryByText('no live terminal')).toBeNull()
    expect(screen.queryByText('Retry')).toBeNull()
    view.unmount()
  })

  it('writes PTY output frames into the terminal', async () => {
    // Regression: frames arrived but never reached xterm.
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

  it('preserves existing terminal output when control is released', async () => {
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

    fireEvent.click(screen.getByRole('button', { name: 'Release' }))
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

  // The view stays mounted across runs, and a refused attach never acks, so
  // only the switch itself can clear the previous run's output.
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

  // xterm parses in slices, so a reset still lets already-queued output arrive after it.
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
    expect(screen.getByText('Failed: provisioning: create checkout: no space left')).toBeDefined()
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
      screen.getByText('Failed: provisioning: create checkout: no space left'),
    ).toBeDefined()
    expect(screen.queryByText('Retry')).toBeNull()
    view.unmount()
  })

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

  it('lets a stalled run be steered without calling it not running', () => {
    const view = mount({}, { status: 'needs-attention' })
    attached()

    const toggle = screen.getByRole('button', { name: 'Release' }) as HTMLButtonElement
    expect(toggle.getAttribute('aria-disabled')).toBe('false')
    expect(screen.queryByText('This run is not running')).toBeNull()
    view.unmount()
  })

  // The server keeps an idle run on the live side of its replay gate.
  it('keeps the retry on a run that is idle', () => {
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

// See the terminal section of docs/dashboard-frontend.md.
describe('the terminal on a phone', () => {
  const phone = () => atViewport(390, { height: 844, pointer: 'coarse' })

  it('follows the session geometry and sends no size of its own', async () => {
    phone()
    const resize = vi.spyOn(Terminal.prototype, 'resize')
    const view = mount()
    attached({ cols: 132, rows: 43 })

    // The flag keeps this client out of the PTY's minimum size; the geometry
    // only lays out a session with no PTY, such as a finished run's replay.
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

    // The phone redraws at what the server reports, not at its original ack.
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

    fireEvent.click(screen.getByRole('button', { name: 'Release' }))
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

import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import type { RunStatus } from '@/lib/types'
import type * as apiModule from '@/lib/api'
import type * as attachModule from '@/routes/terminal/attach'
import type * as xtermHostModule from '@/components/xterm-host'
import { useStore } from '@/store'
import {
  getShellSocket,
  initialRunShellDock,
  type RunShellDockState,
  unregisterShellSocket,
} from '@/store/terminal'
import { run } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const replayGateCalls = vi.hoisted(() => ({
  starts: [] as attachModule.ReplayMode[],
  writes: [] as attachModule.AttachDataKind[],
  cancels: [] as boolean[],
}))
const structuralReplayCalls = vi.hoisted(() => ({
  begins: [] as number[],
  cancels: [] as number[],
  finishes: [] as number[],
}))

vi.mock('@/components/xterm-host', async (importOriginal) => {
  const actual = await importOriginal<typeof xtermHostModule>()
  return {
    ...actual,
    useXterm: (options?: xtermHostModule.XtermOptions) => {
      const controller = actual.useXterm(options)
      return {
        ...controller,
        beginStructuralReplay: () => {
          const generation = controller.beginStructuralReplay?.()
          if (generation === undefined) throw new Error('structural replay unavailable')
          structuralReplayCalls.begins.push(generation)
          return generation
        },
        cancelStructuralReplay: async (generation: number) => {
          structuralReplayCalls.cancels.push(generation)
          await controller.cancelStructuralReplay?.(generation)
        },
        finishStructuralReplay: async (generation: number) => {
          structuralReplayCalls.finishes.push(generation)
          await controller.finishStructuralReplay?.(generation)
        },
      }
    },
  }
})

vi.mock('@/routes/terminal/attach', async (importOriginal) => {
  const actual = await importOriginal<typeof attachModule>()
  return {
    ...actual,
    replayGate: (
      write: (chunk: Uint8Array, done?: () => void) => void,
      onReplaying?: (replaying: boolean, full?: boolean) => void,
    ) => {
      const gate = actual.replayGate(write, onReplaying)
      return {
        ...gate,
        start: (mode: attachModule.ReplayMode = 'full') => {
          replayGateCalls.starts.push(mode)
          gate.start(mode)
        },
        cancel: (full = true) => {
          replayGateCalls.cancels.push(full)
          gate.cancel(full)
        },
        write: (
          chunk: Uint8Array,
          kind: attachModule.AttachDataKind,
          settled?: () => void,
        ) => {
          replayGateCalls.writes.push(kind)
          return gate.write(chunk, kind, settled)
        },
      }
    },
  }
})

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return {
    ...actual,
    api: {
      ...fakeApi(),
      attachShellSocket: (runID: string, tab: string) =>
        `ws://localhost/ws/attach/${encodeURIComponent(runID)}?shell=${encodeURIComponent(tab)}`,
    },
  }
})

function mount({
  runID = 'run_1',
  dock = {},
  status = 'running',
}: { runID?: string; dock?: Partial<RunShellDockState>; status?: RunStatus } = {}) {
  const View = lookupRoute('terminal')
  if (!View) throw new Error('terminal route not registered')
  useStore.getState().upsertRun(run({ id: runID, status }))
  useStore.setState({
    terminals: {},
    pausedRuns: { [runID]: false },
    // The dock ships collapsed; these cases are about what it shows open.
    shellDocks: { [runID]: { ...initialRunShellDock, collapsed: false, ...dock } },
  })
  return render(<View params={{ runId: runID }} />)
}
beforeEach(() => {
  replayGateCalls.starts.length = 0
  replayGateCalls.writes.length = 0
  replayGateCalls.cancels.length = 0
  structuralReplayCalls.begins.length = 0
  structuralReplayCalls.cancels.length = 0
  structuralReplayCalls.finishes.length = 0
  for (const runID of ['run_1', 'run_2']) {
    for (const tab of ['t1', 't2', 't3', 't4']) unregisterShellSocket(runID, tab)
  }
  StubSocket.install()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('run-shell dock', () => {
  it('opens a forced writable shell tab for a stalled live run', async () => {
    const view = mount({ status: 'needs-attention' })

    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))

    const socket = StubSocket.opened[1]
    expect(socket.url).toBe('ws://localhost/ws/attach/run_1?shell=t1')
    act(() => socket.onopen?.())
    expect(socket.frames()[0]).toMatchObject({ write: true })
    view.unmount()
  })

  it('shows the fixed refusal sentence and does not reconnect on denied shells', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))

    const socket = StubSocket.opened[1]
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: false, code: -32001, error: 'permission denied' }),
      })
    })

    const refusal = screen.getByText('You can view this run but not open a shell in it')
    expect(refusal).toBeDefined()
    // The refusal disposes the terminal that had the keyboard. Left on
    // <body>, the reader's next keystroke would reach the shell's shortcuts
    // and leave the run.
    expect(document.activeElement).toBe(refusal)
    await waitFor(
      () => expect(StubSocket.opened).toHaveLength(2),
      { timeout: 100 },
    )
    view.unmount()
  })

  it('removes a tab when the shell socket closes normally', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))
    act(() => StubSocket.opened[1].onclose?.({ code: 1000 }))

    expect(useStore.getState().shellDocks.run_1.tabs).toEqual([])
    // The last shell exiting disposes the terminal that had the keyboard, so
    // the body it leaves behind takes it rather than <body>.
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Open shell' }).parentElement,
    )
    view.unmount()
  })

  it('rebinds a persistent shell after the terminal route remounts', async () => {
    const first = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.last().url).toContain('?shell=t1'))
    const shell = StubSocket.last()
    act(() => {
      shell.onopen?.()
      shell.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }) })
    })
    first.unmount()

    const second = mount({
      dock: { tabs: ['t1'], activeTab: 't1', collapsed: false },
    })
    const shellsBeforeReopen = () =>
      StubSocket.opened.filter((socket) => socket.url.includes('?shell=t1'))
    await waitFor(() => expect(shellsBeforeReopen()).toHaveLength(2))
    const reopened = shellsBeforeReopen()[1]
    act(() => {
      reopened.onopen?.()
      reopened.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 2, resume_id: 'pty-incarnation-shell' }) })
      reopened.onmessage?.({ data: new TextEncoder().encode('remounted shell').buffer })
    })

    await vi.waitFor(() =>
      expect(
        [...document.querySelectorAll('.xterm-rows')].some((rows) =>
          rows.textContent?.includes('remounted shell'),
        ),
      ).toBe(true),
    )
    second.unmount()
  })
  it('starts shell replay muting and hiding before the first replay frame', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))
    const shell = StubSocket.opened[1]

    act(() => {
      shell.onopen?.()
      shell.onmessage?.({
        data: JSON.stringify({ ok: true, replay: 3, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }),
      })
    })
    expect(replayGateCalls.starts).toEqual(['full'])
    expect(structuralReplayCalls.begins).toHaveLength(1)
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()
    const terminalDockElement = screen.getByRole('region', { name: 'Terminal dock' })
    const host = terminalDockElement.querySelector(
      '.min-h-0.flex-1.bg-background',
    ) as HTMLElement
    expect(host.style.visibility).toBe('hidden')

    act(() => {
      shell.onmessage?.({ data: new TextEncoder().encode('out').buffer })
    })
    expect(replayGateCalls.starts).toEqual(['full'])

    await waitFor(() =>
      expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull(),
    )
    expect(host.style.visibility).toBe('')
    expect(replayGateCalls.writes).toContain('replay-end')
    expect(structuralReplayCalls.finishes).toEqual(structuralReplayCalls.begins)
    view.unmount()
  })

  it('completes an accepted zero-byte shell replay through the full reveal', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))
    const shell = StubSocket.opened[1]

    act(() => {
      shell.onopen?.()
      shell.onmessage?.({
        data: JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }),
      })
    })

    expect(replayGateCalls.starts).toEqual(['full'])
    expect(replayGateCalls.writes).toEqual(['replay-end'])
    await waitFor(() => expect(structuralReplayCalls.finishes).toEqual(structuralReplayCalls.begins))
    expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull()
    view.unmount()
  })

  it('cancels the structural shell replay when a full replay aborts', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))
    const shell = StubSocket.opened[1]

    act(() => {
      shell.onopen?.()
      shell.onmessage?.({
        data: JSON.stringify({ ok: true, replay: 3, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }),
      })
      shell.onclose?.({ code: 1006 })
    })

    await waitFor(() => expect(structuralReplayCalls.cancels).toEqual(structuralReplayCalls.begins))
    expect(structuralReplayCalls.finishes).toEqual([])
    expect(replayGateCalls.cancels).toContain(true)
    view.unmount()
  })

  it('keeps resumed shell replay delta-only', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))
    const first = StubSocket.opened[1]
    act(() => {
      first.onopen?.()
      first.onmessage?.({
        data: JSON.stringify({ ok: true, replay: 0, cursor: 0, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }),
      })
    })
    await waitFor(() => expect(structuralReplayCalls.finishes).toEqual(structuralReplayCalls.begins))

    replayGateCalls.starts.length = 0
    replayGateCalls.writes.length = 0
    structuralReplayCalls.begins.length = 0
    structuralReplayCalls.cancels.length = 0
    structuralReplayCalls.finishes.length = 0
    getShellSocket('run_1', 't1')?.reopen({ resume: true })
    const shells = () => StubSocket.opened.filter((socket) => socket.url.includes('?shell=t1'))
    await waitFor(() => expect(shells()).toHaveLength(2))
    const resumed = shells()[1]
    act(() => resumed.onopen?.())
    expect(resumed.frames()[0]).toMatchObject({ resume: true, cursor: 0 })
    act(() => {
      resumed.onmessage?.({
        data: JSON.stringify({ ok: true, resumed: true, replay: 1, cursor: 1, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }),
      })
      resumed.onmessage?.({ data: new Uint8Array([1]).buffer })
    })

    await waitFor(() => expect(replayGateCalls.writes).toContain('replay-end'))
    expect(replayGateCalls.starts).toEqual(['delta'])
    expect(structuralReplayCalls.begins).toEqual([])
    expect(structuralReplayCalls.cancels).toEqual([])
    expect(structuralReplayCalls.finishes).toEqual([])
    expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull()
    view.unmount()
  })
  it('ignores late callbacks from a prior run sharing the active shell tab', async () => {
    const View = lookupRoute('terminal')
    if (!View) throw new Error('terminal route not registered')
    const first = mount({
      runID: 'run_1',
      dock: { tabs: ['t1'], activeTab: 't1', collapsed: false },
    })
    const shellFor = (runID: string) =>
      StubSocket.opened.find((socket) => socket.url.includes(`/attach/${runID}?shell=t1`))
    await waitFor(() => expect(shellFor('run_1')).toBeDefined())
    const oldShell = shellFor('run_1')
    act(() => {
      oldShell?.onopen?.()
      oldShell?.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }) })
    })

    useStore.getState().upsertRun(run({ id: 'run_2' }))
    useStore.setState({
      terminals: {},
      pausedRuns: { run_1: false, run_2: false },
      shellDocks: {
        run_1: { ...initialRunShellDock, tabs: ['t1'], activeTab: 't1', collapsed: false },
        run_2: { ...initialRunShellDock, tabs: ['t1'], activeTab: 't1', collapsed: false },
      },
    })
    first.rerender(<View params={{ runId: 'run_2' }} />)

    await waitFor(() => expect(shellFor('run_2')).toBeDefined())
    const currentShell = shellFor('run_2')
    structuralReplayCalls.begins.length = 0
    act(() => {
      currentShell?.onopen?.()
      currentShell?.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }) })
      currentShell?.onmessage?.({ data: new TextEncoder().encode('B output').buffer })
      // These events belong to run_1, but arrive after run_2 accepted t1.
      oldShell?.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' }) })
      oldShell?.onmessage?.({ data: new TextEncoder().encode('A output').buffer })
      oldShell?.onclose?.({ code: 1000 })
    })
    expect(structuralReplayCalls.begins).toHaveLength(1)

    await vi.waitFor(() =>
      expect(
        [...document.querySelectorAll('.xterm-rows')].some((rows) =>
          rows.textContent?.includes('B output'),
        ),
      ).toBe(true),
    )
    expect(screen.queryByRole('status')).toBeNull()
    expect(useStore.getState().shellDocks.run_2.tabs).toEqual(['t1'])
    expect(currentShell?.closed).toBe(false)
    first.unmount()
  })

  it('keeps late background callbacks away from the active shell and refusal', async () => {
    const view = mount({
      dock: { tabs: ['t1', 't2'], activeTab: 't1', collapsed: false },
    })
    const terminalDock = within(screen.getByRole('region', { name: 'Terminal dock' }))
    const shellsFor = (tab: string) =>
      StubSocket.opened.filter((socket) => socket.url.includes(`?shell=${tab}`))
    const accepted = JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 1, resume_id: 'pty-incarnation-shell' })
    const denied = JSON.stringify({ ok: false, code: -32001, error: 'write denied' })

    await waitFor(() => expect(shellsFor('t1')).toHaveLength(1))
    const firstActive = shellsFor('t1')[0]
    act(() => {
      firstActive?.onopen?.()
      firstActive?.onmessage?.({ data: accepted })
    })

    fireEvent.click(screen.getByRole('tab', { name: 't2' }))
    await waitFor(() => expect(shellsFor('t2')).toHaveLength(1))
    const background = shellsFor('t2')[0]
    act(() => {
      background?.onopen?.()
      background?.onmessage?.({ data: accepted })
    })

    fireEvent.click(screen.getByRole('tab', { name: 't1' }))
    await waitFor(() => expect(shellsFor('t1')).toHaveLength(2))
    const active = shellsFor('t1')[1]
    act(() => {
      active?.onopen?.()
      active?.onmessage?.({ data: accepted })
    })
    expect(terminalDock.getByRole('toolbar', { name: 'Terminal controls' })).toBeDefined()

    const lateBackgroundMessage = background?.onmessage
    act(() => lateBackgroundMessage?.({ data: denied }))
    expect(background?.closed).toBe(true)
    act(() => lateBackgroundMessage?.({ data: accepted }))
    expect(terminalDock.getByRole('toolbar', { name: 'Terminal controls' })).toBeDefined()

    const activeMessage = active?.onmessage
    act(() =>
      activeMessage?.({
        data: JSON.stringify({ ok: false, code: -32002, error: 'active backend refusal' }),
      }),
    )
    expect(terminalDock.getByText('active backend refusal')).toBeDefined()
    expect(active?.closed).toBe(true)
    act(() => lateBackgroundMessage?.({ data: accepted }))
    expect(terminalDock.getByText('active backend refusal')).toBeDefined()
    expect(terminalDock.queryByRole('toolbar', { name: 'Terminal controls' })).toBeNull()
    view.unmount()
  })


  it('reconnects as a mirror after control loss until the user takes over', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))

    const shell = StubSocket.opened[1]
    act(() => {
      shell.onopen?.()
      shell.onmessage?.({
        data: JSON.stringify({ ok: true, replay: 0, has_control: true, control_generation: 3, resume_id: 'pty-incarnation-shell' }),
      })
      shell.onclose?.({ code: 1008, reason: 'control taken over' })
    })

    expect(screen.getByText('Read-only shell. Another session controls this run.')).toBeDefined()
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(3))
    const mirror = StubSocket.opened[2]
    act(() => {
      mirror.onopen?.()
    })
    expect(mirror.frames()[0]).not.toHaveProperty('write')

    fireEvent.click(screen.getByRole('button', { name: 'Take shell control' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(4))
    const takeover = StubSocket.opened[3]
    act(() => {
      takeover.onopen?.()
    })
    expect(takeover.frames()[0]).toMatchObject({ write: true, takeover: true })
    view.unmount()
  })
  it('waits for pause state instead of offering a rejected shell', () => {
    const view = mount({ status: 'needs-attention' })
    act(() => useStore.setState({ pausedRuns: {} }))

    expect(screen.queryByRole('button', { name: 'Open shell' })).toBeNull()
    expect(screen.getByText('Run shell unavailable: waiting for the run pause state.')).toBeDefined()
    view.unmount()
  })

  it('does not offer shell tabs after a completed run loses its container', () => {
    const view = mount({ status: 'completed' })

    expect(screen.queryByRole('button', { name: 'Open shell' })).toBeNull()
    expect(screen.getByText(/Run shell unavailable/)).toBeDefined()
    view.unmount()
  })
})

import { act, renderHook, waitFor } from '@testing-library/react'
import type { Terminal } from '@xterm/xterm'
import { useRunTerminalSession } from '@/routes/terminal/session'
import { useStore } from '@/store'
import { initialTerminal } from '@/store/terminal'
import { run, serverInfo } from '@/test/fixtures'
import { toRecord } from '@/store/runs'
import { StubSocket } from '@/test/stub-socket'

function fakeTerminal(): Terminal {
  return {
    cols: 80,
    buffer: { normal: { length: 1 }, alternate: { length: 0 } },
    write: vi.fn((_chunk: unknown, done?: () => void) => done?.()),
    reset: vi.fn(),
  } as unknown as Terminal
}

function mount(active = true) {
  const terminal = fakeTerminal()
  useStore.setState({
    info: serverInfo,
    runs: { run_1: toRecord(run()) },
    terminals: { run_1: initialTerminal },
  })
  const result = renderHook(
    ({ currentActive }) =>
      useRunTerminalSession({
        runID: 'run_1',
        run: useStore.getState().runs.run_1,
        active: currentActive,
        initialized: true,
        terminal,
        geometry: () => ({ cols: 80, rows: 24 }),
        setGeometry: vi.fn(),
        phone: false,
        automaticWrite: false,
        authorityKey: 'mem_alice:collaborator:mem_alice:false:',
      }),
    { initialProps: { currentActive: active } },
  )
  return { ...result, terminal }
}

beforeEach(() => {
  StubSocket.install()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useRunTerminalSession', () => {
  it('uses one interactive screen attach and waits for an acknowledged grant', () => {
    const { result } = mount()
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: true, replay: 0, control_generation: 0, has_control: false }),
      })
    })
    expect(socket.frames()[0]).toMatchObject({ screen: true, interactive: true })

    act(() => result.current.takeControl())
    expect(result.current.state.write).toBe(false)
    act(() =>
      socket.onmessage?.({
        data: JSON.stringify({
          type: 'control',
          request_id: 1,
          ok: true,
          has_control: true,
          control_generation: 1,
        }),
      }),
    )
    expect(result.current.state.write).toBe(true)
    expect(useStore.getState().terminalControlTaken).toBe(true)
  })

  it('parks the socket and resumes the same lifecycle', async () => {
    const mounted = mount()
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0, cursor: 123, resume_id: 'pty-incarnation-run' }) })
    })
    act(() => mounted.rerender({ currentActive: false }))
    expect(socket.closed).toBe(true)

    act(() => mounted.rerender({ currentActive: true }))
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const resumed = StubSocket.last()
    act(() => resumed.onopen?.())
    expect(resumed.frames()[0]).toMatchObject({ resume: true })
    mounted.unmount()
  })
})

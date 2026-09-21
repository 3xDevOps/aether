import { act, renderHook, waitFor } from '@testing-library/react'
import type { Terminal } from '@xterm/xterm'
import {
  useRunTerminalSession,
  type RunTerminalSessionInput,
} from '@/routes/terminal/session'
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

type SessionViewport = Pick<
  RunTerminalSessionInput,
  | 'setGeometry'
  | 'beginStructuralReplay'
  | 'cancelStructuralReplay'
  | 'finishStructuralReplay'
  | 'onInvalidate'
>

function mount(active = true, viewport: Partial<SessionViewport> = {}) {
  const terminal = fakeTerminal()
  const replay: SessionViewport = {
    setGeometry: vi.fn(async () => {}),
    beginStructuralReplay: vi.fn(() => 1),
    cancelStructuralReplay: vi.fn(async () => {}),
    finishStructuralReplay: vi.fn(async () => {}),
    ...viewport,
  }
  useStore.setState({
    info: serverInfo,
    runs: { run_1: toRecord(run()) },
    terminals: { run_1: initialTerminal },
  })
  const result = renderHook(
    ({
      currentActive,
      currentTerminal = terminal,
    }: {
      currentActive: boolean
      currentTerminal?: Terminal
    }) =>
      useRunTerminalSession({
        runID: 'run_1',
        run: useStore.getState().runs.run_1,
        active: currentActive,
        initialized: true,
        terminal: currentTerminal,
        geometry: () => ({ cols: 80, rows: 24 }),
        ...replay,
        phone: false,
        automaticWrite: false,
        authorityKey: 'mem_alice:collaborator:mem_alice:false:',
      }),
    { initialProps: { currentActive: active } as { currentActive: boolean; currentTerminal?: Terminal } },
  )
  return { ...result, terminal, ...replay, viewport: replay }
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
        data: JSON.stringify({
          ok: true,
          replay: 0,
          resume_id: 'epoch',
          cursor: '7',
          control_generation: 0,
          has_control: false,
        }),
      })
    })
    expect(socket.frames()[0]).toMatchObject({ screen: true, interactive: true })
    expect(result.current.controlMetadata?.position).toEqual({ epoch: 'epoch', sequence: '7' })

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

  it('restores a full replay only after geometry and the final xterm callback', async () => {
    const order: string[] = []
    let geometryDone!: () => void
    let writeDone!: () => void
    const mounted = mount(true, {
      beginStructuralReplay: vi.fn(() => {
        order.push('begin')
        return 7
      }),
      setGeometry: vi.fn(
        () =>
          new Promise<void>((resolve) => {
            order.push('geometry:start')
            geometryDone = () => {
              order.push('geometry:done')
              resolve()
            }
          }),
      ),
      finishStructuralReplay: vi.fn(async (generation) => {
        order.push(`finish:${generation}`)
      }),
    })
    mounted.terminal.write = vi.fn((_chunk: unknown, done?: () => void) => {
      order.push('write')
      writeDone = () => {
        order.push('write:done')
        done?.()
      }
    })
    const socket = StubSocket.last()

    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 80, rows: 24, replay: 3 }),
      })
      socket.onmessage?.({ data: new TextEncoder().encode('abc').buffer })
    })

    expect(order).toEqual(['begin', 'geometry:start'])
    expect(mounted.finishStructuralReplay).not.toHaveBeenCalled()

    await act(async () => {
      geometryDone()
      await Promise.resolve()
    })
    expect(order).toEqual(['begin', 'geometry:start', 'geometry:done', 'write'])
    expect(mounted.finishStructuralReplay).not.toHaveBeenCalled()

    await act(async () => {
      writeDone()
      await Promise.resolve()
    })
    await waitFor(() => expect(mounted.finishStructuralReplay).toHaveBeenCalledWith(7))
    expect(order).toEqual([
      'begin',
      'geometry:start',
      'geometry:done',
      'write',
      'write:done',
      'finish:7',
    ])
    mounted.unmount()
  })

  it('keeps a zero-byte full replay hidden through paint and structural finish', async () => {
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal(
      'requestAnimationFrame',
      vi.fn((callback: FrameRequestCallback) => {
        frames.push(callback)
        return frames.length
      }),
    )
    const order: string[] = []
    let geometryDone!: () => void
    let finishDone!: () => void
    const mounted = mount(true, {
      beginStructuralReplay: vi.fn(() => {
        order.push('begin')
        return 9
      }),
      setGeometry: vi.fn(
        () =>
          new Promise<void>((resolve) => {
            order.push('geometry:start')
            geometryDone = () => {
              order.push('geometry:done')
              resolve()
            }
          }),
      ),
      finishStructuralReplay: vi.fn(
        () =>
          new Promise<void>((resolve) => {
            order.push('finish')
            finishDone = resolve
          }),
      ),
    })
    mounted.terminal.write = vi.fn((chunk: Uint8Array, done?: () => void) => {
      order.push(`write:${chunk.length}`)
      done?.()
    }) as unknown as Terminal['write']
    const socket = StubSocket.last()

    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 80, rows: 24, replay: 0 }),
      })
    })

    expect(order).toEqual(['begin', 'geometry:start'])
    expect(mounted.result.current.replaying).toBe(true)
    expect(mounted.finishStructuralReplay).not.toHaveBeenCalled()
    expect(frames).toHaveLength(0)

    await act(async () => {
      geometryDone()
      await Promise.resolve()
    })
    expect(order).toEqual(['begin', 'geometry:start', 'geometry:done', 'write:0'])
    expect(frames).toHaveLength(1)

    const replacementFinish = vi.fn(async () => {})
    mounted.viewport.finishStructuralReplay = replacementFinish
    mounted.rerender({ currentActive: true })

    act(() => frames.shift()?.(0))
    expect(mounted.result.current.replaying).toBe(true)
    expect(mounted.finishStructuralReplay).not.toHaveBeenCalled()
    expect(frames).toHaveLength(1)

    act(() => frames.shift()?.(16))
    expect(mounted.finishStructuralReplay).toHaveBeenCalledWith(9)
    expect(replacementFinish).not.toHaveBeenCalled()
    expect(mounted.result.current.replaying).toBe(true)

    await act(async () => {
      finishDone()
      await Promise.resolve()
    })
    expect(mounted.result.current.replaying).toBe(false)
    mounted.unmount()
  })

  it('orders refusal reset, replay cancellation, and invalidation', async () => {
    const order: string[] = []
    let resetDone!: () => void
    let cancelDone!: () => void
    let geometryCalls = 0
    const mounted = mount(true, {
      beginStructuralReplay: vi.fn(() => 11),
      setGeometry: vi.fn(() => {
        geometryCalls++
        if (geometryCalls === 1) return Promise.resolve()
        return new Promise<void>((resolve) => {
          order.push('reset:start')
          resetDone = () => {
            order.push('reset:done')
            resolve()
          }
        })
      }),
      cancelStructuralReplay: vi.fn(
        () =>
          new Promise<void>((resolve) => {
            order.push('cancel:start')
            cancelDone = () => {
              order.push('cancel:done')
              resolve()
            }
          }),
      ),
      onInvalidate: () => order.push('invalidate'),
    })
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 80, rows: 24, replay: 3 }),
      })
      socket.onmessage?.({
        data: JSON.stringify({ ok: false, error: 'replay refused' }),
      })
    })

    expect(order).toEqual(['reset:start'])
    expect(mounted.cancelStructuralReplay).not.toHaveBeenCalled()

    await act(async () => {
      resetDone()
      await Promise.resolve()
    })
    expect(order).toEqual(['reset:start', 'reset:done', 'cancel:start'])
    expect(mounted.cancelStructuralReplay).toHaveBeenCalledWith(11)

    await act(async () => {
      cancelDone()
      await Promise.resolve()
    })
    await waitFor(() =>
      expect(order).toEqual([
        'reset:start',
        'reset:done',
        'cancel:start',
        'cancel:done',
        'invalidate',
      ]),
    )
    mounted.unmount()
  })

  it('cancels structural replay through its original controller on replacement', () => {
    const ownerCancel = vi.fn(async () => {})
    const replacementCancel = vi.fn(async () => {})
    const mounted = mount(true, {
      beginStructuralReplay: vi.fn(() => 13),
      cancelStructuralReplay: ownerCancel,
    })
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 80, rows: 24, replay: 3 }),
      })
    })

    mounted.viewport.cancelStructuralReplay = replacementCancel
    mounted.rerender({ currentActive: true, currentTerminal: fakeTerminal() })

    expect(ownerCancel).toHaveBeenCalledWith(13)
    expect(replacementCancel).not.toHaveBeenCalled()
    mounted.unmount()
  })

  it('keeps stale refusal cleanup from revealing or invalidating a replacement replay', async () => {
    let resetDone!: () => void
    let cancelDone!: () => void
    let geometryCalls = 0
    let generation = 0
    const replacementCancel = vi.fn(async () => {})
    const onInvalidate = vi.fn()
    const mounted = mount(true, {
      beginStructuralReplay: vi.fn(() => ++generation),
      setGeometry: vi.fn(() => {
        geometryCalls++
        if (geometryCalls !== 2) return Promise.resolve()
        return new Promise<void>((resolve) => {
          resetDone = resolve
        })
      }),
      cancelStructuralReplay: vi.fn(
        () =>
          new Promise<void>((resolve) => {
            cancelDone = resolve
          }),
      ),
      onInvalidate,
    })
    const refused = StubSocket.last()
    act(() => {
      refused.onopen?.()
      refused.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 80, rows: 24, replay: 3 }),
      })
      refused.onmessage?.({
        data: JSON.stringify({ ok: false, error: 'replay refused' }),
      })
    })

    mounted.viewport.cancelStructuralReplay = replacementCancel
    mounted.rerender({ currentActive: true })

    await act(async () => {
      resetDone()
      await Promise.resolve()
    })
    expect(mounted.cancelStructuralReplay).toHaveBeenCalledWith(1)
    expect(replacementCancel).not.toHaveBeenCalled()

    act(() => mounted.result.current.retry())
    const replacement = StubSocket.last()
    expect(replacement).not.toBe(refused)
    act(() => {
      replacement.onopen?.()
      replacement.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 80, rows: 24, replay: 3 }),
      })
    })
    expect(mounted.beginStructuralReplay).toHaveBeenCalledTimes(2)
    expect(mounted.result.current.replaying).toBe(true)

    await act(async () => {
      cancelDone()
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(onInvalidate).not.toHaveBeenCalled()
    expect(mounted.result.current.replaying).toBe(true)
    expect(mounted.cancelStructuralReplay).toHaveBeenCalledTimes(1)
    mounted.unmount()
  })

  it('parks the socket and resumes a delta without a structural viewport restore', async () => {
    const mounted = mount()
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          cursor: 123,
          resume_id: 'pty-incarnation-run',
        }),
      })
    })
    await waitFor(() => expect(mounted.finishStructuralReplay).toHaveBeenCalled())
    vi.mocked(mounted.beginStructuralReplay!).mockClear()
    vi.mocked(mounted.cancelStructuralReplay!).mockClear()
    vi.mocked(mounted.finishStructuralReplay!).mockClear()

    act(() => mounted.rerender({ currentActive: false }))
    expect(socket.closed).toBe(true)

    act(() => mounted.rerender({ currentActive: true }))
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const resumed = StubSocket.last()
    let replayDone!: () => void
    mounted.terminal.write = vi.fn((_chunk: unknown, done?: () => void) => {
      replayDone = done ?? (() => {})
    }) as unknown as Terminal['write']
    act(() => {
      resumed.onopen?.()
      resumed.onmessage?.({
        data: JSON.stringify({
          ok: true,
          resumed: true,
          replay: 3,
          cursor: 126,
          resume_id: 'pty-incarnation-run',
        }),
      })
      resumed.onmessage?.({ data: new TextEncoder().encode('new').buffer })
    })

    expect(resumed.frames()[0]).toMatchObject({ resume: true })
    expect(mounted.result.current.replaying).toBe(false)
    act(() => replayDone())
    await waitFor(() => expect(mounted.result.current.replaying).toBe(false))
    expect(mounted.beginStructuralReplay).not.toHaveBeenCalled()
    expect(mounted.cancelStructuralReplay).not.toHaveBeenCalled()
    expect(mounted.finishStructuralReplay).not.toHaveBeenCalled()
    mounted.unmount()
  })
})

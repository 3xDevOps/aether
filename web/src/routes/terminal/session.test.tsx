import { act, renderHook, waitFor } from '@testing-library/react'
import { useLayoutEffect, useRef } from 'react'
import type { Terminal } from '@xterm/xterm'
import {
  useRunTerminalSession,
  type RunTerminalSessionInput,
  type RunTerminalSessionResult,
} from '@/routes/terminal/session'
import { useStore } from '@/store'
import { initialTerminal } from '@/store/terminal'
import { run, serverInfo } from '@/test/fixtures'
import { toRecord } from '@/store/runs'
import { StubSocket } from '@/test/stub-socket'

function fakeTerminal(): Terminal {
  return {
    cols: 80,
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
>

interface MountProps {
  currentTerminal?: Terminal | null
  currentPhone?: boolean
  currentAutomaticWrite?: boolean
  currentAuthorityKey?: string
  currentIdentityKey?: string | null
  currentCacheEpoch?: number
  onAuthorityLayout?: (session: RunTerminalSessionResult) => void
}

function mount(
  viewport: Partial<SessionViewport> = {},
  terminalState = initialTerminal,
  initialProps: MountProps = {},
) {
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
    terminals: { run_1: terminalState },
  })
  const result = renderHook(
    ({
      currentTerminal = terminal,
      currentPhone = false,
      currentAutomaticWrite = false,
      currentAuthorityKey = 'mem_alice:collaborator:mem_alice:false:',
      currentIdentityKey = 'identity-a',
      currentCacheEpoch = 0,
      onAuthorityLayout,
    }: MountProps) => {
      const session = useRunTerminalSession({
        runID: 'run_1',
        run: useStore.getState().runs.run_1,
        terminal: currentTerminal,
        geometry: () => ({ cols: 80, rows: 24 }),
        ...replay,
        phone: currentPhone,
        automaticWrite: currentAutomaticWrite,
        identityKey: currentIdentityKey,
        terminalCacheEpoch: currentCacheEpoch,
        authorityKey: currentAuthorityKey,
      })
      const previousAuthority = useRef(currentAuthorityKey)
      useLayoutEffect(() => {
        if (previousAuthority.current !== currentAuthorityKey) onAuthorityLayout?.(session)
        previousAuthority.current = currentAuthorityKey
      }, [currentAuthorityKey, onAuthorityLayout, session])
      return session
    },
    { initialProps },
  )
  return { ...result, terminal, ...replay, viewport: replay }
}

beforeEach(() => {
  StubSocket.install()
  useStore.setState({
    identityKey: 'identity-a',
    terminalCacheEpoch: 0,
    terminalWriteIntents: {},
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useRunTerminalSession', () => {
  it('uses one interactive screen attach and waits for replay and an acknowledged grant', async () => {
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
    await waitFor(() => expect(result.current.replaying).toBe(false))
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

    act(() => {
      result.current.send('x')
      result.current.resize(92, 32)
    })
    expect(socket.frames().at(-2)).toMatchObject({ type: 'input', data: 'x' })
    expect(socket.frames().at(-1)).toEqual({
      type: 'resize',
      cols: 92,
      rows: 32,
      control_generation: 1,
    })
  })

  it('uses the fenced cursor when a live viewport change reconnects by resume', async () => {
    const mounted = mount()
    const initial = StubSocket.last()
    act(() => {
      initial.onopen?.()
      initial.onmessage?.({
        data: JSON.stringify({
          ok: true,
          cols: 80,
          rows: 24,
          replay: 0,
          resume_id: 'pty-run-1',
          cursor: '7',
        }),
      })
    })
    await waitFor(() => expect(mounted.result.current.replaying).toBe(false))
    vi.mocked(mounted.beginStructuralReplay!).mockClear()
    vi.mocked(mounted.setGeometry).mockClear()

    mounted.rerender({ currentPhone: true })
    await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
    const resumed = StubSocket.last()
    act(() => resumed.onopen?.())

    expect(resumed.frames()[0]).toMatchObject({
      resume: true,
      resume_id: 'pty-run-1',
      cursor: '7',
    })

    act(() => {
      resumed.onmessage?.({
        data: JSON.stringify({
          ok: true,
          resumed: true,
          cols: 80,
          rows: 24,
          replay: 3,
          resume_id: 'pty-run-1',
          cursor: '10',
        }),
      })
      resumed.onmessage?.({ data: new TextEncoder().encode('new').buffer })
    })

    expect(mounted.setGeometry).toHaveBeenCalledWith(80, 24, false)
    expect(mounted.beginStructuralReplay).not.toHaveBeenCalled()
    expect(mounted.result.current.replaying).toBe(false)
    mounted.unmount()
  })

  it('starts from a fresh snapshot after the prior route session is disposed', async () => {
    const first = mount()
    const oldSocket = StubSocket.last()
    act(() => {
      oldSocket.onopen?.()
      oldSocket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          resume_id: 'pty-run-1',
          cursor: '7',
        }),
      })
    })
    await waitFor(() => expect(first.result.current.replaying).toBe(false))
    first.unmount()
    expect(oldSocket.closed).toBe(true)
    expect(useStore.getState().terminals.run_1).toEqual(initialTerminal)

    const second = mount()
    const freshSocket = StubSocket.last()
    act(() => freshSocket.onopen?.())

    expect(freshSocket.frames()[0]).toMatchObject({
      screen: true,
      interactive: true,
    })
    expect(freshSocket.frames()[0]).not.toHaveProperty('resume')
    expect(freshSocket.frames()[0]).not.toHaveProperty('resume_id')
    expect(freshSocket.frames()[0]).not.toHaveProperty('cursor')
    second.unmount()
  })

  it('reuses an explicit take intent on the next route attach', () => {
    const first = mount()
    act(() => first.result.current.takeControl())
    first.unmount()

    const second = mount()
    const socket = StubSocket.last()
    act(() => socket.onopen?.())

    expect(socket.frames()[0]).toMatchObject({ write: true })
    second.unmount()
  })

  it('reuses an explicit release intent instead of automatic write on remount', () => {
    const first = mount({}, initialTerminal, { currentAutomaticWrite: true })
    act(() => first.result.current.releaseControl())
    first.unmount()

    const second = mount({}, initialTerminal, { currentAutomaticWrite: true })
    const socket = StubSocket.last()
    act(() => socket.onopen?.())

    expect(socket.frames()[0]).not.toHaveProperty('write')
    second.unmount()
  })

  it.each([
    ['identity', { currentIdentityKey: 'identity-b' }],
    ['terminal cache epoch', { currentCacheEpoch: 1 }],
  ] as const)('fences intent when the %s changes', (_name, nextProps) => {
    const first = mount()
    act(() => first.result.current.takeControl())
    first.unmount()

    const second = mount({}, initialTerminal, nextProps)
    const socket = StubSocket.last()
    act(() => socket.onopen?.())

    expect(socket.frames()[0]).not.toHaveProperty('write')
    expect(useStore.getState().terminalWriteIntents.run_1).toBeUndefined()
    second.unmount()
  })

  it('clears intent after steering authorization is denied', () => {
    const mounted = mount()
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: true, replay: 0, has_control: false }),
      })
      mounted.result.current.takeControl()
      socket.onmessage?.({
        data: JSON.stringify({
          type: 'control',
          request_id: 1,
          ok: false,
          code: -32001,
          error: 'permission denied',
          has_control: false,
          control_generation: 0,
        }),
      })
    })

    expect(mounted.result.current.state.steerDenied).toBe(true)
    expect(useStore.getState().terminalWriteIntents.run_1).toBeUndefined()
    mounted.unmount()
  })

  it('clears intent when the requested control lease is lost', () => {
    const first = mount()
    act(() => first.result.current.takeControl())
    first.unmount()

    const second = mount()
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: -32003,
          error: 'control occupied',
          has_control: false,
          control_generation: 1,
        }),
      })
    })

    expect(second.result.current.state.steerDenied).toBe(false)
    expect(useStore.getState().terminalWriteIntents.run_1).toBeUndefined()
    second.unmount()
  })

  it('reconnects an owner as a mirror after an occupied write lease', () => {
    vi.useFakeTimers()
    const mounted = mount({}, initialTerminal, { currentAutomaticWrite: true })
    const socket = StubSocket.last()
    act(() => socket.onopen?.())
    expect(socket.frames()[0]).toMatchObject({ write: true })
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: -32003,
          error: 'control occupied',
          has_control: false,
        }),
      })
      socket.onclose?.({ code: 1008 })
    })
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    const retry = StubSocket.last()
    act(() => retry.onopen?.())
    expect(retry.frames()[0]).not.toHaveProperty('write')
    mounted.unmount()
    vi.useRealTimers()
  })

  it('clears intent when the run is deleted or its authority changes', () => {
    const deleted = mount()
    act(() => deleted.result.current.takeControl())
    act(() => {
      useStore.setState({ runs: {} })
      deleted.rerender({})
    })
    expect(useStore.getState().terminalWriteIntents.run_1).toBeUndefined()
    deleted.unmount()

    const changed = mount()
    act(() => changed.result.current.takeControl())
    changed.rerender({ currentAuthorityKey: 'mem_alice:viewer:mem_alice:false:' })
    expect(useStore.getState().terminalWriteIntents.run_1).toBeUndefined()
    changed.unmount()
  })

  it('revokes a writable lease before authority-change layout consumers can send', () => {
    const onAuthorityLayout = vi.fn((session: RunTerminalSessionResult) => {
      expect(session.state.write).toBe(false)
      expect(session.controlMetadata).toBeUndefined()
      expect(useStore.getState().terminals.run_1?.write).toBe(false)
      session.send('stale input')
    })
    const mounted = mount()
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
    })
    const header = socket.frames().at(-1) as { control_session_id?: string }
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          control_session_id: header.control_session_id,
          control_generation: 4,
          has_control: true,
        }),
      })
    })
    expect(mounted.result.current.state.write).toBe(true)
    expect(mounted.result.current.controlMetadata).toMatchObject({
      control_session_id: header.control_session_id,
      control_generation: 4,
      has_control: true,
    })
    const frameCount = socket.frames().length

    mounted.rerender({
      currentAuthorityKey: 'mem_alice:viewer:mem_alice:false:',
      onAuthorityLayout,
    })

    expect(onAuthorityLayout).toHaveBeenCalledTimes(1)
    expect(socket.frames().slice(frameCount)).toEqual([
      {
        type: 'control',
        request_id: 1,
        write: false,
        control_generation: 4,
      },
    ])
    mounted.unmount()
  })

  it('puts a pre-ack control request on the attach header', () => {
    const mounted = mount()
    const socket = StubSocket.last()
    act(() => mounted.result.current.takeControl(true))
    act(() => socket.onopen?.())
    expect(socket.frames()[0]).toMatchObject({ write: true, takeover: true })
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          control_generation: 0,
          has_control: false,
        }),
      })
    })
    expect(
      socket.frames().filter(
        (frame) =>
          typeof frame === 'object' &&
          frame !== null &&
          'type' in frame &&
          frame.type === 'control',
      ),
    ).toEqual([])
    mounted.unmount()
  })

  it('releases a write grant when authority changes after the header is sent', () => {
    const mounted = mount({}, initialTerminal, { currentAutomaticWrite: true })
    const socket = StubSocket.last()
    act(() => socket.onopen?.())
    const header = socket.frames()[0] as { write?: boolean; control_session_id?: string }
    expect(header.write).toBe(true)
    mounted.rerender({
      currentAutomaticWrite: false,
      currentAuthorityKey: 'mem_alice:viewer:mem_alice:false:',
    })
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          ok: true,
          replay: 0,
          control_session_id: header.control_session_id,
          control_generation: 3,
          has_control: true,
        }),
      })
    })
    expect(socket.frames().at(-1)).toMatchObject({
      type: 'control',
      write: false,
      control_generation: 3,
    })
    mounted.unmount()
  })

  it('resets displayed state before xterm becomes available', () => {
    const stale = {
      connection: 'offline' as const,
      write: true,
      steerDenied: true,
      message: 'old refusal',
      refused: true,
    }
    const mounted = mount({}, stale, { currentTerminal: null })

    expect(mounted.result.current.state).toEqual(initialTerminal)
    expect(mounted.result.current.controlMetadata).toBeUndefined()
    expect(mounted.result.current.sessionMissing).toBe(false)
    expect(mounted.result.current.replaying).toBe(false)
    expect(StubSocket.opened).toHaveLength(0)
    mounted.unmount()
  })

  it('restores a full replay only after geometry and the final xterm callback', async () => {
    const order: string[] = []
    let geometryDone!: () => void
    let writeDone!: () => void
    const mounted = mount({
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
    const mounted = mount({
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
    mounted.rerender({})

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

  it('orders refusal reset and replay cancellation', async () => {
    const order: string[] = []
    let resetDone!: () => void
    let cancelDone!: () => void
    let geometryCalls = 0
    const mounted = mount({
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
      ]),
    )
    mounted.unmount()
  })

  it('cancels structural replay through its original controller on replacement', () => {
    const ownerCancel = vi.fn(async () => {})
    const replacementCancel = vi.fn(async () => {})
    const mounted = mount({
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
    mounted.rerender({ currentTerminal: fakeTerminal() })

    expect(ownerCancel).toHaveBeenCalledWith(13)
    expect(replacementCancel).not.toHaveBeenCalled()
    mounted.unmount()
  })

  it('keeps stale refusal cleanup from revealing a replacement replay', async () => {
    let resetDone!: () => void
    let cancelDone!: () => void
    let geometryCalls = 0
    let generation = 0
    const replacementCancel = vi.fn(async () => {})
    const mounted = mount({
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
    mounted.rerender({})

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
    expect(mounted.result.current.replaying).toBe(true)
    expect(mounted.cancelStructuralReplay).toHaveBeenCalledTimes(1)
    mounted.unmount()
  })

  it('closes the attachment and cancels structural replay on unmount', () => {
    const mounted = mount()
    const socket = StubSocket.last()
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: true, cols: 80, rows: 24, replay: 3 }),
      })
    })

    mounted.unmount()

    expect(socket.closed).toBe(true)
    expect(socket.onmessage).toBeNull()
    expect(socket.onclose).toBeNull()
    expect(mounted.cancelStructuralReplay).toHaveBeenCalledWith(1)
  })
})

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useStore } from '@/store'
import {
  emitShellSocketData,
  initialTerminal,
  initialRunShellDock,
  registerShellSocket,
  subscribeShellSocket,
  type RunShellSocket,
} from '@/store/terminal'

function socket(close = vi.fn()): RunShellSocket {
  return { close, send: vi.fn(), resize: vi.fn(), reopen: vi.fn(), rebind: vi.fn(), suspend: vi.fn(), resume: vi.fn() }
}

describe('run-shell dock state', () => {
  beforeEach(() => {
    useStore.setState({ shellDocks: {} })
  })

  it('names tabs t1 through t4 and caps each run at four tabs', () => {
    const store = useStore.getState()

    expect(store.openShellTab('run_1')).toBe('t1')
    expect(store.openShellTab('run_1')).toBe('t2')
    expect(store.openShellTab('run_1')).toBe('t3')
    expect(store.openShellTab('run_1')).toBe('t4')
    expect(store.openShellTab('run_1')).toBeNull()
    expect(useStore.getState().shellDocks.run_1).toMatchObject({
      tabs: ['t1', 't2', 't3', 't4'],
      activeTab: 't4',
      collapsed: initialRunShellDock.collapsed,
      refusedMessage: null,
    })
  })

  it('reuses the first free name and closes the socket with a tab', () => {
    const first = useStore.getState().openShellTab('run_1')
    const second = useStore.getState().openShellTab('run_1')
    expect(first).toBe('t1')
    expect(second).toBe('t2')

    const close = vi.fn()
    registerShellSocket('run_1', 't1', socket(close))
    useStore.getState().closeShellTab('run_1', 't1')

    expect(close).toHaveBeenCalledOnce()
    expect(useStore.getState().shellDocks.run_1).toMatchObject({
      tabs: ['t2'],
      activeTab: 't2',
    })
    expect(useStore.getState().openShellTab('run_1')).toBe('t1')
  })

  it('delivers shell output kinds and settled callbacks to subscribers', () => {
    const received: Array<[Uint8Array, 'replay' | 'replay-end' | 'live']> = []
    let settled: (() => void) | undefined
    const unsubscribe = subscribeShellSocket('run_1', 't1', (chunk, kind, done) => {
      received.push([chunk, kind])
      settled = done
    })
    const live = new Uint8Array([1, 2])
    const completion = vi.fn()

    expect(emitShellSocketData('run_1', 't1', live, 'live', completion)).toBeUndefined()

    expect(received).toEqual([[live, 'live']])
    expect(settled).toBe(completion)
    expect(completion).not.toHaveBeenCalled()
    settled?.()
    expect(completion).toHaveBeenCalledOnce()
    unsubscribe()

    const noListenerCompletion = vi.fn()
    emitShellSocketData('run_1', 't1', live, 'live', noListenerCompletion)
    expect(noListenerCompletion).toHaveBeenCalledOnce()
  })

  it('returns one listener replay completion without wrapping it', async () => {
    let resolveCompletion!: () => void
    const completion = new Promise<void>((resolve) => {
      resolveCompletion = resolve
    })
    const unsubscribe = subscribeShellSocket('run_1', 't1', () => completion)

    const result = emitShellSocketData('run_1', 't1', new Uint8Array([1]), 'replay-end')

    expect(result).toBe(completion)
    resolveCompletion()
    await result
    unsubscribe()
  })

  it('waits for every asynchronous shell listener', async () => {
    let resolveFirst!: () => void
    let resolveSecond!: () => void
    const first = new Promise<void>((resolve) => {
      resolveFirst = resolve
    })
    const second = new Promise<void>((resolve) => {
      resolveSecond = resolve
    })
    const unsubscribeFirst = subscribeShellSocket('run_1', 't1', () => first)
    const unsubscribeSecond = subscribeShellSocket('run_1', 't1', () => second)

    const result = emitShellSocketData('run_1', 't1', new Uint8Array([1]), 'replay-end')

    if (!result) throw new Error('expected asynchronous shell listener completion')
    let settled = false
    void result.then(() => {
      settled = true
    })
    resolveFirst()
    await Promise.resolve()
    expect(settled).toBe(false)
    resolveSecond()
    await result
    expect(settled).toBe(true)
    unsubscribeFirst()
    unsubscribeSecond()
  })

  it('records a shell refusal without changing the tab list', () => {
    useStore.getState().openShellTab('run_1')
    useStore.getState().setShellRefused('run_1', 'You cannot open a shell')

    expect(useStore.getState().shellDocks.run_1).toMatchObject({
      tabs: ['t1'],
      refusedMessage: 'You cannot open a shell',
    })
  })
})

describe('run terminal steering', () => {
  it('starts as a mirror until the terminal view identifies the owner', () => {
    expect(initialTerminal.write).toBe(false)
  })
})

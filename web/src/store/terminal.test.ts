import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useStore } from '@/store'
import {
  initialRunShellDock,
  registerShellSocket,
  type RunShellSocket,
} from '@/store/terminal'

function socket(close = vi.fn()): RunShellSocket {
  return { close, send: vi.fn(), resize: vi.fn(), reopen: vi.fn() }
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

  it('records a shell refusal without changing the tab list', () => {
    useStore.getState().openShellTab('run_1')
    useStore.getState().setShellRefused('run_1', 'You cannot open a shell')

    expect(useStore.getState().shellDocks.run_1).toMatchObject({
      tabs: ['t1'],
      refusedMessage: 'You cannot open a shell',
    })
  })
})

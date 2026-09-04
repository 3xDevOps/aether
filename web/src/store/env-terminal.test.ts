import { describe, expect, it, vi } from 'vitest'
import { useStore } from '@/store'
import {
  hasEnvTerminalLineSent,
  initialEnvTerminal,
  markEnvTerminalLineSent,
  registerEnvTerminalSocket,
  unregisterEnvTerminalSocket,
  type EnvTerminalSocket,
} from '@/store/env-terminal'

function socket(): EnvTerminalSocket {
  return {
    send: vi.fn(),
    resize: vi.fn(),
    reopen: vi.fn(),
    close: vi.fn(),
  }
}

describe('environment terminal slice', () => {
  it('opens main first, allocates the next free numbered tab, and closes tabs', () => {
    useStore.setState({ envTerminal: initialEnvTerminal })
    const state = useStore.getState()

    expect(state.openEnvTerminalTab()).toBe('main')
    expect(state.openEnvTerminalTab()).toBe('t2')
    expect(useStore.getState().envTerminal.tabs).toEqual(['main', 't2'])

    state.closeEnvTerminalTab('t2')
    expect(useStore.getState().envTerminal.tabs).toEqual(['main'])
    expect(useStore.getState().envTerminal.activeTab).toBe('main')
  })

  it('sends a complete line through the selected tab socket', () => {
    useStore.setState({ envTerminal: { ...initialEnvTerminal, tabs: ['main'] } })
    const connection = socket()
    registerEnvTerminalSocket('main', connection)

    useStore.getState().sendLine('main', 'install claude')

    expect(connection.send).toHaveBeenCalledWith('install claude\n')
    connection.close()
  })

  it('clears sent-line markers when a terminal socket session ends', () => {
    useStore.getState().resetEnvTerminal()
    const first = socket()
    registerEnvTerminalSocket('main', first)
    markEnvTerminalLineSent('main', 'install claude')
    expect(hasEnvTerminalLineSent('main', 'install claude')).toBe(true)

    const second = socket()
    registerEnvTerminalSocket('main', second)
    expect(first.close).toHaveBeenCalled()
    expect(hasEnvTerminalLineSent('main', 'install claude')).toBe(false)

    markEnvTerminalLineSent('main', 'install claude')
    unregisterEnvTerminalSocket('main')
    expect(hasEnvTerminalLineSent('main', 'install claude')).toBe(false)
  })
})

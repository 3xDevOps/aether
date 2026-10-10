import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DevTerminal } from '@/lib/types'
import { useStore } from '@/store'
import { registerShellSocket, unregisterShellSocket, type RunShellSocket } from '@/store/terminal'

function terminal(id: string, incarnation = `${id}-process`): DevTerminal {
  return { terminal_id: id, incarnation, name: id, started_by: 'run_agent', cols: 80, rows: 24, process: { state: 'running' } }
}
function socket(close = vi.fn()): RunShellSocket {
  return {
    close, send: vi.fn(), resize: vi.fn(), reopen: vi.fn(), rebind: vi.fn(),
    suspend: vi.fn(), resume: vi.fn(), resetWriteDenial: vi.fn(),
    setControl: vi.fn(), isEnded: () => false,
    requestTakeover: vi.fn(() => false),
  }
}

describe('authoritative development terminals', () => {
  beforeEach(() => {
    unregisterShellSocket('run_1', 'agent-command')
    useStore.setState({ shellDocks: {} })
  })

  it('keeps a hidden process hidden across discovery and permits explicit rejoin', () => {
    const command = terminal('agent-command')
    useStore.getState().syncShellTerminals('run_1', [command])
    const close = vi.fn()
    registerShellSocket('run_1', command.terminal_id, socket(close))
    useStore.getState().closeShellTab('run_1', command.terminal_id)
    useStore.getState().syncShellTerminals('run_1', [command, terminal('agent-created-later')])
    expect(close).toHaveBeenCalledOnce()
    expect(useStore.getState().shellDocks.run_1.tabs).toEqual(['agent-created-later'])
    expect(useStore.getState().shellDocks.run_1.terminals).toEqual([command, terminal('agent-created-later')])
    useStore.getState().selectShellTab('run_1', command.terminal_id)
    expect(useStore.getState().shellDocks.run_1.activeTab).toBe(command.terminal_id)
    expect(useStore.getState().shellDocks.run_1.tabs).toContain(command.terminal_id)
  })

  it('fences the old viewer on replacement and discovers the new incarnation', () => {
    const command = terminal('agent-command')
    useStore.getState().syncShellTerminals('run_1', [command])
    const close = vi.fn()
    registerShellSocket('run_1', command.terminal_id, socket(close))
    useStore.getState().syncShellTerminals('run_1', [terminal(command.terminal_id, 'replacement')])
    expect(close).toHaveBeenCalledOnce()
    expect(useStore.getState().shellDocks.run_1.terminals[0].incarnation).toBe('replacement')
  })

  it('retains ended process state while detaching the obsolete writer', () => {
    const command = terminal('agent-command')
    useStore.getState().syncShellTerminals('run_1', [command])
    const close = vi.fn()
    registerShellSocket('run_1', command.terminal_id, socket(close))
    useStore.getState().syncShellTerminals('run_1', [{ ...command, process: { state: 'exited', exit_code: 9 } }])
    expect(close).toHaveBeenCalledOnce()
    expect(useStore.getState().shellDocks.run_1).toMatchObject({
      activeTab: 'agent-command', tabs: ['agent-command'],
      terminals: [{ process: { state: 'exited', exit_code: 9 } }],
    })
  })

  it('never opens a tab for a shell that had already ended, or reopens one that was closed', () => {
    const ended: DevTerminal = { ...terminal('finished'), process: { state: 'exited', exit_code: 0 } }
    const command = terminal('agent-command')
    useStore.getState().syncShellTerminals('run_1', [ended, command])
    expect(useStore.getState().shellDocks.run_1.tabs).toEqual(['agent-command'])
    const stopped: DevTerminal = { ...command, process: { state: 'stopped', exit_code: 143 } }
    useStore.getState().syncShellTerminals('run_1', [ended, stopped])
    expect(useStore.getState().shellDocks.run_1.tabs).toEqual(['agent-command'])
    useStore.getState().closeShellTab('run_1', command.terminal_id)
    useStore.getState().syncShellTerminals('run_1', [ended, stopped])
    expect(useStore.getState().shellDocks.run_1).toMatchObject({ tabs: [], activeTab: null, shellShown: false })
  })
})

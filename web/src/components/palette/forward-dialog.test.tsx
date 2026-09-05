import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { ForwardDialog } from '@/components/palette/forward-dialog'
import { runCommands } from '@/lib/commands'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const forwardMocks = vi.hoisted(() => ({
  localForwardStart: vi.fn(),
  localForwardStop: vi.fn(),
  localForwardStatus: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: forwardMocks }))

beforeEach(() => {
  vi.clearAllMocks()
  forwardMocks.localForwardStatus.mockResolvedValue({
    forwards: [
      { run_id: 'run_1', port: 1455, local_port: 1455, conns: 1 },
      { run_id: 'run_2', port: 3000, local_port: 3000, conns: 0 },
    ],
  })
  forwardMocks.localForwardStart.mockResolvedValue({
    run_id: 'run_1',
    port: 1455,
    local_port: 1455,
    state: 'active',
  })
  forwardMocks.localForwardStop.mockResolvedValue({
    run_id: 'run_1',
    port: 1455,
    state: 'stopped',
  })
  useStore.setState({
    paletteDialog: 'forward',
    paletteRunID: 'run_1',
  })
})

describe('forward dialog', () => {
  it('starts and stops a forward for the focused run', async () => {
    render(<ForwardDialog />)

    await waitFor(() =>
      expect(api.localForwardStatus).toHaveBeenCalledWith(),
    )
    expect(screen.queryByText('Port 3000')).toBeNull()
    expect((screen.getByLabelText('Port') as HTMLInputElement).value).toBe('1455')
    expect(screen.getByText('Port 1455')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() =>
      expect(api.localForwardStart).toHaveBeenCalledWith('run_1', 1455),
    )

    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    await waitFor(() =>
      expect(api.localForwardStop).toHaveBeenCalledWith('run_1', 1455),
    )
  })
})

describe('forward command', () => {
  const context = (local: boolean, status: 'running' | 'failed') => ({
    run: toRecord(run({ status })),
    paused: false,
    cap: {
      hasMethod: () => true,
      hasLocal: (verb: string) => local && verb === 'forward.start',
      hasWS: () => true,
    },
    members: {},
    self: { id: 'mem_alice' as string, role: 'collaborator' as const },
  })

  it('requires the local gateway and an unfinished run', () => {
    expect(runCommands(context(false, 'running')).some((command) => command.id === 'forward')).toBe(false)
    expect(runCommands(context(true, 'running')).some((command) => command.id === 'forward')).toBe(true)
    expect(runCommands(context(true, 'failed')).some((command) => command.id === 'forward')).toBe(false)
  })
})

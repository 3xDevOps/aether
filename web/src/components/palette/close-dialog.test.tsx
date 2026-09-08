import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { CloseDialog } from '@/components/palette/close-dialog'
import { api } from '@/lib/api'
import { runCommands } from '@/lib/commands'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const closeMocks = vi.hoisted(() => ({
  runClose: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: closeMocks }))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

beforeEach(() => {
  vi.clearAllMocks()
  closeMocks.runClose.mockResolvedValue(run({ status: 'merged' }))
  useStore.setState({
    paletteDialog: 'close',
    paletteRunID: 'run_1',
    runs: { run_1: toRecord(run({ id: 'run_1', status: 'completed' })) },
  })
})

describe('close dialog', () => {
  it('records the outcome the member picks', async () => {
    render(<CloseDialog />)
    fireEvent.click(screen.getByRole('button', { name: 'Merged' }))
    await waitFor(() => expect(api.runClose).toHaveBeenCalledWith('run_1', 'merged'))
    expect(useStore.getState().paletteDialog).toBeNull()
  })

  it('records abandoned too', async () => {
    render(<CloseDialog />)
    fireEvent.click(screen.getByRole('button', { name: 'Abandoned' }))
    await waitFor(() => expect(api.runClose).toHaveBeenCalledWith('run_1', 'abandoned'))
  })
})

// Delete is server-safe throughout the lifecycle. Close resolves the
// outcome from any state that holds a record; only a queued run (no
// record yet) cannot be closed.
describe('ending commands by stage', () => {
  const ids = (
    status:
      | 'queued'
      | 'provisioning'
      | 'running'
      | 'needs-attention'
      | 'completed'
      | 'merged'
      | 'abandoned'
      | 'failed'
      | 'interrupted',
  ) =>
    runCommands({
      run: toRecord(run({ status })),
      paused: false,
      cap: { hasMethod: () => true, hasLocal: () => false, hasWS: () => true },
      members: {},
      self: { id: 'mem_alice' as string, role: 'collaborator' as const },
    }).map((command) => command.id)

  it('offers delete and close at every stage with a record, kill only while live', () => {
    for (const status of ['queued', 'provisioning', 'running'] as const) {
      const live = ids(status)
      expect(live).toContain('kill')
      expect(live).toContain('delete')
    }
    // Queued has no record to close yet.
    expect(ids('queued')).not.toContain('close')
    for (const status of [
      'provisioning',
      'running',
      'needs-attention',
      'completed',
      'merged',
      'abandoned',
      'failed',
      'interrupted',
    ] as const) {
      expect(ids(status)).toContain('close')
      expect(ids(status)).toContain('delete')
    }
  })

  it('hides completed-run actions when the Kill policy denies them', () => {
    const commands = runCommands({
      run: toRecord(run({ status: 'completed', protected: true })),
      paused: false,
      cap: { hasMethod: () => true, hasLocal: () => false, hasWS: () => true },
      members: {},
      self: { id: 'mem_bob', role: 'collaborator' },
      steerOthers: 'admins_only',
    }).map((command) => command.id)

    expect(commands).not.toContain('close')
    expect(commands).not.toContain('delete')
  })
})

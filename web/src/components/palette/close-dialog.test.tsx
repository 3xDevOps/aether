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
    runs: { run_1: toRecord(run({ id: 'run_1', status: 'needs-attention' })) },
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

// One ending action per lifecycle stage: Kill while the agent is alive,
// Close while it waits for review, Delete once it has ended.
describe('ending commands by stage', () => {
  const context = (status: 'running' | 'needs-attention' | 'merged') => ({
    run: toRecord(run({ status })),
    paused: false,
    cap: { hasMethod: () => true, hasLocal: () => false, hasWS: () => true },
    members: {},
    self: { id: 'mem_alice' as string, role: 'collaborator' as const },
  })
  const ids = (status: 'running' | 'needs-attention' | 'merged') =>
    runCommands(context(status)).map((c) => c.id)

  it('offers exactly one ending action per stage', () => {
    const live = ids('running')
    expect(live).toContain('kill')
    expect(live).not.toContain('close')
    expect(live).not.toContain('delete')

    const waiting = ids('needs-attention')
    expect(waiting).toContain('close')
    expect(waiting).toContain('delete')
    expect(waiting).not.toContain('kill')

    const ended = ids('merged')
    expect(ended).toContain('delete')
    expect(ended).not.toContain('kill')
    expect(ended).not.toContain('close')
  })
})

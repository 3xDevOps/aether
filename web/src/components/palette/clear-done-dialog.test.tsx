import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { ClearDoneDialog } from '@/components/palette/clear-done-dialog'
import type { ClearDonePlan } from '@/lib/commands'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const archiveMocks = vi.hoisted(() => ({
  runArchive: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: archiveMocks }))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const plan: ClearDonePlan = {
  eligible: [
    toRecord(run({ id: 'run_a', status: 'merged', finished_at: '2026-08-14T10:15:00Z' })),
    toRecord(run({ id: 'run_b', status: 'failed', finished_at: '2026-08-14T10:10:00Z' })),
  ],
  notClosed: 1,
  notAllowed: 0,
}

beforeEach(() => {
  vi.clearAllMocks()
  archiveMocks.runArchive.mockImplementation(async (id: string) =>
    run({ id, status: 'merged', archived_at: '2026-08-14T11:00:00Z' }),
  )
  useStore.setState({
    paletteDialog: 'clear-done',
    paletteClearDonePlan: plan,
    runs: {},
  })
})

// The palette's "Clear done runs" entry hands its plan to the same
// ClearDoneConfirm the Done header's button renders, so this only has to
// prove the palette wires it up: the dialog reads the snapshot it was
// given and archiving runs the same sequential loop.
describe('clear done dialog', () => {
  it('archives every eligible run in order and closes the palette form', async () => {
    render(<ClearDoneDialog />)
    expect(screen.getByText('Archive 2 finished runs?')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Archive 2' }))

    await waitFor(() => expect(useStore.getState().paletteDialog).toBeNull())
    expect(archiveMocks.runArchive).toHaveBeenNthCalledWith(1, 'run_a', true)
    expect(archiveMocks.runArchive).toHaveBeenNthCalledWith(2, 'run_b', true)
  })

  it('cancels without calling the gateway', () => {
    render(<ClearDoneDialog />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(useStore.getState().paletteDialog).toBeNull()
    expect(archiveMocks.runArchive).not.toHaveBeenCalled()
  })

  it('renders nothing once the plan is gone', () => {
    useStore.setState({ paletteClearDonePlan: null })
    const { container } = render(<ClearDoneDialog />)
    expect(container.innerHTML).toBe('')
  })
})

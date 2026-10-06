import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { ClearDoneDialog, ReleaseFinishedDialog } from '@/components/palette/clear-done-dialog'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const archiveMocks = vi.hoisted(() => ({
  runArchive: vi.fn(),
  runRelease: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: archiveMocks }))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const closed = [
  run({ id: 'run_a', status: 'merged', finished_at: '2026-08-14T10:15:00Z' }),
  run({ id: 'run_b', status: 'failed', finished_at: '2026-08-14T10:10:00Z' }),
]

beforeEach(() => {
  vi.clearAllMocks()
  archiveMocks.runArchive.mockImplementation(async (id: string) =>
    run({ id, status: 'merged', archived_at: '2026-08-14T11:00:00Z' }),
  )
  useStore.setState({
    paletteDialog: 'clear-done',
    activeWorkspace: '',
    runs: Object.fromEntries(closed.map((r) => [r.id, toRecord(r)])),
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
  })
})

describe('archive confirmation dialog', () => {
  it('archives every eligible run in order and closes the palette form', async () => {
    render(<ClearDoneDialog />)

    fireEvent.click(screen.getByRole('button', { name: 'Archive 2' }))

    await waitFor(() => expect(useStore.getState().paletteDialog).toBeNull())
    expect(archiveMocks.runArchive).toHaveBeenNthCalledWith(1, 'run_b', true)
    expect(archiveMocks.runArchive).toHaveBeenNthCalledWith(2, 'run_a', true)
  })

  it('cancels without calling the gateway', () => {
    render(<ClearDoneDialog />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(useStore.getState().paletteDialog).toBeNull()
    expect(archiveMocks.runArchive).not.toHaveBeenCalled()
  })

  it('says so when no closed run qualifies', () => {
    useStore.setState({ runs: {} })
    render(<ClearDoneDialog />)
    expect(screen.getByText('No closed runs to archive')).toBeDefined()
    expect(screen.queryByText(/schedules their deletion/)).toBeNull()
    expect(screen.queryByRole('button', { name: /Archive/ })).toBeNull()
    fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    expect(useStore.getState().paletteDialog).toBeNull()
  })
})

describe('release confirmation dialog', () => {
  it('releases every finished run that keeps its container', async () => {
    useStore.setState({
      paletteDialog: 'release-finished',
      runs: {
        run_a: toRecord(run({ id: 'run_a', status: 'merged', reason: 'closed; retained container' })),
        run_b: toRecord(run({ id: 'run_b', status: 'merged' })),
      },
    })
    render(<ReleaseFinishedDialog />)

    fireEvent.click(screen.getByRole('button', { name: 'Free 1' }))

    await waitFor(() => expect(useStore.getState().paletteDialog).toBeNull())
    expect(archiveMocks.runRelease).toHaveBeenCalledTimes(1)
    expect(archiveMocks.runRelease).toHaveBeenCalledWith('run_a')
  })

  it('says so when no finished run keeps its container', () => {
    useStore.setState({ paletteDialog: 'release-finished' })
    render(<ReleaseFinishedDialog />)
    expect(screen.getByText('No finished runs keep a container')).toBeDefined()
    expect(screen.queryByText(/cannot be reopened/)).toBeNull()
    expect(screen.queryByRole('button', { name: /Free/ })).toBeNull()
    fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    expect(useStore.getState().paletteDialog).toBeNull()
    expect(archiveMocks.runRelease).not.toHaveBeenCalled()
  })
})

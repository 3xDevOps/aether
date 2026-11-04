import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { ClearDoneDialog, ReleaseFinishedDialog } from '@/components/palette/clear-done-dialog'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const archiveMocks = vi.hoisted(() => ({
  runArchive: vi.fn(),
  runRelease: vi.fn(),
  runCloseCheck: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: archiveMocks }))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const closed = [
  run({ id: 'run_a', status: 'merged', finished_at: '2026-08-14T10:15:00Z' }),
  run({ id: 'run_b', status: 'failed', finished_at: '2026-08-14T10:10:00Z' }),
]

beforeEach(() => {
  vi.clearAllMocks()
  archiveMocks.runCloseCheck.mockImplementation(async (ids: string[]) => ids.map((id) => ({ run_id: id, warnings: [] })))
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

    fireEvent.click(await screen.findByRole('button', { name: 'Archive 2' }))

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

  it('archives completed sessions too, keeping each one that still holds work with its reason', async () => {
    const completed = [
      run({ id: 'run_c', status: 'completed', review: 'accepted', finished_at: '2026-08-14T10:05:00Z' }),
      run({ id: 'run_d', status: 'completed', review: 'accepted', finished_at: '2026-08-14T10:00:00Z' }),
    ]
    useStore.setState({ runs: Object.fromEntries([...closed, ...completed].map((r) => [r.id, toRecord(r)])) })
    archiveMocks.runCloseCheck.mockImplementation(async (ids: string[]) =>
      ids.map((id) => ({
        run_id: id,
        warnings: id === 'run_d'
          ? [{ kind: 'pull_request', detail: 'Pull request acme/shop#42 was open, not merged', url: 'https://github.com/acme/shop/pull/42', number: 42 }, { kind: 'unmerged_work', detail: 'Commits may not be merged' }]
          : [{ kind: 'protected', detail: 'Protected' }],
      })),
    )
    render(<ClearDoneDialog />)

    expect(screen.getByText('Checking finished sessions…')).toBeDefined()
    const archive = await screen.findByRole('button', { name: 'Archive 3' })
    expect(archiveMocks.runCloseCheck).toHaveBeenCalledWith(['run_c', 'run_d'], 'completed')
    expect(screen.getByRole('link', { name: 'acme/shop#42' })).toBeDefined()
    fireEvent.click(archive)
    await waitFor(() => expect(useStore.getState().paletteDialog).toBeNull())
    expect(archiveMocks.runArchive.mock.calls.map(([id]) => id).sort()).toEqual(['run_a', 'run_b', 'run_c'])
  })

  it('keeps completed sessions it could not check, and checks again on request', async () => {
    useStore.setState({ runs: { run_c: toRecord(run({ id: 'run_c', status: 'completed', finished_at: '2026-08-14T10:05:00Z' })) } })
    archiveMocks.runCloseCheck.mockRejectedValueOnce(new Error('gateway unreachable'))
    render(<ClearDoneDialog />)

    expect(await screen.findByText(/gateway unreachable/)).toBeDefined()
    expect(screen.getByText('Nothing to archive')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))

    fireEvent.click(await screen.findByRole('button', { name: 'Archive 1' }))
    await waitFor(() => expect(archiveMocks.runArchive).toHaveBeenCalledWith('run_c', true))
  })

  it('says so when no finished run qualifies', async () => {
    useStore.setState({ runs: {} })
    render(<ClearDoneDialog />)
    expect(await screen.findByText('Nothing to archive')).toBeDefined()
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

  it('shows authoritative pool-independent runtime deadlines and pending failures before release', async () => {
    const deadline = '2030-02-03T04:05:06Z'
    useStore.setState({
      paletteDialog: 'release-finished',
      runs: {
        retained: toRecord(run({ id: 'retained', status: 'completed', container_retained_until: deadline, cleanup_pending: true, cleanup_error: 'Container cleanup failed' })),
        active: toRecord(run({ id: 'active', status: 'running', created_at: '2020-01-01T00:00:00Z' })),
      },
    })
    const view = render(<ReleaseFinishedDialog />)
    expect(view.container.ownerDocument.querySelector('time')?.getAttribute('datetime')).toBe(deadline)
    expect(screen.getByRole('status').textContent).toBe('Container cleanup failed')
    expect(archiveMocks.runRelease).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Free 1' }))
    await waitFor(() => expect(archiveMocks.runRelease).toHaveBeenCalledWith('retained'))
    expect(archiveMocks.runRelease).not.toHaveBeenCalledWith('active')
    expect(archiveMocks.runArchive).not.toHaveBeenCalled()
  })

  it('says so when no finished run keeps its container', () => {
    useStore.setState({ paletteDialog: 'release-finished' })
    render(<ReleaseFinishedDialog />)
    expect(screen.getByText('No finished sessions keep a container')).toBeDefined()
    expect(screen.queryByText(/cannot be reopened/)).toBeNull()
    expect(screen.queryByRole('button', { name: /Free/ })).toBeNull()
    fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    expect(useStore.getState().paletteDialog).toBeNull()
    expect(archiveMocks.runRelease).not.toHaveBeenCalled()
  })
})

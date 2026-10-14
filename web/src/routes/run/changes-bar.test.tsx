import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import type { FileDiffStat } from '@/lib/types'
import { ChangesBar } from '@/routes/run/changes-bar'
import { useStore } from '@/store'
import { diffSnapshot, initialDiff } from '@/store/diff'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const files: FileDiffStat[] = [
  { path: 'cmd/new.go', additions: 12, deletions: 0, status: 'added' },
  { path: 'internal/old.go', additions: 3, deletions: 5, status: 'modified' },
]

function seed(opts: { files?: FileDiffStat[]; truncated?: boolean; status?: 'running' | 'merged' } = {}) {
  const record = toRecord(run({ id: 'run_1', status: opts.status ?? 'running' }))
  useStore.setState({
    runs: { [record.id]: record },
    connection: 'live',
    expandedRows: {},
    route: { name: 'run', params: { runId: record.id } },
    diffs: opts.files
      ? { [record.id]: { ...initialDiff, snapshots: [diffSnapshot('2026-08-14T10:05:00Z', { files: opts.files, truncated: opts.truncated })] } }
      : {},
  })
  return record
}

beforeEach(() => {
  vi.mocked(api.workspaceTimeline).mockClear()
})

describe('ChangesBar', () => {
  it('shows totals collapsed, then lists the files when the toggle opens', () => {
    render(<ChangesBar run={seed({ files })} />)
    const toggle = screen.getByRole('button', { name: /2 files changed/ })
    expect(toggle.textContent).toContain('+15')
    expect(toggle.textContent).toContain('−5')
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByRole('list')).toBeNull()

    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    const rows = screen.getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('new.go')
    expect(rows[0].textContent).toContain(', added')
    expect(rows[0].textContent).toContain('+12')
    expect(rows[1].textContent).toContain('old.go')
    expect(rows[1].textContent).toContain(', modified')
    expect(rows[1].textContent).toContain('−5')

    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByRole('list')).toBeNull()
  })

  it('opens the Changes view at a file or at the whole review', () => {
    render(<ChangesBar run={seed({ files })} />)
    fireEvent.click(screen.getByRole('button', { name: /2 files changed/ }))
    fireEvent.click(screen.getByRole('button', { name: /old\.go/ }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1', view: 'changes', file: 'internal/old.go' } })

    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1', view: 'changes' } })
  })

  it('renders nothing without files or once the run is merged', () => {
    const empty = render(<ChangesBar run={seed({ files: [] })} />)
    expect(empty.container.innerHTML).toBe('')
    empty.unmount()

    const merged = render(<ChangesBar run={seed({ files, status: 'merged' })} />)
    expect(merged.container.innerHTML).toBe('')
  })

  it('updates the count when a live snapshot arrives', () => {
    render(<ChangesBar run={seed({ files })} />)
    expect(screen.getByText('2 files changed')).not.toBeNull()
    act(() => {
      useStore.getState().noteDiffSnapshot('run_1', diffSnapshot('2026-08-14T10:06:00Z', { files: [...files, { path: 'README.md', additions: 1, deletions: 0, status: 'added' }] }))
    })
    expect(screen.getByText('3 files changed')).not.toBeNull()
  })

  it('reads the newest persisted snapshot once when none is known, and retries a failure', async () => {
    const timeline = vi.mocked(api.workspaceTimeline)
    const record = seed()
    timeline.mockRejectedValueOnce(new Error('timeline unavailable'))
    render(<ChangesBar run={record} />)
    expect((await screen.findByRole('alert')).textContent).toContain('Changes could not be read: timeline unavailable')
    expect(timeline).toHaveBeenCalledTimes(1)
    expect(timeline).toHaveBeenCalledWith({ workspace_id: record.workspace_id, run_id: 'run_1', types: ['run.diff'], newest: true, limit: 1 })

    timeline.mockResolvedValueOnce({
      events: [{ time: '2026-08-14T10:05:00Z', payload: { files } }],
    } as Awaited<ReturnType<typeof api.workspaceTimeline>>)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('2 files changed')).not.toBeNull()
    await waitFor(() => expect(timeline).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('shows a file count but no totals for a truncated snapshot', () => {
    render(<ChangesBar run={seed({ files, truncated: true })} />)
    const toggle = screen.getByRole('button', { name: /2 files changed/ })
    expect(toggle.textContent).not.toContain('+')
    expect(toggle.textContent).not.toContain('−')
    fireEvent.click(toggle)
    expect(screen.getAllByRole('listitem')[0].textContent).not.toContain('+12')
  })
})

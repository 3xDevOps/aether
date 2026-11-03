import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CloseConfirmation } from '@/components/close-warnings'
import { api } from '@/lib/api'
import { runCommands, useCommandRunner } from '@/lib/commands'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, serverInfo } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return { ...actual, api: fakeApi() }
})
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), dismiss: vi.fn() } }))

function MarkComplete() {
  const perform = useCommandRunner()
  const record = useStore((s) => s.runs.run_1)!
  const complete = runCommands({
    run: record,
    paused: false,
    cap: { hasMethod: () => true, hasLocal: () => false, hasWS: () => true },
    members: { [alice.id]: alice },
    self: { id: alice.id, role: alice.role },
  }).find((command) => command.id === 'complete')!
  return <button onClick={() => void perform(complete)}>Mark complete</button>
}

beforeEach(() => {
  vi.mocked(api.runReview).mockClear()
  useStore.setState({
    info: { ...serverInfo, member: alice },
    runs: { run_1: toRecord(run({ id: 'run_1', status: 'needs-attention', state: 'finished', reason: 'agent reported success', review: 'ready' })) },
    predicting: {},
    closeConfirmation: null,
  })
  vi.mocked(api.runCloseCheck).mockResolvedValueOnce([{ run_id: 'run_1', warnings: [
    { kind: 'unmerged_work', detail: 'Commits on aether/run-1 are in no merged pull request' },
    { kind: 'protected', detail: 'Protected: only its owner or an admin can steer, message or close it' },
  ] }])
})

describe('Mark complete with close warnings', () => {
  it('asks first, listing each warning, and runs only on Close anyway', async () => {
    render(<><MarkComplete /><CloseConfirmation /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Close this session anyway?' })
    expect(dialog.textContent).toContain('Commits on aether/run-1 are in no merged pull request. The branch stays, but nothing merges it.')
    expect(dialog.textContent).toContain('Protected: only its owner or an admin can steer, message or close it.')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }))
    expect(api.runReview).not.toHaveBeenCalled()
    expect(useStore.getState().runs.run_1.review).toBe('ready')
    fireEvent.click(screen.getByRole('button', { name: 'Close anyway' }))
    await waitFor(() => expect(api.runReview).toHaveBeenCalledWith('run_1', 'complete'))
    expect(api.runCloseCheck).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('alertdialog')).toBeNull()
  })

  it('does nothing on Cancel', async () => {
    render(<><MarkComplete /><CloseConfirmation /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(api.runReview).not.toHaveBeenCalled()
  })
})

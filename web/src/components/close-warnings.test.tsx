import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CloseConfirmation, CloseWarnings } from '@/components/close-warnings'
import { Check } from '@/components/icons'
import { toast } from 'sonner'
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
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), dismiss: vi.fn() } }))

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
  vi.mocked(api.runCloseCheck).mockClear()
  vi.mocked(toast.info).mockClear()
  useStore.setState({
    info: { ...serverInfo, member: alice },
    runs: { run_1: toRecord(run({ id: 'run_1', status: 'needs-attention', state: 'finished', reason: 'agent reported success', review: 'ready' })) },
    predicting: {},
    closeConfirmation: null,
    closeChecking: {},
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

  it('checks once while a second press waits on the first', async () => {
    render(<><MarkComplete /><CloseConfirmation /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    await screen.findByRole('alertdialog')
    expect(api.runCloseCheck).toHaveBeenCalledTimes(1)
    expect(api.runCloseCheck).toHaveBeenCalledWith(['run_1'], 'completed')
  })

  it('drops the close when the session changed during a slow check, such as after Not yet', async () => {
    vi.mocked(api.runCloseCheck).mockReset()
    const answer = Promise.withResolvers<{ run_id: string; warnings: [] }[]>()
    vi.mocked(api.runCloseCheck).mockReturnValueOnce(answer.promise)
    render(<><MarkComplete /><CloseConfirmation /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    act(() => useStore.setState((s) => ({ runs: { run_1: { ...s.runs.run_1!, review: '' } } })))
    await act(async () => answer.resolve([{ run_id: 'run_1', warnings: [] }]))
    await waitFor(() => expect(toast.info).toHaveBeenCalledWith('The session changed while its close was being checked, so it was not closed'))
    expect(api.runReview).not.toHaveBeenCalled()
  })

  it('drops Close anyway when the session changed since the warnings were shown', async () => {
    render(<><MarkComplete /><CloseConfirmation /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    await screen.findByRole('alertdialog')
    act(() => useStore.setState((s) => ({ runs: { run_1: { ...s.runs.run_1!, status: 'running', state: 'working' } } })))
    fireEvent.click(screen.getByRole('button', { name: 'Close anyway' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(api.runReview).not.toHaveBeenCalled()
    expect(toast.info).toHaveBeenCalled()
  })

  it('drops the confirmation when its run is gone', async () => {
    render(<CloseConfirmation />)
    act(() => useStore.setState({ runs: {}, closeConfirmation: { check: { run_id: 'run_1', warnings: [{ kind: 'protected', detail: 'Protected' }] }, asked: '', command: { id: 'complete', label: 'Mark complete', Icon: Check, perform: () => {} } } }))
    await waitFor(() => expect(useStore.getState().closeConfirmation).toBeNull())
  })
})

describe('CloseWarnings', () => {
  it('names the subsession a warning is about, and shows a cleanup failure the session record does not have', () => {
    useStore.setState({ runs: { ...useStore.getState().runs, run_2: toRecord(run({ id: 'run_2', title: 'Worker' })), run_3: toRecord(run({ id: 'run_3', title: 'Watcher' })) } })
    render(<CloseWarnings run={useStore.getState().runs.run_1!} onLeave={() => {}} check={{ run_id: 'run_1', warnings: [
      { kind: 'awaited', detail: 'Subsession run_2 (Worker): Waiting for it to finish: run_3 (Watcher)', run_ids: ['run_3'], subsession: 'run_2' },
      { kind: 'cleanup_pending', detail: 'Cleanup failed: remove container: device busy' },
    ] }} />)
    const list = screen.getByRole('list', { name: 'What closing affects' })
    expect(list.textContent).toContain('Subsession Worker: waiting for it to finish: Watcher. Closing wakes them with its outcome.')
    expect(list.textContent).toContain('Cleanup failed: remove container: device busy.')
  })
})

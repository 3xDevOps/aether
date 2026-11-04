import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { vi } from 'vitest'
import { CloseDialog } from '@/components/palette/close-dialog'
import { api, codeInvalidState } from '@/lib/api'
import type * as apiModule from '@/lib/api'
import { runCommands } from '@/lib/commands'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), ApiError: Error }
})
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), dismiss: vi.fn() } }))

const status = () => useStore.getState().runs.run_1?.status

beforeEach(() => {
  vi.clearAllMocks()
  useStore.setState({
    paletteDialog: 'close',
    paletteRunID: 'run_1',
    runs: { run_1: toRecord(run({ id: 'run_1', status: 'completed' })) },
    predicting: {},
  })
})

describe('close dialog', () => {
  it('closes at once and shows the outcome before the server answers', async () => {
    const answer = Promise.withResolvers<never>()
    vi.mocked(api.runClose).mockReturnValueOnce(answer.promise)
    render(<CloseDialog />)
    fireEvent.click(await screen.findByRole('button', { name: 'Merged' }))
    expect(api.runClose).toHaveBeenCalledWith('run_1', 'merged')
    expect(useStore.getState().paletteDialog).toBeNull()
    expect(status()).toBe('merged')
    expect(toast.success).not.toHaveBeenCalled()
    await act(async () => answer.resolve(run({ id: 'run_1', status: 'merged' }) as never))
    expect(toast.success).toHaveBeenCalledWith('Closed as merged')
  })

  it('puts the run back and shows the server\'s refusal', async () => {
    vi.mocked(api.runClose).mockRejectedValueOnce(Object.assign(new Error('session is finishing'), { code: -32001 }))
    vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', status: 'completed' }))
    render(<CloseDialog />)
    fireEvent.click(await screen.findByRole('button', { name: 'Abandoned' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Closed as abandoned failed: session is finishing'))
    expect(status()).toBe('completed')
  })

  it('keeps a close the server published before refusing, and shows the refusal', async () => {
    const answer = Promise.withResolvers<never>()
    vi.mocked(api.runClose).mockReturnValueOnce(answer.promise)
    vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', status: 'merged', reason: 'closed', state: undefined, status_seq: 1 }))
    render(<CloseDialog />)
    fireEvent.click(await screen.findByRole('button', { name: 'Merged' }))
    act(() => useStore.getState().applyRunStatus('run_1', { to: 'merged', reason: 'closed', status_seq: 1 }, new Date().toISOString()))
    await act(async () => answer.reject(Object.assign(new Error('run.close: scheduler: evidence session lookup: database is locked'), { code: -32603 })))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Closed as merged failed: evidence session lookup: database is locked'))
    expect(status()).toBe('merged')
  })

  it('counts a refusal of a close that already landed as success', async () => {
    vi.mocked(api.runClose).mockRejectedValueOnce(Object.assign(new Error('session finalization is in progress'), { code: codeInvalidState }))
    vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', status: 'merged', state: undefined }))
    render(<CloseDialog />)
    fireEvent.click(await screen.findByRole('button', { name: 'Merged' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Closed as merged'))
    expect(toast.error).not.toHaveBeenCalled()
    expect(status()).toBe('merged')
  })

  it('lists what closing affects, riskiest first, and makes abandoning the danger, with Cancel focused and ringed', async () => {
    useStore.setState({ runs: { run_1: toRecord(run({ id: 'run_1', status: 'completed' })), run_2: toRecord(run({ id: 'run_2', title: 'Coordinator' })) } })
    vi.mocked(api.runCloseCheck).mockResolvedValueOnce([{ run_id: 'run_1', warnings: [
      { kind: 'pull_request', detail: 'Pull request acme/api#7 is open, not merged', url: 'https://github.com/acme/api/pull/7' },
      { kind: 'awaited', detail: 'Waiting for it to finish: run_2 (Coordinator)', run_ids: ['run_2'] },
    ] }])
    render(<CloseDialog />)
    const merged = await screen.findByRole('button', { name: 'Close as merged' })
    const abandoned = screen.getByRole('button', { name: 'Close as abandoned' })
    expect(abandoned.className).toContain('bg-state-failed')
    expect(merged.className).not.toContain('bg-accent')
    const cancel = screen.getByRole('button', { name: 'Cancel' })
    expect(document.activeElement).toBe(cancel)
    expect(cancel.className).toContain('focus:outline-accent')
    const list = screen.getByRole('list', { name: 'What closing affects' })
    expect(list.textContent).toMatch(/^Pull request acme\/api#7 is open, not merged\..*Waiting for it to finish/)
    expect(screen.getByRole('link', { name: 'Open the pull request' }).getAttribute('href')).toBe('https://github.com/acme/api/pull/7')
    expect(list.textContent).toContain('Waiting for it to finish: Coordinator. Closing wakes them with its outcome.')
    fireEvent.click(merged)
    expect(api.runClose).toHaveBeenCalledWith('run_1', 'merged')
  })

  it('makes the outcome that was primary the danger when no work is at risk', async () => {
    vi.mocked(api.runCloseCheck).mockResolvedValueOnce([{ run_id: 'run_1', warnings: [{ kind: 'messages', detail: '1 message', count: 1 }] }])
    render(<CloseDialog />)
    expect((await screen.findByRole('button', { name: 'Close as merged' })).className).toContain('bg-state-failed')
    expect(screen.getByRole('button', { name: 'Close as abandoned' }).className).not.toContain('bg-state-failed')
  })

  it('keeps the calm Merged and Abandoned without warnings', async () => {
    render(<CloseDialog />)
    expect((await screen.findByRole('button', { name: 'Merged' })).className).toContain('bg-accent')
  })

  it('shows a failed check and still lets the owner close', async () => {
    vi.mocked(api.runCloseCheck).mockRejectedValueOnce(new Error('method not found: run.close_check'))
    render(<CloseDialog />)
    await screen.findByRole('button', { name: 'Close as merged' })
    expect(screen.getByRole('alert').textContent).toBe('Could not check what closing affects: method not found: run.close_check')
  })

  it('waits for the server, naming the choice, over another verb\'s pending prediction', async () => {
    const answer = Promise.withResolvers<never>()
    vi.mocked(api.runClose).mockReturnValueOnce(answer.promise)
    useStore.setState({ predicting: { run_1: {} } })
    render(<CloseDialog />)
    fireEvent.click(await screen.findByRole('button', { name: 'Merged' }))
    expect(screen.getByRole('button', { name: 'Closing…' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('button', { name: 'Abandoned' })).toHaveProperty('disabled', true)
    expect(useStore.getState().paletteDialog).toBe('close')
    expect(status()).toBe('completed')
    await act(async () => answer.resolve(run({ id: 'run_1', status: 'merged' }) as never))
    expect(useStore.getState().paletteDialog).toBeNull()
    expect(toast.success).toHaveBeenCalledWith('Closed as merged')
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

  it.each(['tui', 'acp'] as const)('keeps %s reports open and reopens only after explicit Close', (mode) => {
    const commandsFor = (over: Parameters<typeof run>[0]) => runCommands({
      run: toRecord(run({ mode, ...over })),
      paused: false,
      cap: { hasMethod: () => true, hasLocal: () => false, hasWS: () => true },
      members: {},
      self: { id: 'mem_alice', role: 'collaborator' },
    }).map((command) => command.id)
    const parked = commandsFor({
      status: 'needs-attention', reason: 'agent reported success', outcome_unseen: true,
    })
    expect(parked).toContain('inject')
    expect(parked).toContain('close')
    expect(parked).not.toContain('relaunch')
    expect(parked).not.toContain('archive')
    const closed = commandsFor({ status: 'abandoned', reason: 'closed; retained container' })
    expect(closed).toContain('relaunch')
    expect(closed).not.toContain('inject')
    expect(commandsFor({ status: 'abandoned', reason: 'retained container expired' })).not.toContain('relaunch')
    expect(commandsFor({ mode: 'headless', status: 'completed', reason: 'agent reported success; retained container' }))
      .not.toContain('relaunch')
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

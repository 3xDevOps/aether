import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { api, ApiError } from '@/lib/api'
import type { Run } from '@/lib/types'
import { CompletionPrompt } from '@/routes/run/completion-prompt'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, bob, run, serverInfo } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return { ...actual, api: fakeApi() }
})
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), dismiss: vi.fn() } }))

const reported = (over: Partial<Run> = {}) =>
  run({ status: 'needs-attention', state: 'finished', reason: 'agent reported success', review: 'ready', status_seq: 4, ...over })

function seed(over: Partial<Run> = {}) {
  const record = toRecord(reported(over))
  useStore.setState({
    info: { ...serverInfo, member: alice },
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: { [record.id]: record },
    predicting: {},
  })
}

function Prompt() {
  const record = useStore((s) => s.runs.run_1)
  return record ? <CompletionPrompt run={record} /> : null
}

const banner = () => screen.queryByRole('region', { name: 'Completion' })

beforeEach(() => {
  vi.mocked(api.runReview).mockClear()
  vi.mocked(toast.error).mockClear()
})

describe('CompletionPrompt', () => {
  it('asks while the reported success is ready, with the agent’s summary', async () => {
    seed()
    vi.mocked(api.workspaceTimeline).mockResolvedValueOnce({
      events: [
        { id: 'ev_1', seq: 1, time: '2026-08-14T10:00:00Z', workspace_id: 'wsp_1', run_id: 'run_1', actor_id: '', type: 'workspace.timeline', payload: { kind: 'report', outcome: 'success', summary: 'first pass' } },
        { id: 'ev_2', seq: 2, time: '2026-08-14T10:10:00Z', workspace_id: 'wsp_1', run_id: 'run_1', actor_id: '', type: 'workspace.timeline', payload: { kind: 'report', outcome: 'success', summary: 'checkout rewritten and tested' } },
      ],
      next_seq: 3,
      more: false,
    })
    render(<Prompt />)
    await waitFor(() => expect(banner()?.textContent).toContain('checkout rewritten and tested'))
    expect(banner()?.textContent).toContain('Your agent reports this task is finished. Mark it complete?')
    expect(screen.getByRole('button', { name: 'Mark complete' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Not yet' })).toBeDefined()
  })

  it('stays away after Not yet, and for a run that is not ready or not the viewer’s to answer', () => {
    seed({ review: 'later' })
    const { rerender } = render(<Prompt />)
    expect(banner()).toBeNull()
    seed({ review: '' })
    rerender(<Prompt />)
    expect(banner()).toBeNull()
    seed({ member_id: bob.id })
    useStore.setState({ info: { ...serverInfo, member: { ...alice, role: 'viewer' } } })
    rerender(<Prompt />)
    expect(banner()).toBeNull()
  })

  it('hides at once on Not yet and records it', async () => {
    seed()
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Not yet' }))
    expect(banner()).toBeNull()
    expect(useStore.getState().runs.run_1.review).toBe('later')
    await waitFor(() => expect(api.runReview).toHaveBeenCalledWith('run_1', 'later'))
  })

  it('completes the run at once on Mark complete', async () => {
    seed()
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    expect(banner()).toBeNull()
    expect(useStore.getState().runs.run_1).toMatchObject({ review: 'accepted', status: 'completed' })
    await waitFor(() => expect(api.runReview).toHaveBeenCalledWith('run_1', 'complete'))
  })

  it('puts the prompt back and shows the server’s error when Mark complete is refused', async () => {
    seed()
    vi.mocked(api.runReview).mockRejectedValueOnce(new ApiError(400, 'run is protected', -32602))
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    expect(banner()).toBeNull()
    await waitFor(() => expect(banner()).not.toBeNull())
    expect(useStore.getState().runs.run_1).toMatchObject({ review: 'ready', status: 'needs-attention' })
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining('run is protected'))
  })
})

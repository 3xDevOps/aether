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
    expect(banner()?.textContent).toContain('Your agent reports this task is finished. Mark complete?')
    const described = screen.getByRole('button', { name: 'Mark complete' }).getAttribute('aria-describedby')
    expect(document.getElementById(described ?? '')?.textContent).toBe('Closes the session.')
    for (const name of ['Mark complete', 'Not yet', 'Review later']) expect(screen.getByRole('button', { name })).toBeDefined()
    expect(document.activeElement).not.toBe(screen.getByRole('button', { name: 'Mark complete' }))
  })

  it('offers Show more only when the summary overflows three lines as laid out, and bounds it when opened', async () => {
    const sizes = (scroll: number) => {
      Object.defineProperty(HTMLParagraphElement.prototype, 'scrollHeight', { configurable: true, get: () => scroll })
      Object.defineProperty(HTMLParagraphElement.prototype, 'clientHeight', { configurable: true, get: () => 48 })
    }
    const report = (summary: string) => ({
      events: [{ id: 'ev_1', seq: 1, time: '2026-08-14T10:00:00Z', workspace_id: 'wsp_1', run_id: 'run_1', actor_id: '', type: 'workspace.timeline', payload: { kind: 'report', outcome: 'success', summary } }],
      next_seq: 2,
      more: false,
    })
    try {
      sizes(48)
      seed()
      vi.mocked(api.workspaceTimeline).mockResolvedValueOnce(report('fits in three lines'))
      const { unmount } = render(<Prompt />)
      await waitFor(() => expect(banner()?.textContent).toContain('fits in three lines'))
      expect(screen.queryByRole('button', { name: 'Show more' })).toBeNull()
      unmount()

      sizes(96)
      seed({ review_changed_at: '2026-08-14T10:11:00Z' })
      vi.mocked(api.workspaceTimeline).mockResolvedValueOnce(report('short, but wraps past three lines on a phone'))
      render(<Prompt />)
      fireEvent.click(await screen.findByRole('button', { name: 'Show more' }))
      expect(screen.getByText('short, but wraps past three lines on a phone').className).toContain('overflow-y-auto')
      fireEvent.click(screen.getByRole('button', { name: 'Show less' }))
      expect(screen.getByText('short, but wraps past three lines on a phone').className).toContain('line-clamp-3')
    } finally {
      delete (HTMLParagraphElement.prototype as { scrollHeight?: number }).scrollHeight
      delete (HTMLParagraphElement.prototype as { clientHeight?: number }).clientHeight
    }
  })

  it('stays away with no review to answer, for a run not the viewer’s own, and while a verb is pending', () => {
    seed({ review: '' })
    const { rerender } = render(<Prompt />)
    expect(banner()).toBeNull()
    seed({ member_id: bob.id })
    rerender(<Prompt />)
    expect(banner()).toBeNull()
    seed()
    useStore.setState({ predicting: { run_1: {} } })
    rerender(<Prompt />)
    expect(banner()).toBeNull()
  })

  it('asks about a completed run without offering Not yet', () => {
    seed({ status: 'completed', state: '', finished_at: '2026-08-14T10:20:00Z' })
    render(<Prompt />)
    expect(screen.getByRole('button', { name: 'Mark complete' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Not yet' })).toBeNull()
  })

  it('withdraws the review on Not yet, then opens Chat with the composer focused', async () => {
    seed()
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1', view: 'changes' } } })
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Not yet' }))
    expect(banner()).toBeNull()
    expect(useStore.getState().runs.run_1.review).toBe('')
    await waitFor(() => expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1', view: 'session', focus: 'composer' } }))
    expect(api.runReview).toHaveBeenCalledWith('run_1', 'not-yet')
  })

  it('leaves the view alone when the answer to Not yet arrives after the identity changed', async () => {
    seed()
    const route = { name: 'run', params: { runId: 'run_1', view: 'changes' } }
    useStore.setState({ route, identityKey: 'alice' })
    let answer!: (run: Run) => void
    vi.mocked(api.runReview).mockImplementationOnce(() => new Promise((resolve) => { answer = resolve }))
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Not yet' }))
    await waitFor(() => expect(api.runReview).toHaveBeenCalledWith('run_1', 'not-yet'))
    useStore.setState({ identityKey: 'bob' })
    answer(reported({ review: '' }))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(useStore.getState().route).toEqual(route)
  })

  it('leaves the view alone when Not yet is refused', async () => {
    seed()
    const route = { name: 'run', params: { runId: 'run_1', view: 'changes' } }
    useStore.setState({ route })
    vi.mocked(api.runReview).mockRejectedValueOnce(new ApiError(400, 'session run_1 is completed', -32002))
    vi.mocked(api.runGet).mockResolvedValueOnce(reported())
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Not yet' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringContaining('session run_1 is completed')))
    expect(useStore.getState().route).toEqual(route)
  })

  it('folds to one line on Review later, and Review now asks again', async () => {
    seed()
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Review later' }))
    expect(useStore.getState().runs.run_1.review).toBe('later')
    await waitFor(() => expect(api.runReview).toHaveBeenCalledWith('run_1', 'later'))
    expect(banner()?.textContent).toBe('Finished, kept for later ·Review now')
    expect(screen.queryByRole('button', { name: 'Mark complete' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Review now' }))
    expect(banner()?.textContent).toContain('Mark complete?')
    fireEvent.click(screen.getByRole('button', { name: 'Review later' }))
    expect(banner()?.textContent).toBe('Finished, kept for later ·Review now')
    expect(api.runReview).toHaveBeenCalledOnce()
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
    vi.mocked(api.runReview).mockRejectedValueOnce(new ApiError(400, 'session is protected', -32602))
    vi.mocked(api.runGet).mockResolvedValueOnce(reported())
    render(<Prompt />)
    fireEvent.click(screen.getByRole('button', { name: 'Mark complete' }))
    expect(banner()).toBeNull()
    await waitFor(() => expect(banner()).not.toBeNull())
    expect(useStore.getState().runs.run_1).toMatchObject({ review: 'ready', status: 'needs-attention' })
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining('session is protected'))
  })
})

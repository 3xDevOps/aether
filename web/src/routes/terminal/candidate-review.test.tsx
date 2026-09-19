import { act, fireEvent, render, screen } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { Candidate, CandidateSummary } from '@/lib/integration-types'
import { CandidateReview } from '@/routes/terminal/candidate-review'
import { useStore } from '@/store'
import { fakeApi, integrationCandidate, workspace } from '@/test/fixtures'

type CandidateOverrides = Partial<Candidate>

function candidate(overrides: CandidateOverrides = {}): Candidate {
  return {
    ...integrationCandidate,
    candidate_id: 'candidate_review_test',
    state: 'conflicted',
    conflicts: ['left.txt', 'right.txt'],
    candidate_revision: 'candidate-revision-1',
    version: 1,
    applied_inputs: 0,
    ...overrides,
  }
}

function summary(value: Candidate): CandidateSummary {
  return {
    candidate_id: value.candidate_id,
    workspace_id: value.workspace_id,
    state: value.state,
    candidate_revision: value.candidate_revision,
    target_ref: value.target_ref,
    expected_target_revision: value.expected_target_revision,
    delivery_request: value.delivery_request,
    delivery_receipt: value.delivery_receipt,
    created_at: value.created_at,
    expires_at: value.expires_at,
  }
}

async function openCandidateReview(client: Api): Promise<void> {
  render(<CandidateReview workspaceID={workspace.id} currentRunID="run_1" client={client} />)
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Candidate review' }))
  })
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Show full' }))
  })
  expect(screen.getByRole('heading', { name: 'Candidate details' })).toBeDefined()
}

async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
  })
}

beforeEach(() => {
  useStore.setState({ connection: 'live' })
  Object.defineProperty(window.navigator, 'onLine', { configurable: true, value: true })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('candidate review authority and resolution drafts', () => {
  it('keeps the fresher polled result and restores controls after an older refresh', async () => {
    vi.useFakeTimers()
    const first = candidate({
      state: 'frozen',
      applied_inputs: integrationCandidate.inputs.length,
      conflicts: [],
      verifications: [{
        verification_id: 'verification-1',
        candidate_revision: 'candidate-revision-1',
        argv: [],
        image: 'image:fixture',
        status: 'running',
        created_at: '2026-08-14T10:00:00Z',
        expires_at: '2026-08-15T10:00:00Z',
      }],
    })
    const fresher = {
      ...first,
      version: 2,
      verifications: [{ ...first.verifications[0]!, status: 'passed' as const, output: 'combined checks passed' }],
    }
    let completeRefresh!: (result: { candidate: Candidate }) => void
    const refresh = new Promise<{ candidate: Candidate }>((resolve) => { completeRefresh = resolve })
    const client = fakeApi({
      integrationList: vi.fn(async () => ({ candidates: [summary(first)] })),
      integrationShow: vi.fn()
        .mockResolvedValueOnce({ candidate: first })
        .mockReturnValueOnce(refresh)
        .mockResolvedValue({ candidate: fresher }),
    })

    await openCandidateReview(client)
    fireEvent(window, new Event('offline'))
    await settle()
    fireEvent(window, new Event('online'))
    await settle()
    const verify = screen.getByRole('button', { name: 'Run verification' }) as HTMLButtonElement
    expect(verify.disabled).toBe(true)

    await act(async () => { await vi.advanceTimersByTimeAsync(2_000) })
    expect(screen.getByText('combined checks passed')).toBeDefined()
    await act(async () => { completeRefresh({ candidate: first }) })

    expect(screen.getByText('combined checks passed')).toBeDefined()
    expect(verify.disabled).toBe(false)
  })

  it('clears resolution drafts when a reconnect accepts a newer candidate version', async () => {
    const first = candidate({ candidate_revision: 'candidate-revision-1', version: 1 })
    const changed = candidate({ candidate_revision: 'candidate-revision-2', version: 2 })
    const showAnswers = [first, first, changed]
    const client = fakeApi({
      integrationList: vi.fn(async () => ({ candidates: [summary(first)] })),
      integrationShow: vi.fn(async () => ({ candidate: showAnswers.shift() ?? changed })),
    })

    await openCandidateReview(client)
    const draft = screen.getByLabelText('Resolution for right.txt')
    fireEvent.change(draft, { target: { value: 'stale draft' } })
    expect((draft as HTMLTextAreaElement).value).toBe('stale draft')

    fireEvent(window, new Event('online'))
    await settle()
    expect((draft as HTMLTextAreaElement).value).toBe('stale draft')

    fireEvent(window, new Event('online'))
    await settle()
    expect(screen.getByText('candidate-revision-2')).toBeDefined()
    expect((screen.getByLabelText('Resolution for right.txt') as HTMLTextAreaElement).value).toBe('')
  })

  it('preserves untouched drafts for a same-step partial response', async () => {
    const first = candidate({ version: 1 })
    const partial = candidate({ version: 2, conflicts: ['right.txt'], applied_inputs: 0 })
    const client = fakeApi({
      integrationList: vi.fn(async () => ({ candidates: [summary(first)] })),
      integrationShow: vi.fn(async () => ({ candidate: first })),
      integrationResolve: vi.fn(async () => ({ candidate: partial })),
    })

    await openCandidateReview(client)
    fireEvent.change(screen.getByLabelText('Resolution for left.txt'), { target: { value: 'left content' } })
    fireEvent.change(screen.getByLabelText('Resolution for right.txt'), { target: { value: 'keep this draft' } })
    fireEvent.click(screen.getByLabelText('Apply resolution for right.txt'))
    fireEvent.click(screen.getByRole('button', { name: 'Apply resolutions' }))
    await settle()

    expect(screen.queryByLabelText('Resolution for left.txt')).toBeNull()
    expect((screen.getByLabelText('Resolution for right.txt') as HTMLTextAreaElement).value).toBe('keep this draft')
  })

  it('clears every draft when assembly advances even if a conflict path recurs', async () => {
    const first = candidate({ version: 1, conflicts: ['left.txt'] })
    const advanced = candidate({ version: 2, conflicts: ['left.txt'], applied_inputs: 1 })
    const client = fakeApi({
      integrationList: vi.fn(async () => ({ candidates: [summary(first)] })),
      integrationShow: vi.fn(async () => ({ candidate: first })),
      integrationResolve: vi.fn(async () => ({ candidate: advanced })),
    })

    await openCandidateReview(client)
    fireEvent.change(screen.getByLabelText('Resolution for left.txt'), { target: { value: 'old left draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply resolutions' }))
    await settle()

    expect((screen.getByLabelText('Resolution for left.txt') as HTMLTextAreaElement).value).toBe('')
  })
})

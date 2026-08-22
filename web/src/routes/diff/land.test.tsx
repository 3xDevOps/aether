import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { LandControls } from '@/routes/diff/land'
import { useStore } from '@/store'
import { initialDiff } from '@/store/diff'
import { toRecord, type RunRecord } from '@/store/runs'
import { alice, run, session } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

const active = run({ id: 'run_1' })

function seed(over: { local?: string[]; status?: 'running' | 'needs-attention' } = {}) {
  const record = toRecord(run({ id: active.id, status: over.status ?? 'running' }))
  useStore.setState({
    sessions: { [session.id]: session },
    members: { [alice.id]: alice },
    runs: { [active.id]: record },
    diffs: { [active.id]: { ...initialDiff, base: 'abcdef1234567890' } },
    capabilities: over.local
      ? { gateway: 'local', methods: ['*'], ws: [], local: over.local }
      : { gateway: 'remote', methods: ['*'], ws: [] },
    hydrated: true,
  })
  return record
}

function renderControls(record: RunRecord) {
  return render(<LandControls run={record} />)
}

beforeEach(() => {
  vi.clearAllMocks()
})

// Pull is a local-gateway verb: a remote monitor never shows the button, so
// nobody clicks a verb the server would refuse anyway.
test('pull is hidden without the local pull capability', () => {
  renderControls(seed())
  expect(screen.queryByRole('button', { name: 'Pull branch' })).toBeNull()
})

test('pull fetches the branch and shows the git output', async () => {
  vi.mocked(api.localPull).mockResolvedValue({
    branch: active.branch,
    ref: `refs/heads/${active.branch}`,
    output: 'From ssh://host\n * [new branch] run-1-checkout',
  })
  renderControls(seed({ local: ['pull'] }))

  fireEvent.click(screen.getByRole('button', { name: 'Pull branch' }))

  expect(api.localPull).toHaveBeenCalledWith(active.id)
  expect(await screen.findByText(`fetched refs/heads/${active.branch}`)).toBeTruthy()
  expect(screen.getByText(/new branch/)).toBeTruthy()
  expect(toast.success).toHaveBeenCalledWith(`fetched refs/heads/${active.branch}`)
})

// The server's refusal is the whole message; nothing rewrites it.
test('a rejected pull surfaces the server message verbatim', async () => {
  vi.mocked(api.localPull).mockRejectedValue(
    new Error('link.repo has not run: no repository is linked'),
  )
  renderControls(seed({ local: ['pull'] }))

  fireEvent.click(screen.getByRole('button', { name: 'Pull branch' }))

  await waitFor(() =>
    expect(toast.error).toHaveBeenCalledWith(
      'link.repo has not run: no repository is linked',
    ),
  )
  expect(screen.queryByText(/fetched/)).toBeNull()
})

test('close as merged asks first, then calls run.close with the outcome', async () => {
  renderControls(seed({ status: 'needs-attention' }))

  fireEvent.click(screen.getByRole('button', { name: 'Close as merged' }))
  expect(await screen.findByText('Close as merged?')).toBeTruthy()
  expect(api.runClose).not.toHaveBeenCalled()

  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Close as merged' }))
  await waitFor(() => expect(api.runClose).toHaveBeenCalledWith(active.id, 'merged'))

  // The store is untouched: the run.status event, not this click, moves it.
  expect(useStore.getState().runs[active.id]?.status).toBe('needs-attention')
})

test('close buttons only appear for needs-attention runs', () => {
  renderControls(seed({ status: 'running' }))
  expect(screen.queryByRole('button', { name: 'Close as merged' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Close as abandoned' })).toBeNull()
})

test('the review commands name the fetched branch and the base', () => {
  renderControls(seed())
  expect(
    screen.getByText(`git log --oneline aether/${active.branch}`),
  ).toBeTruthy()
  expect(
    screen.getByText(`git diff abcdef12...aether/${active.branch}`),
  ).toBeTruthy()
})

test('copy writes the command to the clipboard when the API exists', async () => {
  const writeText = vi.fn(async () => {})
  vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
  renderControls(seed())

  const command = `git log --oneline aether/${active.branch}`
  fireEvent.click(screen.getByRole('button', { name: `Copy ${command}` }))

  await waitFor(() => expect(writeText).toHaveBeenCalledWith(command))
  expect(toast.success).toHaveBeenCalledWith('Copied')
  vi.unstubAllGlobals()
})

test('copy falls back to selecting the text where the clipboard is missing', async () => {
  // jsdom ships no navigator.clipboard - exactly the environment (plain-http
  // origins, older engines) the fallback exists for. The click must not
  // throw; it selects the command text instead.
  renderControls(seed())

  const command = `git log --oneline aether/${active.branch}`
  fireEvent.click(screen.getByRole('button', { name: `Copy ${command}` }))

  await waitFor(() => {
    const selection = window.getSelection()
    expect(selection?.rangeCount).toBe(1)
    expect(selection?.getRangeAt(0).toString()).toBe(command)
  })
})

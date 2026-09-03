import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { ReviewCommands } from '@/routes/diff/review-commands'
import { useStore } from '@/store'
import { initialDiff } from '@/store/diff'
import { toRecord, type RunRecord } from '@/store/runs'
import { alice, run, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

const active = run({ id: 'run_1' })

function seed() {
  const record = toRecord(run({ id: active.id }))
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    runs: { [active.id]: record },
    diffs: { [active.id]: { ...initialDiff, base: 'abcdef1234567890' } },
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    hydrated: true,
  })
  return record
}

function renderCommands(record: RunRecord) {
  return render(<ReviewCommands run={record} />)
}

beforeEach(() => {
  vi.clearAllMocks()
})

test('the review commands name the fetched branch and the base', () => {
  renderCommands(seed())
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
  renderCommands(seed())

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
  renderCommands(seed())

  const command = `git log --oneline aether/${active.branch}`
  fireEvent.click(screen.getByRole('button', { name: `Copy ${command}` }))

  await waitFor(() => {
    const selection = window.getSelection()
    expect(selection?.rangeCount).toBe(1)
    expect(selection?.getRangeAt(0).toString()).toBe(command)
  })
})

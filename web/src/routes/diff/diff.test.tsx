import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { parsePatch } from '@/routes/diff/parse'
import '@/routes/diff'
import { lookupRoute } from '@/routes/registry'
import { useStore } from '@/store'
import { initialDiff, type RunDiffState } from '@/store/diff'
import { toRecord } from '@/store/runs'
import type { RunPatch } from '@/lib/types'
import { alice, bob, run, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const patch = [
  'diff --git a/cmd/main.go b/cmd/main.go',
  'index 111..222 100644',
  '--- a/cmd/main.go',
  '+++ b/cmd/main.go',
  '@@ -1,3 +1,3 @@',
  ' package main',
  '-old line',
  '+new line',
  'diff --git a/db/schema.sql b/db/schema.sql',
  'deleted file mode 100644',
  '--- a/db/schema.sql',
  '+++ /dev/null',
  '@@ -1,2 +0,0 @@',
  '--- a comment git did not write',
  '-CREATE TABLE t (id INT);',
  'diff --git a/notes.md b/notes.md',
  'new file mode 100644',
  '--- /dev/null',
  '+++ b/notes.md',
  '@@ -0,0 +1 @@',
  '+hello',
  '',
].join('\n')

const newFile = [
  'diff --git a/newer.txt b/newer.txt',
  'new file mode 100644',
  '--- /dev/null',
  '+++ b/newer.txt',
  '@@ -0,0 +1 @@',
  '+later',
  '',
].join('\n')

const active = run({ id: 'run_1' })
const peerRun = run({ id: 'run_2', member_id: bob.id, task: 'the other run' })

function seed(diff?: Partial<RunDiffState>) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: { [active.id]: toRecord(active), [peerRun.id]: toRecord(peerRun) },
    diffs: diff ? { [active.id]: { ...initialDiff, ...diff } } : {},
    overlaps: {},
    route: { name: 'diff', params: { runId: active.id } },
    hydrated: true,
  })
}

const DiffView = lookupRoute('diff')!

function renderDiff() {
  return render(<DiffView params={{ runId: active.id }} />)
}

beforeEach(() => {
  vi.clearAllMocks()
})

// The parser is the whole of the client's diff support, so the shapes it has
// to survive are worth pinning: a deletion, a new file, and a removed line
// that looks exactly like a file marker.
test('parses a unified diff into files, kinds and counts', () => {
  const files = parsePatch(patch)

  expect(files.map((f) => f.path)).toEqual(['cmd/main.go', 'db/schema.sql', 'notes.md'])
  expect(files.map((f) => f.status)).toEqual(['modified', 'deleted', 'added'])
  expect(files[0]).toMatchObject({ additions: 1, deletions: 1 })
  expect(files[0].lines).toContainEqual({ kind: 'context', text: 'package main' })
  // "--- a comment..." is a removed SQL comment inside a hunk, not a header.
  expect(files[1].deletions).toBe(2)
  expect(files[1].lines).toContainEqual({ kind: 'del', text: '-- a comment git did not write' })
})

test('renders the fetched patch and says when it was cut short', async () => {
  seed()
  vi.mocked(api.runPatch).mockResolvedValue({
    run_id: active.id,
    base: 'abcdef1234567890',
    patch,
    truncated: true,
  })
  renderDiff()

  expect(await screen.findByText('cmd/main.go')).toBeTruthy()
  expect(screen.getByText('notes.md')).toBeTruthy()
  expect(screen.getByText('+new line')).toBeTruthy()
  expect(screen.getByText('abcdef12')).toBeTruthy()
  expect(screen.getByText(/too large to render in full/)).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledWith(active.id)
})

// The event stream carries no patch text: a snapshot is a timeline entry and
// the signal to fetch the patch again.
test('a diff snapshot refetches the patch and joins the timeline', async () => {
  seed({ status: 'ready', base: 'abcdef12', patch, revision: 0, fetched: 0 })
  renderDiff()
  expect(api.runPatch).not.toHaveBeenCalled()

  vi.mocked(api.runPatch).mockResolvedValue({
    run_id: active.id,
    base: 'abcdef12',
    patch,
    truncated: false,
  })
  useStore.getState().noteDiffSnapshot(active.id, {
    time: new Date().toISOString(),
    files: [{ path: 'notes.md', additions: 1, deletions: 0 }],
  })

  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))
  const timeline = screen.getByRole('complementary')
  fireEvent.click(within(timeline).getByRole('button'))

  // Selecting a snapshot narrows the patch to the files it touched.
  expect(screen.getByText('notes.md')).toBeTruthy()
  expect(screen.queryByText('cmd/main.go')).toBeNull()
})

// A slow request must not swallow the snapshot that lands while it is in
// flight: the answer is for the revision it was issued at, and anything newer
// asks again. Otherwise the tab shows a diff missing the newest changes while
// reporting itself fresh, and only Refresh recovers it.
test('a snapshot arriving mid-fetch is answered by a second fetch', async () => {
  seed({ status: 'ready', base: 'abcdef12', patch, revision: 0, fetched: 0 })
  let land: (p: RunPatch) => void = () => {}
  const inFlight = new Promise<RunPatch>((resolve) => {
    land = resolve
  })
  const answer = (text: string): RunPatch => ({
    run_id: active.id,
    base: 'abcdef12',
    patch: text,
    truncated: false,
  })
  vi.mocked(api.runPatch)
    .mockReturnValueOnce(inFlight)
    .mockResolvedValue(answer(patch + newFile))
  renderDiff()

  act(() =>
    useStore.getState().noteDiffSnapshot(active.id, {
      time: '2026-08-14T10:03:00Z',
      files: [{ path: 'cmd/main.go', additions: 1, deletions: 1 }],
    }),
  )
  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))

  // Mid-flight: the second snapshot neither cancels the request nor starts
  // another one of its own.
  act(() =>
    useStore.getState().noteDiffSnapshot(active.id, {
      time: '2026-08-14T10:04:00Z',
      files: [{ path: 'newer.txt', additions: 1, deletions: 0 }],
    }),
  )
  expect(api.runPatch).toHaveBeenCalledTimes(1)

  await act(async () => {
    land(answer(patch))
    await inFlight
  })

  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(2))
  expect(await screen.findByText('newer.txt')).toBeTruthy()
  expect(useStore.getState().diffs[active.id].fetched).toBe(2)
})

test('a conflict chip names the file and the member and opens their run', () => {
  seed({ status: 'ready', patch })
  useStore.setState({
    overlaps: {
      [active.id]: [{ run_id: peerRun.id, member_id: bob.id, files: ['cmd/main.go', 'go.mod'] }],
    },
  })
  render(<ConflictChips run={useStore.getState().runs[active.id]} />)

  const chip = screen.getByRole('button', { name: /2 overlapping files with Bob/ })
  expect(chip.textContent).toContain('main.go')
  expect(chip.textContent).toContain('Bob')

  fireEvent.click(chip)
  expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: peerRun.id } })
})

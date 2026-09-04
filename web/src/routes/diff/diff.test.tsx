import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { parsePatch } from '@/routes/diff/parse'
import '@/routes/diff'
import { lookupRoute } from '@/routes/registry'
import { useStore } from '@/store'
import { initialDiff, type DiffSnapshot, type RunDiffState } from '@/store/diff'
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

/** A timeline entry the server recorded trees for. `snapshots` is newest
 * first, the order `noteDiffSnapshot` builds. */
function snapshot(time: string, parentTree: string, tree: string): DiffSnapshot {
  return {
    time,
    files: [{ path: 'notes.md', additions: 1, deletions: 1 }],
    tree,
    parentTree,
  }
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
  act(() =>
    useStore.getState().noteDiffSnapshot(active.id, {
      time: new Date().toISOString(),
      files: [{ path: 'notes.md', additions: 1, deletions: 0 }],
      tree: 'tree1',
      parentTree: 'tree0',
    }),
  )

  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))
  expect(api.runPatch).toHaveBeenCalledWith(active.id)
  const timeline = screen.getByRole('complementary')
  expect(within(timeline).getByRole('button').textContent).toContain('1 file')
})

// The point of a per-snapshot tree: the tab asks for that one interval, not a
// filter over the current diff.
test('selecting a snapshot fetches its interval and shows only that change', async () => {
  seed({
    status: 'ready',
    base: 'abcdef12',
    patch,
    revision: 0,
    fetched: 0,
    snapshots: [snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1')],
  })
  vi.mocked(api.runPatch).mockResolvedValue({
    run_id: active.id,
    base: 'tree1',
    patch: newFile,
    truncated: false,
  })
  renderDiff()

  fireEvent.click(within(screen.getByRole('complementary')).getByRole('button'))

  expect(await screen.findByText('newer.txt')).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledWith(active.id, { from: 'tree0', to: 'tree1' })
  // Only the interval's file, and the header no longer claims a fork-point
  // diff. The fork point the run's base names is untouched.
  expect(screen.queryByText('cmd/main.go')).toBeNull()
  expect(screen.getByText(/What changed/)).toBeTruthy()
  expect(useStore.getState().diffs[active.id].base).toBe('abcdef12')
})

// A file edited twice shows its second change alone, which the old filter
// over the cumulative diff could not do.
test('a second snapshot of the same file shows only the second change', async () => {
  const first = [
    'diff --git a/notes.md b/notes.md',
    '--- a/notes.md',
    '+++ b/notes.md',
    '@@ -1 +1 @@',
    '-hello',
    '+first edit',
    '',
  ].join('\n')
  const second = [
    'diff --git a/notes.md b/notes.md',
    '--- a/notes.md',
    '+++ b/notes.md',
    '@@ -1 +1 @@',
    '-first edit',
    '+second edit',
    '',
  ].join('\n')
  seed({
    status: 'ready',
    base: 'abcdef12',
    patch,
    revision: 0,
    fetched: 0,
    snapshots: [
      snapshot('2026-08-14T10:04:00Z', 'tree1', 'tree2'),
      snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1'),
    ],
  })
  vi.mocked(api.runPatch).mockImplementation(async (_runID, range) => ({
    run_id: active.id,
    base: range?.from ?? 'abcdef12',
    patch: range?.to === 'tree2' ? second : range ? first : patch,
    truncated: false,
  }))
  renderDiff()

  // Newest first: the second snapshot is the first row.
  const rows = within(screen.getByRole('complementary')).getAllByRole('button')
  fireEvent.click(rows[0])

  expect(await screen.findByText('+second edit')).toBeTruthy()
  expect(screen.queryByText('+first edit')).toBeNull()

  fireEvent.click(rows[1])
  expect(await screen.findByText('+first edit')).toBeTruthy()
  expect(screen.queryByText('+second edit')).toBeNull()
})

// An interval patch answers for two tree ids and the cumulative patch is
// already in the store, so going back to it asks the server for nothing.
test('deselecting returns to the cumulative patch without refetching', async () => {
  seed({
    status: 'ready',
    base: 'abcdef12',
    patch,
    revision: 0,
    fetched: 0,
    snapshots: [snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1')],
  })
  vi.mocked(api.runPatch).mockResolvedValue({
    run_id: active.id,
    base: 'tree1',
    patch: newFile,
    truncated: false,
  })
  renderDiff()

  const row = within(screen.getByRole('complementary')).getByRole('button')
  fireEvent.click(row)
  expect(await screen.findByText('newer.txt')).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledTimes(1)

  fireEvent.click(row)
  expect(await screen.findByText('cmd/main.go')).toBeTruthy()
  expect(screen.getByText('abcdef12')).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledTimes(1)
})

// The server says why an interval cannot be shown - a tree it no longer has
// on disk, say. That message is what the tab shows.
test('an interval that fails shows the server message', async () => {
  seed({
    status: 'ready',
    base: 'abcdef12',
    patch,
    revision: 0,
    fetched: 0,
    snapshots: [snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1')],
  })
  vi.mocked(api.runPatch).mockRejectedValue(
    new Error("run.patch: that snapshot's tree is no longer on disk"),
  )
  renderDiff()

  fireEvent.click(within(screen.getByRole('complementary')).getByRole('button'))

  expect(
    await screen.findByText("run.patch: that snapshot's tree is no longer on disk"),
  ).toBeTruthy()
})

// A server that predates per-snapshot trees sends no tree, and there is no
// honest way to show that interval - so the row says so instead of falling
// back to a filter.
test('a snapshot without a tree is not selectable', async () => {
  seed({
    status: 'ready',
    base: 'abcdef12',
    patch,
    revision: 0,
    fetched: 0,
    snapshots: [{ time: '2026-08-14T10:03:00Z', files: [] }],
  })
  renderDiff()

  const row = within(screen.getByRole('complementary')).getByRole('button')
  expect(row.hasAttribute('disabled')).toBe(true)
  expect(row.getAttribute('title')).toContain('did not record a tree')

  fireEvent.click(row)
  expect(screen.getByText('cmd/main.go')).toBeTruthy()
  expect(api.runPatch).not.toHaveBeenCalled()
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

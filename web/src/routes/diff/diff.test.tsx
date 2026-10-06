import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { parsePatch } from '@/routes/diff/parse'
import { ChangesView } from '@/routes/diff'
import { useStore } from '@/store'
import { initialDiff, intervalKey, type DiffSnapshot, type RunDiffState } from '@/store/diff'
import { toRecord } from '@/store/runs'
import type { RunPatch } from '@/lib/types'
import { alice, bob, run, workspace } from '@/test/fixtures'
import { hintOn } from '@/test/tooltip'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const patch = [
  'diff --git a/cmd/main.go b/cmd/main.go',
  'index 111..222 100644',
  '--- a/cmd/main.go',
  '+++ b/cmd/main.go',
  '@@ -10,3 +10,3 @@',
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

function added(path: string, lines: number): string {
  return [
    `diff --git a/${path} b/${path}`,
    'new file mode 100644',
    '--- /dev/null',
    `+++ b/${path}`,
    `@@ -0,0 +1,${lines} @@`,
    ...Array.from({ length: lines }, (_, i) => `+row ${i + 1}`),
    '',
  ].join('\n')
}

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
    diffWrap: null,
    route: { name: 'run', params: { runId: active.id, view: 'changes' } },
    hydrated: true,
  })
}

const ready = { status: 'ready', base: 'abcdef1234567890', patch, revision: 0, fetched: 0 } as const

/** `snapshots` is newest first, the order `noteDiffSnapshot` builds. */
function snapshot(time: string, parentTree: string, tree: string): DiffSnapshot {
  return { time, files: [{ path: 'notes.md', additions: 1, deletions: 1 }], tree, parentTree }
}

function renderDiff() {
  return render(<ChangesView runID={active.id} />)
}

function openMenu(name: RegExp | string) {
  fireEvent.pointerDown(screen.getByRole('button', { name }), { button: 0, ctrlKey: false })
}

function chooseInterval(name: RegExp) {
  openMenu(/Current diff|What changed/)
  fireEvent.click(screen.getByRole('menuitemradio', { name }))
}

beforeEach(() => {
  vi.clearAllMocks()
})

test('parses a unified diff into files, kinds, counts and line numbers', () => {
  const files = parsePatch(patch)

  expect(files.map((f) => f.path)).toEqual(['cmd/main.go', 'db/schema.sql', 'notes.md'])
  expect(files.map((f) => f.status)).toEqual(['modified', 'deleted', 'added'])
  expect(files[0]).toMatchObject({ additions: 1, deletions: 1 })
  expect(files[0].lines).toEqual([
    { kind: 'hunk', text: '@@ -10,3 +10,3 @@' },
    { kind: 'context', text: 'package main', old: 10, new: 10 },
    { kind: 'del', text: 'old line', old: 11 },
    { kind: 'add', text: 'new line', new: 11 },
  ])
  // "--- a comment..." is a removed SQL comment inside a hunk, not a header.
  expect(files[1].deletions).toBe(2)
  expect(files[1].lines).toContainEqual({ kind: 'del', text: '-- a comment git did not write', old: 1 })
})

test('the strip sums the files and the patch shows line numbers', async () => {
  seed()
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'abcdef1234567890', patch, truncated: false })
  renderDiff()

  const strip = await screen.findByRole('toolbar', { name: 'Changes' })
  await waitFor(() => expect(strip.textContent).toContain('3 files'))
  expect(strip.textContent).toContain('+2')
  expect(strip.textContent).toContain('−3')
  const main = screen.getByRole('region', { name: 'cmd/main.go' })
  expect(within(main).getByText('+new line')).toBeTruthy()
  const context = within(main).getByText((_, el) => el?.tagName === 'CODE' && el.textContent === ' package main').parentElement!
  expect(context.textContent).toBe('1010 package main')
  expect(api.runPatch).toHaveBeenCalledWith(active.id)
})

test('deleted, binary and large files start collapsed; the chevron opens them', () => {
  const binary = 'diff --git a/logo.png b/logo.png\nnew file mode 100644\nBinary files /dev/null and b/logo.png differ\n'
  seed({ ...ready, patch: patch + binary + added('big.txt', 501) + added('small.txt', 499) })
  renderDiff()

  expect(screen.getByRole('button', { name: 'Collapse cmd/main.go' }).getAttribute('aria-expanded')).toBe('true')
  expect(screen.getByRole('button', { name: 'Expand db/schema.sql' }).getAttribute('aria-expanded')).toBe('false')
  expect(screen.getByRole('button', { name: 'Expand logo.png' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Collapse small.txt' })).toBeTruthy()
  const big = screen.getByRole('region', { name: 'big.txt' })
  expect(big.textContent).toContain('501 lines')
  expect(within(screen.getByRole('region', { name: 'db/schema.sql' })).queryByText('-CREATE TABLE t (id INT);')).toBeNull()

  fireEvent.click(screen.getByRole('button', { name: 'Expand db/schema.sql' }))
  expect(screen.getByText('-CREATE TABLE t (id INT);')).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: 'Collapse cmd/main.go' }))
  expect(screen.queryByText('+new line')).toBeNull()
})

test('expand all and collapse all come from the strip', () => {
  seed(ready)
  renderDiff()

  openMenu('More')
  fireEvent.click(screen.getByRole('menuitem', { name: 'Expand all files' }))
  expect(screen.getByText('-CREATE TABLE t (id INT);')).toBeTruthy()

  openMenu('More')
  fireEvent.click(screen.getByRole('menuitem', { name: 'Collapse all files' }))
  expect(screen.queryByText('+new line')).toBeNull()
  expect(screen.queryByText('+hello')).toBeNull()
})

// jsdom lays nothing out, so the virtual list mounts only the rows it can
// measure: far fewer than the file has.
test('a file over 500 lines renders its hunks virtually once opened', () => {
  seed({ ...ready, patch: added('big.txt', 600) + added('small.txt', 400) })
  renderDiff()

  fireEvent.click(screen.getByRole('button', { name: 'Expand big.txt' }))
  const big = screen.getByRole('region', { name: 'big.txt' })
  expect(within(big).queryAllByText(/^\+row /).length).toBeLessThan(600)
  const small = screen.getByRole('region', { name: 'small.txt' })
  expect(within(small).getAllByText(/^\+row /)).toHaveLength(400)
})

test('the 500-line rule counts content lines, not hunk headers', () => {
  const hunks = Array.from({ length: 10 }, (_, h) => [`@@ -${h * 100 + 1},0 +${h * 100 + 1},${h < 9 ? 50 : 45} @@`, ...Array.from({ length: h < 9 ? 50 : 45 }, (_, i) => `+row ${i}`)])
  seed({ ...ready, patch: ['diff --git a/hunks.txt b/hunks.txt', '--- a/hunks.txt', '+++ b/hunks.txt', ...hunks.flat(), ''].join('\n') })
  renderDiff()
  expect(screen.getByRole('button', { name: 'Collapse hunks.txt' })).toBeTruthy()
})

test('a patch of many medium files mounts only the files near the screen', () => {
  seed({ ...ready, patch: Array.from({ length: 10 }, (_, i) => added(`part${i}.txt`, 400)).join('') })
  renderDiff()
  expect(screen.getByRole('toolbar', { name: 'Changes' }).textContent).toContain('10 files')
  expect(screen.queryAllByText(/^\+row /).length).toBeLessThan(1500)
})

test('a file path opens that file in Files on the run checkout', () => {
  seed(ready)
  useStore.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: [] } })
  renderDiff()

  fireEvent.click(screen.getByRole('button', { name: 'notes.md' }))

  const { route, fileTabs, activeFileKey } = useStore.getState()
  expect(route.name).toBe('files')
  expect(fileTabs.at(-1)).toMatchObject({ kind: 'workspace', workspaceID: active.workspace_id, runID: active.id, path: 'notes.md' })
  expect(activeFileKey).toBe(fileTabs.at(-1)!.key)
  expect(screen.queryByRole('button', { name: 'db/schema.sql' })).toBeNull()
})

test('a binary file has no Open in Files link', () => {
  seed({ ...ready, patch: 'diff --git a/logo.png b/logo.png\nnew file mode 100644\nBinary files /dev/null and b/logo.png differ\n' })
  useStore.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: [] } })
  renderDiff()
  expect(screen.getByRole('region', { name: 'logo.png' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'logo.png' })).toBeNull()
})

test('nothing changed is an empty state, not a blank pane', async () => {
  seed({ ...ready, patch: '' })
  renderDiff()
  expect(screen.getByRole('heading', { name: 'No changes yet.' })).toBeTruthy()
})

test('a diff snapshot refetches the patch and joins the interval menu', async () => {
  seed(ready)
  renderDiff()
  expect(api.runPatch).not.toHaveBeenCalled()

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'abcdef12', patch, truncated: false })
  act(() =>
    useStore.getState().noteDiffSnapshot(active.id, {
      time: new Date().toISOString(),
      files: [{ path: 'notes.md', additions: 1, deletions: 0 }],
      tree: 'tree1',
      parentTree: 'tree0',
    }),
  )

  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))
  openMenu('Current diff')
  expect(screen.getByRole('menuitemradio', { name: /What changed/ }).textContent).toContain('1 file')
  expect(screen.getByRole('menuitemradio', { name: /Current diff/ }).textContent).toContain('Against abcdef12')
})

test('selecting an interval fetches that change alone; Current diff goes back without refetching', async () => {
  seed({ ...ready, snapshots: [snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1')] })
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch: newFile, truncated: false })
  renderDiff()

  chooseInterval(/What changed/)

  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledWith(active.id, { from: 'tree0', to: 'tree1' })
  expect(screen.queryByRole('region', { name: 'cmd/main.go' })).toBeNull()
  expect(screen.getByRole('button', { name: /What changed/ })).toBeTruthy()
  expect(useStore.getState().diffs[active.id].base).toBe('abcdef1234567890')

  chooseInterval(/Current diff/)
  expect(await screen.findByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledTimes(1)
})

test('a second snapshot of the same file shows only the second change', async () => {
  const edit = (from: string, to: string) =>
    ['diff --git a/notes.md b/notes.md', '--- a/notes.md', '+++ b/notes.md', '@@ -1 +1 @@', `-${from}`, `+${to}`, ''].join('\n')
  seed({
    ...ready,
    snapshots: [snapshot('2026-08-14T10:04:00Z', 'tree1', 'tree2'), snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1')],
  })
  vi.mocked(api.runPatch).mockImplementation(async (_runID, range) => ({
    run_id: active.id,
    base: range?.from ?? 'abcdef12',
    patch: range?.to === 'tree2' ? edit('first edit', 'second edit') : edit('hello', 'first edit'),
    truncated: false,
  }))
  renderDiff()

  openMenu('Current diff')
  fireEvent.click(screen.getAllByRole('menuitemradio', { name: /What changed/ })[0])
  expect(await screen.findByText('+second edit')).toBeTruthy()
  expect(screen.queryByText('+first edit')).toBeNull()

  openMenu(/What changed/)
  fireEvent.click(screen.getAllByRole('menuitemradio', { name: /What changed/ })[1])
  expect(await screen.findByText('+first edit')).toBeTruthy()
  expect(screen.queryByText('+second edit')).toBeNull()
})

test('an interval that fails shows the server message and Refresh retries it', async () => {
  seed({ ...ready, snapshots: [snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1')] })
  vi.mocked(api.runPatch).mockRejectedValue(new Error("run.patch: that snapshot's tree is no longer on disk"))
  renderDiff()

  chooseInterval(/What changed/)
  expect(await screen.findByText("run.patch: that snapshot's tree is no longer on disk")).toBeTruthy()

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree0', patch: newFile, truncated: false })
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
})

test('a snapshot without a tree is a disabled row that says why', () => {
  seed({ ...ready, snapshots: [{ time: '2026-08-14T10:03:00Z', files: [] }] })
  renderDiff()

  openMenu('Current diff')
  const row = screen.getByRole('menuitemradio', { name: /What changed/ })
  expect(row.getAttribute('aria-disabled')).toBe('true')
  expect(row.textContent).toContain('did not record a tree')
  expect(api.runPatch).not.toHaveBeenCalled()
})

test('with no snapshots the interval menu says why it is empty', () => {
  seed(ready)
  renderDiff()
  openMenu('Current diff')
  expect(screen.getByText('No intervals since you opened the dashboard.')).toBeTruthy()
})

test('snapshots falling off the timeline take their cached intervals with them', () => {
  const snapshots = Array.from({ length: 40 }, (_, i) =>
    snapshot(`2026-08-14T10:${String(i).padStart(2, '0')}:00Z`, `tree${i}`, `tree${i + 1}`),
  )
  const oldest = snapshots[snapshots.length - 1]
  seed({
    status: 'ready',
    snapshots,
    intervals: { [intervalKey(oldest.parentTree!, oldest.tree!)]: { patch: newFile, truncated: false, status: 'ready' } },
  })

  act(() => useStore.getState().noteDiffSnapshot(active.id, snapshot('2026-08-14T11:00:00Z', 'treeX', 'treeY')))

  const state = useStore.getState().diffs[active.id]
  expect(state.snapshots).toHaveLength(40)
  expect(state.snapshots.some((s) => s.time === oldest.time)).toBe(false)
  expect(state.intervals).toEqual({})
})

// The answer is for the revision the request was issued at; anything newer
// asks again, or the view would show a stale diff as fresh.
test('a snapshot arriving mid-fetch is answered by a second fetch', async () => {
  seed(ready)
  let land: (p: RunPatch) => void = () => {}
  const inFlight = new Promise<RunPatch>((resolve) => {
    land = resolve
  })
  const answer = (text: string): RunPatch => ({ run_id: active.id, base: 'abcdef12', patch: text, truncated: false })
  vi.mocked(api.runPatch).mockReturnValueOnce(inFlight).mockResolvedValue(answer(patch + newFile))
  renderDiff()

  act(() =>
    useStore.getState().noteDiffSnapshot(active.id, {
      time: '2026-08-14T10:03:00Z',
      files: [{ path: 'cmd/main.go', additions: 1, deletions: 1 }],
    }),
  )
  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))
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
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(useStore.getState().diffs[active.id].fetched).toBe(2)
})

test('a conflict chip names the file and the member and opens their run', async () => {
  seed(ready)
  useStore.setState({ overlaps: { [active.id]: [{ run_id: peerRun.id, member_id: bob.id, files: ['cmd/main.go', 'go.mod'] }] } })
  render(<ConflictChips run={useStore.getState().runs[active.id]} />)

  const chip = screen.getByRole('button', { name: /2 overlapping files with Bob/ })
  expect(chip.textContent).toContain('main.go')
  expect(await hintOn(chip)).toBe('cmd/main.go\ngo.mod\n\nalso being changed by Bob')

  fireEvent.click(chip)
  expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: peerRun.id } })
})

test('a conflict chip writes its file list out on a coarse pointer', () => {
  atViewport(390, { pointer: 'coarse' })
  seed(ready)
  useStore.setState({ overlaps: { [active.id]: [{ run_id: peerRun.id, member_id: bob.id, files: ['cmd/main.go', 'go.mod'] }] } })
  render(<ConflictChips run={useStore.getState().runs[active.id]} />)

  expect(screen.getByText('cmd/main.go go.mod - also being changed by Bob')).toBeTruthy()
})

it('sends an unknown run to the shared missing-run view', () => {
  seed()
  useStore.setState({ hydrationError: null, streamDead: false })
  render(<ChangesView runID="run_missing" />)
  expect(screen.getByRole('button', { name: 'Back to board' })).toBeDefined()
})

test('a coarse pointer wraps long lines until the toggle says otherwise, and the choice persists', async () => {
  atViewport(390, { pointer: 'coarse' })
  seed(ready)
  const first = renderDiff()

  const toggle = screen.getByRole('button', { name: 'Wrap lines' })
  expect(toggle.getAttribute('aria-pressed')).toBe('true')
  expect(screen.getByText('+new line').className).toContain('whitespace-pre-wrap')

  fireEvent.click(toggle)
  await waitFor(() => expect(toggle.getAttribute('aria-pressed')).toBe('false'))
  expect(screen.getByText('+new line').className).not.toContain('whitespace-pre-wrap')
  first.unmount()

  renderDiff()
  expect(screen.getByRole('button', { name: 'Wrap lines' }).getAttribute('aria-pressed')).toBe('false')
})

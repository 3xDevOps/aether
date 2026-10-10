import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { parsePatch } from '@/routes/diff/parse'
import { ChangesView } from '@/routes/diff'
import { patchReview } from '@/routes/diff/review'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { initialDiff, intervalKey, type DiffSnapshot, type RunDiffState } from '@/store/diff'
import { toRecord } from '@/store/runs'
import type { RunPatch } from '@/lib/types'
import { alice, bob, roomMessage, run, serverInfo, vera, workspace } from '@/test/fixtures'
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

/** The tab holds the run's control lease unless a test says otherwise. */
function agent(over: Partial<AgentTerminal> = {}): AgentTerminal {
  return {
    localControl: true,
    roomControl: { has_control: true, control_session_id: 'ctl', control_generation: 3 },
    steerable: true,
    controlUnavailable: false,
    ...over,
  } as AgentTerminal
}

function renderDiff(over?: Partial<AgentTerminal>) {
  return render(<ChangesView runID={active.id} agent={agent(over)} agentName="Claude Code" />)
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
  patchReview(active.id, { comments: [], sending: false, sent: undefined })
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

  const strip = await screen.findByRole('group', { name: 'Changes' })
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
  expect(screen.getByRole('group', { name: 'Changes' }).textContent).toContain('10 files')
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

test('nothing changed is an empty state with nothing to publish', async () => {
  seed({ ...ready, patch: '' })
  useStore.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: [] } })
  renderDiff()
  expect(screen.getByRole('heading', { name: 'No changes yet.' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Publish…' })).toBeNull()
})

test('recorded changes remain readable without checkout-only publishing', () => {
  seed({ ...ready, recorded: true })
  useStore.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: [] } })
  renderDiff()
  expect(screen.getByRole('region', { name: 'cmd/main.go' }).textContent).toContain('new line')
  expect(screen.queryByRole('button', { name: 'Publish…' })).toBeNull()
  act(() => useStore.getState().setDiff(active.id, { recorded: false }))
  expect(screen.getByRole('button', { name: 'Publish…' })).toBeTruthy()
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

test('keeps cumulative changes usable across snapshot failures and marks the recovered interval gap', async () => {
  const failed: DiffSnapshot = { time: '2026-08-14T10:03:00Z', files: [], historyGap: true, snapshotError: 'Snapshot input exceeded its retained capture limit' }
  seed({ ...ready, snapshots: [failed] })
  renderDiff()
  expect(screen.getByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(screen.getByRole('status').textContent).toContain(failed.snapshotError)
  expect(api.runPatch).not.toHaveBeenCalled()

  const recovered = { ...snapshot('2026-08-14T10:04:00Z', 'tree0', 'tree2'), historyGap: true }
  act(() => useStore.getState().setDiff(active.id, { snapshots: [recovered, failed] }))
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree0', patch: newFile, truncated: false })
  openMenu('Current diff')
  fireEvent.click(screen.getAllByRole('menuitemradio', { name: /What changed/ })[0])
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(screen.getByRole('status')).toBeTruthy()
  expect(screen.queryByRole('region', { name: 'cmd/main.go' })).toBeNull()
  chooseInterval(/Current diff/)
  expect(screen.getByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledTimes(1)
})

test('does not silently show current changes when a historical interval is unavailable on the server', async () => {
  const selected = snapshot('2026-08-14T10:03:00Z', 'tree0', 'tree1')
  seed({ ...ready, snapshots: [selected] })
  vi.mocked(api.runPatch).mockRejectedValue(new Error('Retained snapshot history has expired'))
  renderDiff()
  chooseInterval(/What changed/)
  expect(await screen.findByRole('alert')).toBeTruthy()
  expect(screen.queryByRole('region', { name: 'cmd/main.go' })).toBeNull()
  chooseInterval(/Current diff/)
  expect(screen.getByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledTimes(1)
})

test('a snapshot without a tree cannot request an interval', () => {
  seed({ ...ready, snapshots: [{ time: '2026-08-14T10:03:00Z', files: [] }] })
  renderDiff()

  openMenu('Current diff')
  const row = screen.getByRole('menuitemradio', { name: /What changed/ })
  expect(row.getAttribute('aria-disabled')).toBe('true')
  expect(api.runPatch).not.toHaveBeenCalled()
})

test('a selected interval survives menu eviction and a bounded patch-cache reload', async () => {
  const snapshots = Array.from({ length: 40 }, (_, i) =>
    snapshot(`2026-08-14T10:${String(i).padStart(2, '0')}:00Z`, `tree${i}`, `tree${i + 1}`),
  )
  const oldest = snapshots[snapshots.length - 1]
  seed({
    ...ready,
    snapshots,
    intervals: { [intervalKey(oldest.parentTree!, oldest.tree!)]: { patch: newFile, truncated: false, status: 'ready' } },
  })
  vi.mocked(api.runPatch).mockImplementation(async (_run, range) => ({
    run_id: active.id,
    base: range?.from ?? 'abcdef12',
    patch: range ? newFile : patch,
    truncated: false,
  }))
  renderDiff()
  openMenu('Current diff')
  fireEvent.click(screen.getAllByRole('menuitemradio', { name: /What changed/ }).at(-1)!)
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()

  act(() => useStore.getState().noteDiffSnapshot(active.id, snapshot('2026-08-14T11:00:00Z', 'treeX', 'treeY')))
  expect(useStore.getState().diffs[active.id].snapshots.some((s) => s.time === oldest.time)).toBe(false)
  expect(screen.getByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(screen.queryByRole('alert')).toBeNull()

  act(() => {
    for (let i = 0; i < 41; i++) {
      useStore.getState().setIntervalPatch(active.id, `other-${i}`, { patch: '', truncated: false, status: 'ready' })
    }
  })
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(screen.queryByRole('region', { name: 'cmd/main.go' })).toBeNull()
  expect(screen.queryByRole('alert')).toBeNull()
  expect(Object.keys(useStore.getState().diffs[active.id].intervals).length).toBeLessThanOrEqual(40)
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
  render(<ChangesView runID="run_missing" agent={agent()} agentName="Claude Code" />)
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

function gutter(file: string, name: string) {
  return within(screen.getByRole('region', { name: file })).getByRole('button', { name })
}

function editor() {
  return screen.getByRole<HTMLTextAreaElement>('textbox', { name: /^Comment on / })
}

function comment(file: string, line: string, text: string) {
  fireEvent.click(gutter(file, line))
  fireEvent.change(editor(), { target: { value: text } })
  fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
}

const sentBody = [
  "2 review comments on your changes. Each quotes the diff lines it is about; line numbers are the new file's unless marked removed.",
  '',
  '1. cmd/main.go:11',
  '```diff',
  '+new line',
  '```',
  'use the helper',
  '',
  '2. notes.md:1',
  '```diff',
  '+hello',
  '```',
  'say more',
].join('\n')

test('a comment pins under its line, can be edited and deleted, and brings the review bar with it', () => {
  seed(ready)
  renderDiff()
  expect(screen.queryByRole('group', { name: 'Review' })).toBeNull()

  fireEvent.click(gutter('cmd/main.go', 'Comment on line 11'))
  expect(document.activeElement).toBe(editor())
  fireEvent.change(editor(), { target: { value: 'use the helper' } })
  expect(screen.queryByRole('group', { name: 'Review' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Comment' }))

  const card = screen.getByRole('article', { name: 'Comment on cmd/main.go:11' })
  expect(card.textContent).toContain('use the helper')
  expect(card.parentElement!.previousElementSibling!.textContent).toBe('11+new line')
  expect(within(screen.getByRole('group', { name: 'Review' })).getByText('1 comment')).toBeTruthy()
  expect(screen.getByRole('region', { name: 'cmd/main.go' }).querySelector('header')!.textContent).toContain('1 comment')

  fireEvent.click(within(card).getByRole('button', { name: 'Edit comment' }))
  fireEvent.change(editor(), { target: { value: 'use the existing helper' } })
  fireEvent.keyDown(editor(), { key: 'Enter', ctrlKey: true })
  expect(screen.getByRole('article', { name: 'Comment on cmd/main.go:11' }).textContent).toContain('use the existing helper')

  fireEvent.click(screen.getByRole('button', { name: 'Delete comment' }))
  expect(screen.queryByRole('article')).toBeNull()
  expect(screen.queryByRole('group', { name: 'Review' })).toBeNull()
})

test('Escape closes an editor nobody typed into and leaves a typed one alone', () => {
  seed(ready)
  renderDiff()
  fireEvent.click(gutter('cmd/main.go', 'Comment on line 11'))
  fireEvent.change(editor(), { target: { value: 'half a thought' } })
  fireEvent.keyDown(editor(), { key: 'Escape' })
  expect(editor().value).toBe('half a thought')

  fireEvent.change(editor(), { target: { value: '' } })
  fireEvent.keyDown(editor(), { key: 'Escape' })
  expect(screen.queryByRole('textbox')).toBeNull()
})

test('a drag, a Shift-click and Shift with the arrow keys each make a range', () => {
  seed(ready)
  renderDiff()

  fireEvent.pointerDown(gutter('cmd/main.go', 'Comment on line 10'), { button: 0 })
  fireEvent.pointerOver(gutter('cmd/main.go', 'Comment on line 11'))
  act(() => void window.dispatchEvent(new Event('pointerup')))
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

  fireEvent.click(gutter('cmd/main.go', 'Comment on line 11'))
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:11')
  fireEvent.click(gutter('cmd/main.go', 'Comment on line 10'), { shiftKey: true })
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')
  expect(document.activeElement).toBe(editor())
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

  const first = gutter('cmd/main.go', 'Comment on line 10')
  first.focus()
  fireEvent.keyDown(first, { key: 'ArrowDown', shiftKey: true })
  expect(document.activeElement).toBe(gutter('cmd/main.go', 'Comment on removed line 11'))
  fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown', shiftKey: true })
  fireEvent.click(document.activeElement!)
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')
  expect(screen.getAllByRole('textbox')).toHaveLength(1)
})

test('on a touch screen a tapped line shows its gutter, and a second tap on an empty editor makes a range', () => {
  atViewport(390, { pointer: 'coarse' })
  seed(ready)
  renderDiff()

  fireEvent.click(screen.getByText('+new line'))
  expect(document.activeElement).toBe(gutter('cmd/main.go', 'Comment on line 11'))
  expect(screen.queryByRole('textbox')).toBeNull()

  fireEvent.click(gutter('cmd/main.go', 'Comment on line 11'))
  fireEvent.click(gutter('cmd/main.go', 'Comment on line 10'))
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')

  fireEvent.change(editor(), { target: { value: 'typed' } })
  fireEvent.click(gutter('notes.md', 'Comment on line 1'))
  expect(screen.getAllByRole('textbox')).toHaveLength(2)
})

test('send posts every comment as one message under the lease this tab holds and says where it went', async () => {
  seed(ready)
  renderDiff()
  comment('notes.md', 'Comment on line 1', 'say more')
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  vi.mocked(api.runRoomPost).mockResolvedValueOnce({ message: roomMessage({ id: 'msg_review', run_id: active.id, kind: 'steer_request' }), receipt: 'sent' })

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))

  const bar = within(await screen.findByRole('group', { name: 'Review' }))
  expect((await bar.findByRole('status')).textContent).toContain('Sent 2 comments to Claude Code.')
  expect(api.runRoomPost).toHaveBeenCalledTimes(1)
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0]).toMatchObject({
    workspace_id: active.workspace_id, run_id: active.id, kind: 'steer_request', body: sentBody,
    control_session_id: 'ctl', control_generation: 3,
  })
  expect(screen.queryByRole('article')).toBeNull()

  fireEvent.click(bar.getByRole('button', { name: 'Open the session' }))
  expect(useStore.getState().route.params).toEqual({ runId: active.id, view: 'session' })
})

test('send takes the text in an open editor with it', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  fireEvent.click(gutter('notes.md', 'Comment on line 1'))
  fireEvent.change(editor(), { target: { value: 'say more' } })

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))

  await waitFor(() => expect(api.runRoomPost).toHaveBeenCalled())
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0].body).toBe(sentBody)
  await waitFor(() => expect(screen.queryByRole('textbox')).toBeNull())
})

test('a send that fails keeps every comment, shows the server error, and resends under the same key', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  vi.mocked(api.runRoomPost).mockRejectedValueOnce(new Error('run.room.post: connection lost'))

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))

  expect((await screen.findByRole('alert')).textContent).toBe('run.room.post: connection lost')
  expect(screen.getByRole('article', { name: 'Comment on cmd/main.go:11' })).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
  await screen.findByText(/^Sent 1 comment to Claude Code/)
  const [first, second] = vi.mocked(api.runRoomPost).mock.calls.map(([request]) => request.idempotency_key)
  expect(second).toBe(first)
})

test('a message the server could not deliver keeps the comments and says why', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  vi.mocked(api.runRoomPost).mockResolvedValueOnce({
    message: roomMessage({ run_id: active.id, kind: 'steer_request', state: 'not_sent', failure: { code: 'session_unavailable', message: 'run session unavailable' } }),
    receipt: 'not_sent',
  })

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))

  expect((await screen.findByRole('alert')).textContent).toBe('Not sent: run session unavailable.')
  expect(screen.getByRole('article', { name: 'Comment on cmd/main.go:11' })).toBeTruthy()
})

test('without the lease the message waits for the controller, and the bar says so before and after', async () => {
  seed(ready)
  renderDiff({ localControl: false, roomControl: undefined, controlUnavailable: true })
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  const bar = within(screen.getByRole('group', { name: 'Review' }))
  expect(bar.getByText('Delivers in 45 s unless the controller decides sooner.')).toBeTruthy()
  vi.mocked(api.runRoomPost).mockResolvedValueOnce({ message: roomMessage({ id: 'msg_queued', run_id: active.id, kind: 'steer_request', state: 'queued' }) })

  fireEvent.click(bar.getByRole('button', { name: 'Send to agent' }))

  expect((await screen.findByRole('status')).textContent).toContain('1 comment queued for Claude Code. Delivers in 45 s unless the controller decides sooner.')
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0]).not.toHaveProperty('control_session_id')

  act(() => useStore.getState().upsertRoomMessage(roomMessage({ id: 'msg_queued', run_id: active.id, kind: 'steer_request', state: 'denied', updated_at: '2026-08-14T10:01:00Z' })))
  expect(screen.getByRole('status').textContent).toContain('The controller declined your 1 comment. The text is in the session.')
})

test('a run that cannot take a message disables send and says why', () => {
  seed(ready)
  useStore.setState({ runs: { [active.id]: toRecord(run({ id: active.id, status: 'completed' })) } })
  const view = renderDiff()
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  const bar = within(screen.getByRole('group', { name: 'Review' }))
  expect(bar.getByRole('button', { name: 'Send to agent' })).toHaveProperty('disabled', true)
  expect(bar.getByText('This run has finished.')).toBeTruthy()
  view.unmount()

  onTestFinished(() => {
    useStore.setState({ info: null })
  })
  useStore.setState({ runs: { [active.id]: toRecord(run({ id: active.id, member_id: bob.id })) }, info: { ...serverInfo, member: vera } })
  renderDiff()
  expect(screen.getByRole('button', { name: 'Send to agent' })).toHaveProperty('disabled', true)
  expect(screen.getByText('You can watch this run but not message the agent.')).toBeTruthy()
})

test('discarding asks first, then removes every comment', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  comment('notes.md', 'Comment on line 1', 'say more')

  fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
  const dialog = within(await screen.findByRole('alertdialog', { name: 'Discard 2 comments?' }))
  fireEvent.click(dialog.getByRole('button', { name: 'Keep' }))
  expect(screen.getAllByRole('article')).toHaveLength(2)

  fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
  fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Discard' }))
  await waitFor(() => expect(screen.queryByRole('article')).toBeNull())
  expect(screen.queryByRole('group', { name: 'Review' })).toBeNull()
})

test('a comment follows its line through a refresh, and is kept as outdated when the line or the file goes', async () => {
  seed(ready)
  const view = renderDiff()
  comment('cmd/main.go', 'Comment on line 11', 'use the helper')
  const refresh = (next: string) => act(() => useStore.getState().applyPatch({ run_id: active.id, base: ready.base, patch: next, truncated: false }, 0))

  refresh(patch.replace('@@ -10,3 +10,3 @@\n package main', '@@ -10,3 +10,4 @@\n package main\n+import "fmt"'))
  expect(screen.getByRole('article', { name: 'Comment on cmd/main.go:12' }).textContent).toContain('use the helper')

  view.unmount()
  renderDiff()
  expect(screen.getByRole('article', { name: 'Comment on cmd/main.go:12' })).toBeTruthy()

  refresh(patch.replace('+new line', '+newer line'))
  const outdated = screen.getByRole('article', { name: 'Comment on cmd/main.go:11' })
  expect(outdated.textContent).toContain('Outdated')
  expect(within(outdated).getByText('+new line')).toBeTruthy()
  expect(outdated.textContent).toContain('use the helper')
  expect(within(screen.getByRole('region', { name: 'cmd/main.go' })).getByText('+newer line')).toBeTruthy()

  refresh(newFile)
  expect(within(screen.getByRole('region', { name: 'Outdated comments' })).getByRole('article', { name: 'Comment on cmd/main.go:11' })).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
  await waitFor(() => expect(api.runRoomPost).toHaveBeenCalled())
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0].body).toContain('1. cmd/main.go:11 (these lines have changed since the comment was written)\n```diff\n+new line\n```\nuse the helper')
})

test('a route that names a file opens the current diff at it', async () => {
  seed(ready)
  useStore.setState({ route: { name: 'run', params: { runId: active.id, view: 'changes', file: 'db/schema.sql' } } })
  renderDiff()

  expect(screen.getByRole('button', { name: 'Collapse db/schema.sql' })).toBeTruthy()
  const files = within(screen.getByRole('navigation', { name: 'Changed files' }))
  expect(files.getByRole('button', { name: /schema\.sql/ }).getAttribute('aria-current')).toBe('true')

  act(() => useStore.getState().navigate('run', { runId: active.id, view: 'changes', file: 'notes.md' }))
  await waitFor(() => expect(files.getByRole('button', { name: /notes\.md/ }).getAttribute('aria-current')).toBe('true'))
})

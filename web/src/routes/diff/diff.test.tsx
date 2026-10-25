import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { parsePatch } from '@/routes/diff/parse'
import { ChangesView } from '@/routes/diff'
import { patchReview, review } from '@/routes/diff/review'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { clockTime } from '@/lib/format'
import { initialDiff, type Checkpoint, type DiffSnapshot, type RunDiffState } from '@/store/diff'
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

const active = run({ id: 'run_1', base_branch: 'main', base_commit: 'abcdef1234567890' })
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

function checkpoint(time: string, tree: string | undefined, over: Partial<Checkpoint> = {}): Checkpoint {
  return { time, tree, files: 1, additions: 1, deletions: 1, ...over }
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

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.workspaceTimeline).mockResolvedValue({ events: [], next_seq: 0, more: false })
  patchReview(active.id, { comments: [], sending: undefined, sent: undefined })
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

test('a file path opens that file in the run’s Files view', () => {
  seed(ready)
  useStore.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: [] } })
  renderDiff()

  fireEvent.click(screen.getByRole('button', { name: 'notes.md' }))

  expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: active.id, view: 'files', file: 'notes.md' } })
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

const hour = (h: number) => `2026-08-14T${String(h).padStart(2, '0')}:00:00Z`

/** The Compare menu's row for a checkpoint taken at `time`. */
const at = (time: string) => new RegExp(`^${clockTime(time).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`)

function chooseCompare(name: RegExp | string) {
  openMenu(/All changes|Since/)
  fireEvent.click(screen.getByRole('menuitemradio', { name }))
}

test('a diff snapshot refetches the patch and joins the Compare menu', async () => {
  seed(ready)
  renderDiff()
  expect(api.runPatch).not.toHaveBeenCalled()

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'abcdef12', patch, truncated: false })
  act(() =>
    useStore.getState().noteDiffSnapshot(active.id, {
      time: new Date().toISOString(),
      files: [{ path: 'notes.md', additions: 1, deletions: 0 }],
      tree: 'tree1',
    }),
  )

  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))
  expect(api.runPatch).toHaveBeenCalledWith(active.id)
  openMenu('All changes')
  expect(screen.getByRole('menuitemradio', { name: /^All changes/ }).textContent).toContain('Against main @ abcdef1')
  expect(screen.getByRole('menuitemradio', { name: /ago|now/ }).textContent).toContain('1 file')
  expect(screen.queryByRole('menuitemradio', { name: /Since the run started/ })).toBeNull()
})

test('checkpoints load from history when the view opens, newest first, and older ones on request', async () => {
  seed(ready)
  useStore.setState({ connection: 'live' })
  const event = (seq: number, time: string, head: string, subject: string, tree: string) => ({
    seq,
    time,
    type: 'run.diff',
    payload: { files: [{ path: 'notes.md', additions: 2, deletions: 1 }], tree, head, head_subject: subject },
  })
  vi.mocked(api.workspaceTimeline).mockImplementation(async (query) =>
    query.before_seq
      ? { events: [event(1, hour(9), 'headA', 'first', 'tree0')] as never, next_seq: 9, more: false }
      : {
          events: [event(2, hour(10), 'headA', 'first', 'tree1'), event(3, hour(11), 'headB', 'fix the parser', 'tree2')] as never,
          next_seq: 9,
          more: true,
          older_seq: 2,
        },
  )
  renderDiff()

  await waitFor(() => expect(useStore.getState().diffs[active.id].checkpoints).toHaveLength(2))
  expect(api.workspaceTimeline).toHaveBeenCalledWith({ workspace_id: workspace.id, run_id: active.id, types: ['run.diff'], newest: true, limit: 50 })
  openMenu('All changes')
  const rows = screen.getAllByRole('menuitemradio').slice(1)
  expect(rows.map((row) => row.textContent)).toEqual([
    expect.stringContaining('fix the parser'),
    expect.stringMatching(/1 file · \+2\s*−1/),
  ])
  expect(rows[0].textContent).toContain(clockTime(hour(11)))

  fireEvent.click(screen.getByRole('menuitem', { name: 'Show older checkpoints' }))
  await waitFor(() => expect(screen.getAllByRole('menuitemradio')).toHaveLength(4))
  expect(api.workspaceTimeline).toHaveBeenLastCalledWith({ workspace_id: workspace.id, run_id: active.id, types: ['run.diff'], newest: true, limit: 50, before_seq: 2 })
  expect(screen.queryByRole('menuitem', { name: 'Show older checkpoints' })).toBeNull()
  expect(useStore.getState().diffs[active.id].checkpoints[0]).not.toHaveProperty('files', expect.any(Array))
})

test('a failed history read shows the server message and Retry reads again', async () => {
  seed(ready)
  useStore.setState({ connection: 'live' })
  vi.mocked(api.workspaceTimeline).mockRejectedValueOnce(new Error('timeline unavailable'))
  renderDiff()

  await waitFor(() => expect(useStore.getState().diffs[active.id].historyError).toBe('timeline unavailable'))
  openMenu('All changes')
  expect(screen.getByText('Checkpoints could not be loaded: timeline unavailable')).toBeTruthy()
  fireEvent.click(screen.getByRole('menuitem', { name: 'Retry' }))
  await waitFor(() => expect(api.workspaceTimeline).toHaveBeenCalledTimes(2))
  await waitFor(() => expect(useStore.getState().diffs[active.id].history).toEqual({ more: false, before: undefined }))
})

const afterCheckpoint = { ...ready, checkpoints: [checkpoint(hour(10), 'tree1')], history: { more: false } }

test('choosing a checkpoint shows what changed since it, refetches on new snapshots, and never shows another comparison meanwhile', async () => {
  seed(afterCheckpoint)
  let land: (p: RunPatch) => void = () => {}
  vi.mocked(api.runPatch).mockReturnValueOnce(new Promise<RunPatch>((resolve) => (land = resolve)))
  renderDiff()

  chooseCompare(at(hour(10)))
  expect(await screen.findByText('Loading the diff…')).toBeTruthy()
  expect(screen.queryByRole('region', { name: 'cmd/main.go' })).toBeNull()
  expect(screen.getByRole('button', { name: `Since ${clockTime(hour(10))}` })).toBeTruthy()

  await act(async () => land({ run_id: active.id, base: 'tree1', patch: newFile, truncated: false }))
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledWith(active.id, { from: 'tree1' })
  expect(useStore.getState().diffs[active.id].base).toBe('abcdef1234567890')

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch: newFile + patch, truncated: false })
  act(() => useStore.getState().noteDiffSnapshot(active.id, { time: hour(11), files: [], tree: 'tree2' }))
  expect(await screen.findByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenLastCalledWith(active.id, { from: 'tree1' })

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'abcdef12', patch, truncated: false })
  chooseCompare(/^All changes/)
  await waitFor(() => expect(screen.queryByRole('region', { name: 'newer.txt' })).toBeNull())
  expect(screen.getByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenLastCalledWith(active.id)
})

test('a link to the current diff goes back to All changes', async () => {
  seed(afterCheckpoint)
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch: newFile, truncated: false })
  renderDiff()
  chooseCompare(at(hour(10)))
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'abcdef12', patch, truncated: false })
  act(() => useStore.getState().navigate('run', { runId: active.id, view: 'changes', diff: 'current' }))
  expect(await screen.findByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'All changes' })).toBeTruthy()
})

test('Since the run started appears only once the base has moved past the fork point, and asks for the fork tree', async () => {
  seed({ ...ready, forkTree: 'forktree' })
  useStore.setState({ runs: { [active.id]: toRecord({ ...active, base_commit: 'abcdef1234567890' }) } })
  const view = renderDiff()
  openMenu('All changes')
  expect(screen.queryByRole('menuitemradio', { name: /Since the run started/ })).toBeNull()
  view.unmount()

  // A recorded patch names its base by tree; the fork tree means no upstream merge.
  seed({ ...ready, base: 'forktree', forkTree: 'forktree', recorded: true })
  useStore.setState({ runs: { [active.id]: toRecord({ ...active, base_commit: 'abcdef1234567890' }) } })
  const recorded = renderDiff()
  openMenu('All changes')
  expect(screen.queryByRole('menuitemradio', { name: /Since the run started/ })).toBeNull()
  recorded.unmount()

  seed({ ...ready, forkTree: 'forktree' })
  useStore.setState({ runs: { [active.id]: toRecord({ ...active, base_commit: '1111111222222233' }) } })
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'forktree', patch: newFile, truncated: false })
  renderDiff()
  openMenu('All changes')
  const row = screen.getByRole('menuitemradio', { name: /Since the run started/ })
  expect(row.textContent).toContain('Against 1111111')
  expect(row.textContent).toContain('upstream work merged since')
  fireEvent.click(row)

  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledWith(active.id, { from: 'forktree' })
  expect(screen.getByRole('button', { name: 'Since the run started' })).toBeTruthy()
})

test('a comparison that fails shows the server message, keeps the other patch off screen, and Refresh retries it', async () => {
  seed(afterCheckpoint)
  vi.mocked(api.runPatch).mockRejectedValue(new Error("run.patch: that snapshot's tree is no longer on disk"))
  renderDiff()

  chooseCompare(at(hour(10)))
  expect((await screen.findByRole('alert')).textContent).toContain("run.patch: that snapshot's tree is no longer on disk")
  expect(screen.queryByRole('region', { name: 'cmd/main.go' })).toBeNull()
  expect(api.runPatch).toHaveBeenCalledTimes(1)

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch: newFile, truncated: false })
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'abcdef12', patch, truncated: false })
  chooseCompare(/^All changes/)
  expect(await screen.findByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
})

test('a failed refresh of a checkpoint comparison keeps its patch with the server error', async () => {
  seed(afterCheckpoint)
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch: newFile, truncated: false })
  renderDiff()
  chooseCompare(at(hour(10)))
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()

  vi.mocked(api.runPatch).mockRejectedValue(new Error('patch store unavailable'))
  act(() => useStore.getState().noteDiffSnapshot(active.id, { time: hour(11), files: [], tree: 'tree2' }))

  expect((await screen.findByRole('alert')).textContent).toContain('patch store unavailable')
  expect(screen.getByRole('region', { name: 'newer.txt' })).toBeTruthy()
})

test('a large checkpoint patch waits for Refresh like a large cumulative one', async () => {
  seed(afterCheckpoint)
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch: single('min.js', 1_100_000), truncated: false })
  renderDiff()
  chooseCompare(at(hour(10)))
  expect(await screen.findByRole('region', { name: 'min.js' })).toBeTruthy()

  act(() => useStore.getState().noteDiffSnapshot(active.id, { time: hour(11), files: [], tree: 'tree2' }))
  fireEvent.click(await screen.findByRole('button', { name: 'New changes · Refresh' }))
  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(2))
  expect(api.runPatch).toHaveBeenLastCalledWith(active.id, { from: 'tree1' })
})

test('checkpoints without a tree cannot be compared against; a gap before a tree does not block it', () => {
  seed({
    ...ready,
    history: { more: false },
    checkpoints: [
      checkpoint(hour(13), undefined),
      checkpoint(hour(12), 'tree3', { historyGap: true }),
      checkpoint(hour(11), undefined, { historyGap: true, snapshotError: 'Snapshot input exceeded its retained capture limit' }),
    ],
  })
  renderDiff()

  openMenu('All changes')
  const rows = screen.getAllByRole('menuitemradio').slice(1)
  expect(rows.map((row) => row.getAttribute('aria-disabled'))).toEqual(['true', null, 'true'])
  expect(rows[0].textContent).toContain('did not record a tree')
  expect(rows[2].textContent).toContain('Snapshot input exceeded its retained capture limit')
  expect(api.runPatch).not.toHaveBeenCalled()
})

test('keeps all changes usable across snapshot failures', () => {
  const failed: DiffSnapshot = { time: '2026-08-14T10:03:00Z', files: [], historyGap: true, snapshotError: 'Snapshot input exceeded its retained capture limit' }
  seed({ ...ready, snapshots: [failed] })
  renderDiff()
  expect(screen.getByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
  expect(screen.getByRole('status').textContent).toContain(failed.snapshotError)
  expect(api.runPatch).not.toHaveBeenCalled()
})

test('comments belong to the comparison they were written on', async () => {
  seed(afterCheckpoint)
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch, truncated: false })
  renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')

  chooseCompare(at(hour(10)))
  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))
  comment('notes.md', '+hello', 'and this one')
  expect(review(active.id).comments.map((c) => [c.body, c.scope])).toEqual([
    ['use the helper', ''],
    ['and this one', 'since:tree1'],
  ])

  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'tree1', patch: newFile, truncated: false })
  act(() => useStore.getState().noteDiffSnapshot(active.id, { time: hour(11), files: [], tree: 'tree2' }))
  const outdated = within(await screen.findByRole('region', { name: 'Outdated comments' }))
  expect(outdated.getAllByRole('article')).toHaveLength(1)
  expect(outdated.getByRole('article').textContent).toContain('and this one')
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

/** The number gutter of the line whose code reads `code`, marker included. */
function gutter(file: string, code: string) {
  return within(screen.getByRole('region', { name: file })).getByText(code).previousElementSibling as HTMLElement
}

function editor() {
  return screen.getByRole<HTMLTextAreaElement>('textbox', { name: /^Comment on / })
}

function comment(file: string, code: string, text: string) {
  fireEvent.click(gutter(file, code))
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

  fireEvent.click(gutter('cmd/main.go', '+new line'))
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
  fireEvent.click(gutter('cmd/main.go', '+new line'))
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

  fireEvent.pointerDown(gutter('cmd/main.go', 'package main'), { button: 0 })
  fireEvent.pointerOver(gutter('cmd/main.go', '+new line'))
  act(() => void window.dispatchEvent(new Event('pointerup')))
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

  fireEvent.click(gutter('cmd/main.go', '+new line'))
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:11')
  fireEvent.click(gutter('cmd/main.go', 'package main'), { shiftKey: true })
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')
  expect(document.activeElement).toBe(editor())
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

  // Each file has one gutter button; closing an editor leaves it on that
  // comment's last line, and the arrow keys move it.
  const file = within(screen.getByRole('region', { name: 'cmd/main.go' }))
  const cursor = () => file.getByRole('button', { name: /^Comment on / })
  expect(cursor().getAttribute('aria-label')).toBe('Comment on line 11')
  expect(document.activeElement).toBe(cursor())
  fireEvent.keyDown(cursor(), { key: 'ArrowUp' })
  fireEvent.keyDown(cursor(), { key: 'ArrowUp' })
  expect(cursor()).toBe(gutter('cmd/main.go', 'package main'))
  expect(document.activeElement).toBe(cursor())
  fireEvent.keyDown(cursor(), { key: 'ArrowDown', shiftKey: true })
  expect(cursor().getAttribute('aria-label')).toBe('Comment on removed line 11')
  fireEvent.keyDown(cursor(), { key: 'ArrowDown', shiftKey: true })
  fireEvent.click(cursor())
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')
  expect(screen.getAllByRole('textbox')).toHaveLength(1)
})

test('a drag whose press blurs a control in the same file still makes its range', () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')
  const edit = screen.getByRole('button', { name: 'Edit comment' })
  edit.focus()

  fireEvent.pointerDown(gutter('cmd/main.go', 'package main'), { button: 0 })
  fireEvent.blur(edit)
  fireEvent.pointerOver(gutter('cmd/main.go', '+new line'))
  act(() => void window.dispatchEvent(new Event('pointerup')))

  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')
})

test('on a touch screen a tapped line shows its gutter, and a second tap on an empty editor makes a range', () => {
  atViewport(390, { pointer: 'coarse' })
  seed(ready)
  renderDiff()

  fireEvent.click(screen.getByText('+new line'))
  expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Comment on line 11' }))
  expect(screen.queryByRole('textbox')).toBeNull()

  fireEvent.click(gutter('cmd/main.go', '+new line'))
  fireEvent.click(gutter('cmd/main.go', 'package main'))
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10-11')

  fireEvent.change(editor(), { target: { value: 'typed' } })
  fireEvent.click(gutter('notes.md', '+hello'))
  expect(screen.getAllByRole('textbox')).toHaveLength(2)
})

test('send posts every comment as one message under the lease this tab holds and says where it went', async () => {
  seed(ready)
  renderDiff()
  comment('notes.md', '+hello', 'say more')
  comment('cmd/main.go', '+new line', 'use the helper')
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
  comment('cmd/main.go', '+new line', 'use the helper')
  fireEvent.click(gutter('notes.md', '+hello'))
  fireEvent.change(editor(), { target: { value: 'say more' } })

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))

  await waitFor(() => expect(api.runRoomPost).toHaveBeenCalled())
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0].body).toBe(sentBody)
  await waitFor(() => expect(screen.queryByRole('textbox')).toBeNull())
})

test('a send that fails keeps every comment, shows the server error, and resends under the same key', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')
  vi.mocked(api.runRoomPost).mockRejectedValueOnce(new Error('run.room.post: connection lost'))

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))

  expect((await screen.findByRole('alert')).textContent).toBe('run.room.post: connection lost')
  expect(screen.getByRole('article', { name: 'Comment on cmd/main.go:11' })).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
  await screen.findByText(/^Sent 1 comment to Claude Code/)
  const [first, second] = vi.mocked(api.runRoomPost).mock.calls.map(([request]) => request.idempotency_key)
  expect(second).toBe(first)
})

test('text changed while a send is out stays, and a send that outlives a sign-in leaves nothing behind', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')
  comment('notes.md', '+hello', 'say more')
  let answer!: () => void
  let answers = 0
  vi.mocked(api.runRoomPost).mockImplementation(() => new Promise((resolve) => {
    answer = () => resolve({ message: roomMessage({ id: `msg_${++answers}`, run_id: active.id, kind: 'steer_request' }), receipt: 'sent' })
  }))
  onTestFinished(() => {
    vi.mocked(api.runRoomPost).mockImplementation(async () => ({ message: roomMessage() }))
    useStore.setState({ identityKey: null, roomMessages: {} })
  })

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
  await waitFor(() => expect(api.runRoomPost).toHaveBeenCalledTimes(1))
  fireEvent.click(within(screen.getByRole('article', { name: 'Comment on notes.md:1' })).getByRole('button', { name: 'Edit comment' }))
  fireEvent.change(editor(), { target: { value: 'say much more' } })
  await act(async () => answer())

  expect(screen.queryByRole('article', { name: 'Comment on cmd/main.go:11' })).toBeNull()
  expect(editor().value).toBe('say much more')

  fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
  await waitFor(() => expect(api.runRoomPost).toHaveBeenCalledTimes(2))
  act(() => useStore.setState({ identityKey: 'someone-else' }))
  await act(async () => answer())

  expect(screen.queryByRole('group', { name: 'Review' })).toBeNull()
  const stored = useStore.getState().roomMessages[active.id].map((entry) => entry.id)
  expect(stored).toContain('msg_1')
  expect(stored).not.toContain('msg_2')
})

test('an emptied editor sends its pinned text, and an editor nobody typed into stays open', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')
  comment('notes.md', '+hello', 'say more')
  fireEvent.click(within(screen.getByRole('article', { name: 'Comment on notes.md:1' })).getByRole('button', { name: 'Edit comment' }))
  fireEvent.change(editor(), { target: { value: '' } })
  expect(within(screen.getByRole('group', { name: 'Review' })).getByText('2 comments')).toBeTruthy()
  fireEvent.click(gutter('cmd/main.go', 'package main'))

  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))

  await screen.findByText(/^Sent 2 comments to Claude Code/)
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0].body).toBe(sentBody)
  expect(editor().getAttribute('aria-label')).toBe('Comment on cmd/main.go:10')
})

test('a send error stays in the bar when the run stops taking messages', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')
  vi.mocked(api.runRoomPost).mockRejectedValueOnce(new Error('run.room.post: connection lost'))
  fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
  await screen.findByRole('alert')

  act(() => useStore.setState({ runs: { [active.id]: toRecord(run({ id: active.id, status: 'completed' })) } }))

  expect(screen.getByRole('alert').textContent).toBe('run.room.post: connection lost')
  expect(screen.getByText('This run has finished.')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Send to agent' })).toHaveProperty('disabled', true)
})

test('a message the server could not deliver keeps the comments and says why', async () => {
  seed(ready)
  renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')
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
  comment('cmd/main.go', '+new line', 'use the helper')
  const bar = within(screen.getByRole('group', { name: 'Review' }))
  expect(bar.getByText('Delivers in 45 s unless the controller decides sooner.')).toBeTruthy()
  vi.mocked(api.runRoomPost).mockResolvedValueOnce({ message: roomMessage({ id: 'msg_queued', run_id: active.id, kind: 'steer_request', state: 'queued' }) })

  fireEvent.click(bar.getByRole('button', { name: 'Send to agent' }))

  expect((await screen.findByRole('status')).textContent).toContain('1 comment queued for Claude Code. Delivers in 45 s unless the controller decides sooner.')
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0]).not.toHaveProperty('control_session_id')

  act(() => useStore.getState().upsertRoomMessage(roomMessage({ id: 'msg_queued', run_id: active.id, kind: 'steer_request', state: 'denied', updated_at: '2026-08-14T10:01:00Z' })))
  expect(screen.getByRole('status').textContent).toContain('The controller declined your 1 comment. The text is in the session.')
})

test('while you control the run from another tab, the bar does not promise a 45 s wait', () => {
  seed(ready)
  onTestFinished(() => {
    useStore.setState({ info: null })
  })
  useStore.setState({ runs: { [active.id]: toRecord({ ...active, controller_member_id: alice.id }) }, info: serverInfo })
  renderDiff({ localControl: false, roomControl: undefined, controlUnavailable: true })
  comment('cmd/main.go', '+new line', 'use the helper')
  const bar = within(screen.getByRole('group', { name: 'Review' }))
  expect(bar.queryByText('Delivers in 45 s unless the controller decides sooner.')).toBeNull()
  expect(bar.getByRole('button', { name: 'Send to agent' })).toHaveProperty('disabled', false)
})

test('a run that cannot take a message disables send and says why', () => {
  seed(ready)
  useStore.setState({ runs: { [active.id]: toRecord(run({ id: active.id, status: 'completed' })) } })
  const view = renderDiff()
  comment('cmd/main.go', '+new line', 'use the helper')
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
  comment('cmd/main.go', '+new line', 'use the helper')
  comment('notes.md', '+hello', 'say more')

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
  comment('cmd/main.go', '+new line', 'use the helper')
  const refresh = (next: string) => act(() => useStore.getState().applyPatch({ run_id: active.id, base: ready.base, patch: next, truncated: false }, 0, ''))

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

function single(path: string, characters: number): string {
  return `diff --git a/${path} b/${path}\nnew file mode 100644\n--- /dev/null\n+++ b/${path}\n@@ -0,0 +1 @@\n+${'x'.repeat(characters)}\n`
}

test('a 20 MB patch splits quickly into files with counts and no line parsing', () => {
  const body = Array.from({ length: 200_000 }, (_, i) => `+${String(i).padStart(6, '0')} ${'y'.repeat(90)}`)
  const huge = ['diff --git a/huge.txt b/huge.txt', '--- a/huge.txt', '+++ b/huge.txt', '@@ -0,0 +1,200000 @@', ...body, ''].join('\n')
  const text = Array.from({ length: 2000 }, (_, i) => added(`dir/f${i}.txt`, 10)).join('') + single('min.js', 5_000_000) + huge
  expect(text.length).toBeGreaterThan(20_000_000)

  const started = performance.now()
  const files = parsePatch(text)
  const elapsed = performance.now() - started

  expect(elapsed).toBeLessThan(1500)
  expect(files).toHaveLength(2002)
  expect(files.at(-2)).toMatchObject({ path: 'min.js', contentLines: 1, additions: 1 })
  expect(files.at(-1)).toMatchObject({ path: 'huge.txt', contentLines: 200_000, additions: 200_000 })
  expect(files[0].lines[0]).toEqual({ kind: 'hunk', text: '@@ -0,0 +1,10 @@' })
  expect(files[0].lines).toBe(files[0].lines)
})

test('a refetch keeps the file objects whose text did not change', () => {
  const before = parsePatch(patch)
  const after = parsePatch(patch.replace('+new line', '+newer line') + newFile, before)

  expect(after.map((file) => file.path)).toEqual(['cmd/main.go', 'db/schema.sql', 'notes.md', 'newer.txt'])
  expect(after[0]).not.toBe(before[0])
  expect(after[0].lines).toContainEqual({ kind: 'add', text: 'newer line', new: 11 })
  expect(after[1]).toBe(before[1])
  expect(after[2]).toBe(before[2])
})

test('a file big in bytes starts closed, says its size and caps what one line renders', () => {
  seed({ ...ready, patch: single('min.js', 600_000) + added('small.txt', 3) })
  renderDiff()

  const bundle = screen.getByRole('region', { name: 'min.js' })
  expect(bundle.textContent).toContain('586 KiB')
  expect(within(bundle).queryByText(/^\+x/)).toBeNull()
  expect(screen.getByRole('button', { name: 'Collapse small.txt' })).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: 'Expand min.js' }))
  const line = within(bundle).getByText(/more characters$/).parentElement!
  expect(line.textContent!.length).toBeLessThan(3100)
  expect(line.textContent).toContain('… 597,000 more characters')
})

test('many files mount a bounded number of file sections and list rows', () => {
  seed({ ...ready, patch: Array.from({ length: 400 }, (_, i) => added(`dir/f${i}.txt`, 2)).join('') })
  renderDiff()

  expect(screen.getByRole('group', { name: 'Changes' }).textContent).toContain('400 files')
  expect(document.querySelectorAll('section[aria-label^="dir/"]').length).toBeLessThan(100)
  expect(within(screen.getByRole('navigation', { name: 'Changed files' })).queryAllByRole('listitem').length).toBeLessThan(100)
})

test('the strip menu lists a bounded number of files and says how many it left out', () => {
  seed({ ...ready, patch: Array.from({ length: 150 }, (_, i) => added(`dir/f${i}.txt`, 1)).join('') })
  renderDiff()

  fireEvent.pointerDown(screen.getByRole('button', { name: /150 files/ }), { button: 0, ctrlKey: false })
  expect(screen.getAllByRole('menuitem')).toHaveLength(100)
  expect(screen.getByText('50 more files are not listed.')).toBeTruthy()
})

const big = (text: string) => single('min.js', 1_100_000) + text

test('a large patch is not refetched on a new snapshot; the strip offers New changes and Refresh loads them', async () => {
  seed({ ...ready, patch: big(patch) })
  vi.mocked(api.runPatch).mockResolvedValue({ run_id: active.id, base: 'abcdef12', patch: big(patch + newFile), truncated: false })
  renderDiff()

  act(() => useStore.getState().noteDiffSnapshot(active.id, { time: '2026-08-14T10:03:00Z', files: [{ path: 'newer.txt', additions: 1, deletions: 0 }] }))
  const refresh = await screen.findByRole('button', { name: 'New changes · Refresh' })
  expect(api.runPatch).not.toHaveBeenCalled()
  expect(screen.queryByRole('region', { name: 'newer.txt' })).toBeNull()

  fireEvent.click(refresh)
  expect(await screen.findByRole('region', { name: 'newer.txt' })).toBeTruthy()
  expect(api.runPatch).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('button', { name: 'New changes · Refresh' })).toBeNull()
})

test('a failed refresh of a large patch keeps it on screen with the server error', async () => {
  seed({ ...ready, patch: big(patch) })
  vi.mocked(api.runPatch).mockRejectedValue(new Error('patch store unavailable'))
  renderDiff()

  act(() => useStore.getState().noteDiffSnapshot(active.id, { time: '2026-08-14T10:03:00Z', files: [] }))
  fireEvent.click(await screen.findByRole('button', { name: 'New changes · Refresh' }))

  expect((await screen.findByRole('alert')).textContent).toContain('patch store unavailable')
  expect(screen.getByRole('region', { name: 'cmd/main.go' })).toBeTruthy()
})

test('a remount during a fetch does not start a second one', async () => {
  seed({ ...ready, revision: 1 })
  let land: (p: RunPatch) => void = () => {}
  vi.mocked(api.runPatch).mockReturnValue(new Promise<RunPatch>((resolve) => (land = resolve)))
  const first = renderDiff()
  await waitFor(() => expect(api.runPatch).toHaveBeenCalledTimes(1))
  first.unmount()
  renderDiff()
  expect(api.runPatch).toHaveBeenCalledTimes(1)
  await act(async () => land({ run_id: active.id, base: 'abcdef12', patch, truncated: false }))
})

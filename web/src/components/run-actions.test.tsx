import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast, type Action } from 'sonner'
import type { Mock } from 'vitest'
import { RunActions } from '@/components/run-actions'
import { api, codeInvalidState, codeNotFound } from '@/lib/api'
import type * as apiModule from '@/lib/api'
import type { GatewayCapabilities, Member, PullResult, Run } from '@/lib/types'
import { useStore } from '@/store'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { toRecord } from '@/store/runs'
import type { RunRecord } from '@/store/runs'
import { alice, bob, run, serverInfo, vera, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  // Vitest hoists this factory before static fixture imports initialize.
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn(), dismiss: vi.fn() },
}))

/** A JSON-RPC refusal; an error with no code reads as a request the server never answered. */
const refusal = (text: string) => Object.assign(new Error(text), { code: -32001 })

const every: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }

function seed(
  over: {
    run?: Partial<Run>
    paused?: boolean
    local?: string[]
    members?: Member[]
    self?: Member
    steerOthers?: string
  } = {},
): RunRecord {
  const record = toRecord(run({ id: 'run_1', ...over.run }))
  const members = over.members ?? [alice]
  const self = over.self ?? alice
  useStore.setState({
    workspaces: {
      [workspace.id]: { ...workspace, steer_others: over.steerOthers },
    },
    activeWorkspace: workspace.id,
    members: Object.fromEntries(members.map((m) => [m.id, m])),
    runs: { [record.id]: record },
    pausedRuns: over.paused === undefined ? {} : { [record.id]: over.paused },
    capabilities: over.local ? { ...every, local: over.local } : every,
    info: { ...serverInfo, member: self },
    hydrated: true,
    paletteDialog: null,
    paletteRunID: null,
    predicting: {},
    deletingRuns: {},
  })
  return record
}

async function openMore() {
  const trigger = screen.getByRole('button', { name: 'More' })
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  return within(await screen.findByRole('menu'))
}

async function selectMore(name: string) {
  const menu = await openMore()
  fireEvent.click(menu.getByRole('menuitem', { name }))
}

beforeEach(() => {
  vi.clearAllMocks()
})

const names = (menu: ReturnType<typeof within>) => menu.getAllByRole('menuitem').map((item: HTMLElement) => item.textContent)

test.each([false, true, undefined])('pause state %s offers only the eligible pause verb', async (paused) => {
  render(<RunActions run={seed({ paused })} />)
  expect(screen.getAllByRole('button')).toEqual([screen.getByRole('button', { name: 'More' })])
  const menu = await openMore()
  expect(Boolean(menu.queryByRole('menuitem', { name: 'Pause run' }))).toBe(paused === false)
  expect(Boolean(menu.queryByRole('menuitem', { name: 'Resume run' }))).toBe(paused === true)
  expect(menu.queryByRole('menuitem', { name: 'Send a message to the agent…' })).toBeNull()
})

test.each(['fine', 'coarse'] as const)('every verb is labeled and keyboard reachable for a %s pointer', async (pointer) => {
  atViewport(390, { pointer })
  const record = seed({ paused: false, local: ['pull', 'forward.start'], members: [alice, bob] })
  render(<RunActions run={record} extra={[{ id: 'captures', label: 'Captures…', Icon: () => null, onSelect: vi.fn() }]} />)
  const more = screen.getByRole('button', { name: 'More' })
  await userEvent.tab()
  expect(document.activeElement).toBe(more)
  await userEvent.keyboard('{Enter}')
  const menu = within(await screen.findByRole('menu'))
  expect(names(menu)).toEqual([
    'Pause run', 'Forward a port…', 'Protect run', 'Pull branch', 'Hand off…', 'Captures…', 'Close run…', 'Kill run', 'Delete run',
  ])
  fireEvent.click(menu.getByRole('menuitem', { name: 'Pause run' }))
  await waitFor(() => expect(api.runPause).toHaveBeenCalledWith(record.id))
})

test.each(['Kill run', 'Delete run'])('%s waits for explicit confirmation before its RPC', async (name) => {
  const record = seed({ paused: false })
  render(<RunActions run={record} />)
  await selectMore(name)
  const dialog = within(await screen.findByRole('alertdialog'))
  expect(api.runKill).not.toHaveBeenCalled()
  expect(api.runDelete).not.toHaveBeenCalled()
  await waitFor(() => expect(document.activeElement).toBe(dialog.getByRole('button', { name: 'Cancel' })))
  fireEvent.click(dialog.getByRole('button', { name }))
  if (name === 'Kill run') {
    await waitFor(() => expect(api.runKill).toHaveBeenCalledWith(record.id))
    expect(useStore.getState().runs[record.id]?.status).toBe('abandoned')
    act(() => useStore.getState().applyRunStatus(record.id, { to: 'abandoned', reason: 'killed', status_seq: 1 }, '2020-08-14T10:00:00Z'))
    expect(useStore.getState().runs[record.id]?.stateChangedAt).toBe('2020-08-14T10:00:00Z')
  } else {
    await waitFor(() => expect(api.runDelete).toHaveBeenCalledWith(record.id))
    await waitFor(() => expect(useStore.getState().runs[record.id]).toBeUndefined())
  }
})

test.each(['{Enter}', '{Escape}'])('safe confirmation cancellation with %s restores More without mutation', async (key) => {
  render(<RunActions run={seed({ paused: false })} />)
  const more = screen.getByRole('button', { name: 'More' })
  const menu = await openMore()
  await userEvent.keyboard('{End}')
  expect(document.activeElement).toBe(menu.getByRole('menuitem', { name: 'Delete run' }))
  await userEvent.keyboard('{Enter}')
  const dialog = within(await screen.findByRole('alertdialog'))
  await waitFor(() => expect(document.activeElement).toBe(dialog.getByRole('button', { name: 'Cancel' })))
  await userEvent.keyboard(key)
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  await waitFor(() => expect(document.activeElement).toBe(more))
  expect(api.runDelete).not.toHaveBeenCalled()
})

test('a queued run remains deletable without offering Close', async () => {
  const record = seed({ run: { status: 'queued' } })
  render(<RunActions run={record} />)
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Close run…' })).toBeNull()
  fireEvent.click(menu.getByRole('menuitem', { name: 'Delete run' }))
  fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete run' }))
  await waitFor(() => expect(api.runDelete).toHaveBeenCalledWith(record.id))
})

test.each(['merged', 'abandoned', 'failed', 'interrupted'] as const)('Archive for final status %s needs no confirmation and stays reversible', async (status) => {
  const record = seed({ run: { status } })
  render(<RunActions run={record} />)
  await selectMore('Archive run')
  expect(screen.queryByRole('alertdialog')).toBeNull()
  await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith(record.id, true))
  expect(api.runDelete).not.toHaveBeenCalled()
  expect(useStore.getState().runs[record.id]).toBeDefined()
})

test('an archived run offers Restore and Reopen, not Archive', async () => {
  const record = seed({
    run: {
      status: 'merged', mode: 'tui', reason: 'closed; retained container',
      archived_at: '2020-08-14T10:00:00Z',
    },
  })
  render(<RunActions run={record} />)
  const menu = await openMore()
  expect(menu.getByRole('menuitem', { name: 'Reopen run' })).toBeTruthy()
  expect(menu.queryByRole('menuitem', { name: 'Archive run' })).toBeNull()
  fireEvent.click(menu.getByRole('menuitem', { name: 'Restore run' }))
  await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith(record.id, false))
  expect(api.runDelete).not.toHaveBeenCalled()
})

test('an old archived run is deleted only after explicit confirmation', async () => {
  const record = seed({ run: { status: 'merged', archived_at: '2020-08-14T10:00:00Z' } })
  render(<RunActions run={record} />)
  await selectMore('Delete run')
  const dialog = within(await screen.findByRole('alertdialog'))
  expect(api.runDelete).not.toHaveBeenCalled()
  expect(useStore.getState().runs[record.id]).toBeDefined()
  fireEvent.click(dialog.getByRole('button', { name: 'Delete run' }))
  await waitFor(() => expect(api.runDelete).toHaveBeenCalledWith(record.id))
  await waitFor(() => expect(useStore.getState().runs[record.id]).toBeUndefined())
  expect(api.runArchive).not.toHaveBeenCalled()
})

test('archive follows kill permission and the run.archive capability', async () => {
  const view = render(<RunActions run={seed({ run: { status: 'merged', protected: true }, members: [alice, bob], self: bob })} />)
  expect((await openMore()).queryByRole('menuitem', { name: 'Archive run' })).toBeNull()
  view.unmount()
  const record = seed({ run: { status: 'merged' } })
  useStore.setState({ capabilities: { gateway: 'remote', methods: [], ws: [] } })
  render(<RunActions run={record} />)
  expect((await openMore()).queryByRole('menuitem', { name: /^(Archive|Restore) run$/ })).toBeNull()
})

test('handoff includes eligible members only and returns focus to More', async () => {
  const record = seed({ paused: false, members: [alice, bob, vera, { ...bob, id: 'pending', pending: true }] })
  render(<RunActions run={record} />)
  const more = screen.getByRole('button', { name: 'More' })
  await selectMore('Hand off…')
  const dialog = within(await screen.findByRole('dialog'))
  expect(dialog.getAllByRole('button', { name: /^Hand off to / })).toHaveLength(1)
  fireEvent.click(dialog.getByRole('button', { name: 'Hand off to Bob' }))
  await waitFor(() => expect(api.runHandoff).toHaveBeenCalledWith(record.id, bob.id))
  await waitFor(() => expect(document.activeElement).toBe(more))
})

test('Pull requires local capability and a published commit', async () => {
  const remote = render(<RunActions run={seed({ paused: false })} />)
  expect((await openMore()).queryByRole('menuitem', { name: 'Pull branch' })).toBeNull()
  remote.unmount()
  const local = render(<RunActions run={seed({ paused: false, local: ['pull'] })} />)
  const pull = (await openMore()).getByRole('menuitem', { name: 'Pull branch' })
  expect(pull.getAttribute('aria-disabled')).toBe('true')
  local.unmount()
  const record = seed({ paused: false, local: ['pull'], run: { last_commit: 'a'.repeat(40) } })
  render(<RunActions run={record} />)
  await selectMore('Pull branch')
  await waitFor(() => expect(api.localPull).toHaveBeenCalledWith(record.id))
})

test.each([
  { status: 'merged', reason: 'closed; retained container' },
  { status: 'completed', reason: 'agent reported success; retained container' },
  { status: 'failed', reason: 'agent reported failure; retained container' },
] satisfies Partial<Run>[])('a TUI run that kept its container offers Reopen: %j', async (over) => {
  const record = seed({ run: { ...over, mode: 'tui' } })
  render(<RunActions run={record} />)
  await selectMore('Reopen run')
  await waitFor(() => expect(api.runRelaunch).toHaveBeenCalledWith(record.id))
})

test.each([
  { status: 'running' },
  { status: 'failed', mode: 'tui', reason: 'closed; retained container' },
  { status: 'merged', mode: 'headless', reason: 'closed; retained container' },
  { status: 'completed', mode: 'tui', reason: 'worker finished; retained container' },
] satisfies Partial<Run>[])('Reopen is absent when ineligible: %j', async (over) => {
  render(<RunActions run={seed({ run: over })} />)
  expect((await openMore()).queryByRole('menuitem', { name: 'Reopen run' })).toBeNull()
})

test('release confirms, keeps the run, and shows the real gateway error', async () => {
  const record = seed({ run: { status: 'merged', reason: 'closed; retained container' } })
  vi.mocked(api.runRelease).mockRejectedValueOnce(refusal('evidence is still pending'))
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', status: 'merged', reason: 'closed; retained container' }))
  render(<RunActions run={record} />)
  await selectMore('Free container…')
  fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Free container' }))
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Freed container failed: evidence is still pending'))
  expect(api.runArchive).not.toHaveBeenCalled()
  expect(useStore.getState().runs[record.id].reason).toBe('closed; retained container')
})

test('an in-flight command locks the menu, prevents duplicate RPCs, and retains pull output', async () => {
  const { promise, resolve: finish } = Promise.withResolvers<PullResult>()
  vi.mocked(api.localPull).mockReturnValueOnce(promise)
  const record = seed({ paused: false, local: ['pull'], members: [alice, bob], run: { last_commit: 'a'.repeat(40) } })
  render(<RunActions run={record} />)
  const pull = (await openMore()).getByRole('menuitem', { name: 'Pull branch' })
  act(() => {
    fireEvent.click(pull)
    fireEvent.click(pull)
  })
  await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
  const more = screen.getByRole('button', { name: 'More' })
  expect(more.getAttribute('aria-disabled')).toBe('true')
  fireEvent.keyDown(more, { key: 'Enter' })
  expect(screen.queryByRole('menu')).toBeNull()
  expect(api.localPull).toHaveBeenCalledTimes(1)
  await act(async () => finish({ branch: record.branch, ref: `refs/heads/${record.branch}`, output: 'From ssh://host\n * [new branch] run-1-checkout', current: false, dirty: false }))
  await waitFor(() => expect(toast.success).toHaveBeenCalledWith(`Pulled ${record.branch}`, expect.objectContaining({ cancel: expect.objectContaining({ label: 'Copy command' }) })))
  expect(useStore.getState().pulls[record.id]?.output).toContain('new branch')
  await waitFor(() => expect(more.getAttribute('aria-disabled')).toBeNull())
})

test('the pull toast copies the switch command and switches to the pulled branch', async () => {
  const record = seed({ paused: false, local: ['pull', 'pull.switch'], run: { last_commit: 'a'.repeat(40) } })
  vi.mocked(api.localPull).mockResolvedValueOnce({ branch: record.branch, ref: `refs/remotes/aether/${record.branch}`, output: '', current: false, dirty: false })
  render(<RunActions run={record} />)
  await selectMore('Pull branch')
  await waitFor(() => expect(toast.success).toHaveBeenCalledWith(`Pulled ${record.branch}`, expect.anything()))
  const options = vi.mocked(toast.success).mock.calls.at(-1)![1]!
  const copy = options.cancel as { label: string; onClick: () => void }
  act(() => copy.onClick())
  // jsdom has no navigator.clipboard, so the copy fails and the toast carries the command instead.
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/^Could not copy: /), { description: `git switch ${record.branch}` }))
  const action = options.action as { label: string; onClick: () => void }
  expect(action.label).toBe('Switch to it')
  act(() => action.onClick())
  await waitFor(() => expect(api.localPullSwitch).toHaveBeenCalledWith(record.id))
  await waitFor(() => expect(useStore.getState().pulls[record.id]?.current).toBe(true))
})

test('a viewer gets no mutating actions, but can use eligible local Pull', async () => {
  render(<RunActions run={seed({ paused: false, members: [alice, vera], self: vera, local: ['pull'] })} />)
  const menu = await openMore()
  expect(names(menu)).toEqual(['Pull branch'])
})

test('a collaborator can steer and kill another member run, but cannot protect or hand it off', async () => {
  render(<RunActions run={seed({ paused: false, members: [alice, bob], self: bob })} />)
  const menu = await openMore()
  for (const name of ['Pause run', 'Kill run', 'Delete run', 'Close run…']) expect(menu.getByRole('menuitem', { name })).toBeTruthy()
  expect(menu.queryByRole('menuitem', { name: 'Protect run' })).toBeNull()
  expect(menu.queryByRole('menuitem', { name: 'Hand off…' })).toBeNull()
})

test.each([{ run: { protected: true } }, { steerOthers: 'admins_only' }])('protected ownership and workspace policies still gate all actions: %j', async (policy) => {
  render(<RunActions run={seed({ paused: false, members: [alice, bob], self: bob, ...policy })} />)
  const menu = await openMore()
  for (const name of ['Pause run', 'Kill run', 'Delete run', 'Close run…', 'Hand off…', 'Protect run']) expect(menu.queryByRole('menuitem', { name })).toBeNull()
})

type State = ReturnType<typeof useStore.getState>
const runOne = (s: State) => s.runs.run_1

const verbs: {
  name: string
  confirm?: string
  /** A refusal re-reads the run. */
  slow?: boolean
  method: 'runPause' | 'runResume' | 'runKill' | 'runRelease' | 'runArchive' | 'runProtect'
  done: string
  over: Parameters<typeof seed>[0]
  read: (s: State) => unknown
  from: unknown
  to: unknown
}[] = [
  { name: 'Pause run', method: 'runPause', done: 'Paused', over: { paused: false }, read: (s) => s.pausedRuns.run_1, from: false, to: true },
  { name: 'Resume run', method: 'runResume', done: 'Resumed', over: { paused: true }, read: (s) => s.pausedRuns.run_1, from: true, to: false },
  { name: 'Kill run', confirm: 'Kill run', slow: true, method: 'runKill', done: 'Killed', over: { paused: false }, read: (s) => runOne(s).status, from: 'running', to: 'abandoned' },
  {
    name: 'Free container…', confirm: 'Free container', slow: true, method: 'runRelease', done: 'Freed container',
    over: { run: { status: 'merged', container_retained_until: '2030-01-01T00:00:00Z' } },
    read: (s) => runOne(s).container_retained_until, from: '2030-01-01T00:00:00Z', to: undefined,
  },
  { name: 'Archive run', method: 'runArchive', done: 'Archived', over: { run: { status: 'merged' } }, read: (s) => Boolean(runOne(s).archived_at), from: false, to: true },
  {
    name: 'Restore run', method: 'runArchive', done: 'Restored', over: { run: { status: 'merged', archived_at: '2020-08-14T10:00:00Z' } },
    read: (s) => Boolean(runOne(s).archived_at), from: true, to: false,
  },
  { name: 'Protect run', method: 'runProtect', done: 'Protected', over: {}, read: (s) => Boolean(runOne(s).protected), from: false, to: true },
  { name: 'Unprotect run', method: 'runProtect', done: 'Unprotected', over: { run: { protected: true } }, read: (s) => Boolean(runOne(s).protected), from: true, to: false },
]

async function choose(verb: (typeof verbs)[number]) {
  await selectMore(verb.name)
  if (verb.confirm) fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: verb.confirm }))
}

test.each(verbs)('$name shows its result before the server answers and toasts on confirm', async (verb) => {
  const { promise, resolve } = Promise.withResolvers<unknown>()
  ;(api[verb.method] as Mock).mockReturnValueOnce(promise)
  render(<RunActions run={seed(verb.over)} />)
  await choose(verb)
  await waitFor(() => expect(verb.read(useStore.getState())).toBe(verb.to))
  expect(toast.success).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'More' }).getAttribute('aria-disabled')).toBeNull()
  await act(async () => resolve({}))
  expect(toast.success).toHaveBeenCalledWith(verb.done)
  expect(verb.read(useStore.getState())).toBe(verb.to)
})

test.each(verbs)('$name reverts on refusal and shows the server error', async (verb) => {
  ;(api[verb.method] as Mock).mockRejectedValueOnce(refusal('run.verb: refused by policy'))
  if (verb.slow) vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', ...verb.over?.run }))
  render(<RunActions run={seed(verb.over)} />)
  await choose(verb)
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith(`${verb.done} failed: refused by policy`))
  expect(verb.read(useStore.getState())).toBe(verb.from)
  expect(toast.success).not.toHaveBeenCalled()
})

test('a refusal leaves a state the server sent while the verb was pending', async () => {
  const { promise, reject } = Promise.withResolvers<unknown>()
  vi.mocked(api.runKill).mockReturnValueOnce(promise)
  render(<RunActions run={seed({ paused: false })} />)
  await choose(verbs[2])
  await waitFor(() => expect(runOne(useStore.getState()).status).toBe('abandoned'))
  act(() => useStore.getState().applyRunStatus('run_1', { to: 'failed', reason: 'exited 1', status_seq: 1 }, new Date().toISOString()))
  await act(async () => reject(refusal('run is finishing')))
  expect(toast.error).toHaveBeenCalledWith('Killed failed: run is finishing')
  expect(runOne(useStore.getState()).status).toBe('failed')
})

test('an unanswered verb reverts with a lasting Retry, and a late answer dismisses it without a second toast', async () => {
  const { promise, resolve } = Promise.withResolvers<unknown>()
  vi.mocked(api.runProtect).mockReturnValueOnce(promise)
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1' }))
  vi.mocked(toast.error).mockReturnValueOnce('timeout')
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' }))
    expect(runOne(useStore.getState()).protected).toBe(true)
    act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
    await act(async () => {})
  } finally {
    vi.useRealTimers()
  }
  expect(runOne(useStore.getState()).protected).toBeFalsy()
  expect(toast.error).toHaveBeenCalledWith('Protected failed: The server did not answer in 5 seconds.', {
    duration: Infinity,
    action: { label: 'Retry', onClick: expect.any(Function) },
  })
  await act(async () => resolve({}))
  expect(runOne(useStore.getState()).protected).toBe(true)
  expect(toast.dismiss).toHaveBeenCalledWith('timeout')
  expect(toast.success).not.toHaveBeenCalled()

  const retry = vi.mocked(toast.error).mock.calls[0][1]?.action as Action
  await act(async () => retry.onClick(new MouseEvent('click') as never))
  expect(api.runProtect).toHaveBeenNthCalledWith(2, 'run_1', true)
  expect(runOne(useStore.getState()).protected).toBe(true)
  expect(toast.success).toHaveBeenCalledTimes(1)
})

test('a refusal whose re-read fails puts back a kill even when the agent wrote to the run meanwhile', async () => {
  const { promise, reject } = Promise.withResolvers<unknown>()
  vi.mocked(api.runKill).mockReturnValueOnce(promise)
  vi.mocked(api.runGet).mockRejectedValueOnce(new Error('Failed to fetch'))
  render(<RunActions run={seed({ paused: false })} />)
  await choose(verbs[2])
  await waitFor(() => expect(runOne(useStore.getState()).status).toBe('abandoned'))
  act(() => useStore.getState().applyLastCommit('run_1', 'b'.repeat(40), new Date().toISOString()))
  await act(async () => reject(refusal('run is finishing')))
  expect(toast.error).toHaveBeenCalledWith('Killed failed: run is finishing')
  expect(runOne(useStore.getState())).toMatchObject({ status: 'running', last_commit: 'b'.repeat(40) })
  expect(runOne(useStore.getState()).stateChangedAtPredicted).toBeUndefined()
})

async function confirmDelete() {
  await selectMore('Delete run')
  fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete run' }))
}

test('Delete takes the run off at once, and no re-read brings it back before the answer', async () => {
  const answer = Promise.withResolvers<unknown>()
  vi.mocked(api.runDelete).mockReturnValueOnce(answer.promise)
  render(<RunActions run={seed({ paused: false })} />)
  await confirmDelete()
  expect(runOne(useStore.getState())).toBeUndefined()
  act(() => useStore.getState().upsertRun(run({ id: 'run_1', status: 'abandoned' })))
  act(() => useStore.getState().setRuns([run({ id: 'run_1' })]))
  expect(runOne(useStore.getState())).toBeUndefined()
  expect(toast.success).not.toHaveBeenCalled()
  await act(async () => answer.resolve({}))
  expect(toast.success).toHaveBeenCalledWith('Deleted')
  expect(runOne(useStore.getState())).toBeUndefined()
  expect(useStore.getState().deletingRuns).toEqual({})
})

test('a refused delete puts the run back and shows the refusal', async () => {
  vi.mocked(api.runDelete).mockRejectedValueOnce(refusal('run.delete: run is protected'))
  render(<RunActions run={seed({ paused: false })} />)
  await confirmDelete()
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Deleted failed: run is protected'))
  expect(runOne(useStore.getState()).status).toBe('running')
  expect(useStore.getState().deletingRuns).toEqual({})
})

test('a delete refused after the server stopped the run puts the run back stopped', async () => {
  const { promise, reject } = Promise.withResolvers<unknown>()
  vi.mocked(api.runDelete).mockReturnValueOnce(promise)
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', status: 'abandoned', reason: 'killed', state: undefined, status_seq: 1 }))
  render(<RunActions run={seed({ paused: false })} />)
  await confirmDelete()
  act(() => useStore.getState().applyRunStatus('run_1', { to: 'abandoned', reason: 'killed', status_seq: 1 }, new Date().toISOString()))
  await act(async () => reject(refusal('run.delete: scheduler: delete run checkout: device or resource busy')))
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Deleted failed: delete run checkout: device or resource busy'))
  expect(runOne(useStore.getState())).toMatchObject({ status: 'abandoned', reason: 'killed', status_seq: 1 })
  expect(useStore.getState().deletingRuns).toEqual({})
})

test('a delete with no answer puts the run back as the server still holds it', async () => {
  vi.mocked(api.runDelete).mockRejectedValueOnce(new Error('Failed to fetch'))
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', title: 'renamed' }))
  render(<RunActions run={seed({ paused: false })} />)
  await confirmDelete()
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Deleted failed: Failed to fetch', expect.objectContaining({ duration: Infinity })))
  expect(runOne(useStore.getState()).title).toBe('renamed')
  expect(useStore.getState().deletingRuns).toEqual({})
})

const notFound = () => Object.assign(new Error('run.delete: run not found'), { code: codeNotFound })

test.each([
  { says: 'the delete', arrange: () => vi.mocked(api.runDelete).mockRejectedValueOnce(notFound()) },
  {
    says: 'the re-read after a lost answer',
    arrange: () => {
      vi.mocked(api.runDelete).mockRejectedValueOnce(new Error('Failed to fetch'))
      vi.mocked(api.runGet).mockRejectedValueOnce(notFound())
    },
  },
  {
    says: 'the re-read after a refusal',
    arrange: () => {
      vi.mocked(api.runDelete).mockRejectedValueOnce(refusal('run.delete: scheduler: delete run checkout: device or resource busy'))
      vi.mocked(api.runGet).mockRejectedValueOnce(notFound())
    },
  },
])('a not-found answer from $says counts as deleted', async ({ arrange }) => {
  arrange()
  render(<RunActions run={seed({ paused: false })} />)
  await confirmDelete()
  await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Deleted'))
  expect(toast.error).not.toHaveBeenCalled()
  expect(runOne(useStore.getState())).toBeUndefined()
  expect(useStore.getState().deletingRuns).toEqual({})
  expect(useStore.getState().predicting.run_1).toBeUndefined()
})

async function pastDeadline(press: () => void) {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    press()
    act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
    await act(async () => {})
  } finally {
    vi.useRealTimers()
  }
  await act(async () => {})
}

test.each([
  { reread: 'shows the prediction', read: () => Promise.resolve(run({ id: 'run_1', protected: true })), protect: true },
  { reread: 'fails', read: () => Promise.reject(new Error('Failed to fetch')), protect: false },
])('an unanswered verb whose re-read $reread', async ({ read, protect }) => {
  vi.mocked(api.runProtect).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runGet).mockImplementationOnce(read)
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  expect(api.runGet).toHaveBeenCalledWith('run_1')
  expect(Boolean(runOne(useStore.getState()).protected)).toBe(protect)
  if (protect) {
    expect(toast.success).toHaveBeenCalledWith('Protected')
    expect(toast.error).not.toHaveBeenCalled()
  } else {
    expect(toast.error).toHaveBeenCalledWith('Protected failed: The server did not answer in 5 seconds.', expect.anything())
  }
})

test('a deadline re-read answered after a newer event keeps the event and still confirms the press', async () => {
  const read = Promise.withResolvers<Run>()
  vi.mocked(api.runProtect).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runGet).mockReturnValueOnce(read.promise)
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  act(() => useStore.getState().applyRunTitle('run_1', 'renamed'))
  await act(async () => read.resolve(run({ id: 'run_1', protected: true })))
  expect(runOne(useStore.getState())).toMatchObject({ title: 'renamed', protected: true })
  expect(toast.success).toHaveBeenCalledWith('Protected')
})

test('a deadline re-read answered after a newer event keeps the event and reverts a press the server lacks', async () => {
  const read = Promise.withResolvers<Run>()
  vi.mocked(api.runProtect).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runGet).mockReturnValueOnce(read.promise)
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  act(() => useStore.getState().applyRunTitle('run_1', 'renamed'))
  await act(async () => read.resolve(run({ id: 'run_1', protected: false })))
  expect(runOne(useStore.getState())).toMatchObject({ title: 'renamed' })
  expect(runOne(useStore.getState()).protected).toBeFalsy()
  expect(toast.success).not.toHaveBeenCalled()
  expect(toast.error).toHaveBeenCalledWith('Protected failed: The server did not answer in 5 seconds.', expect.objectContaining({
    action: { label: 'Retry', onClick: expect.any(Function) },
  }))
})

test('an unanswered unprotect counts a re-read that omits protected as applied', async () => {
  vi.mocked(api.runProtect).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1' }))
  render(<RunActions run={seed({ run: { protected: true } })} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Unprotect run' })))
  expect(runOne(useStore.getState()).protected).toBeFalsy()
  expect(toast.success).toHaveBeenCalledWith('Unprotected')
  expect(toast.error).not.toHaveBeenCalled()
})

test('a retry never reverts what the first attempt\'s late success confirmed', async () => {
  const first = Promise.withResolvers<unknown>()
  vi.mocked(api.runProtect).mockReturnValueOnce(first.promise).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1' })).mockRejectedValueOnce(new Error('Failed to fetch'))
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  expect(runOne(useStore.getState()).protected).toBeFalsy()
  const retry = vi.mocked(toast.error).mock.calls[0][1]?.action as Action
  await pastDeadline(() => {
    retry.onClick(new MouseEvent('click') as never)
    expect(runOne(useStore.getState()).protected).toBe(true)
    first.resolve({})
  })
  expect(api.runProtect).toHaveBeenCalledTimes(2)
  expect(runOne(useStore.getState()).protected).toBe(true)
})

test('a verb\'s late answer leaves the lock of the verb pressed after it', async () => {
  const protect = Promise.withResolvers<unknown>()
  vi.mocked(api.runProtect).mockReturnValueOnce(protect.promise)
  vi.mocked(api.runPause).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1' }))
  seed({ paused: false })
  render(<RunActions run={runOne(useStore.getState())} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  expect(useStore.getState().predicting.run_1).toBeUndefined()
  await selectMore('Pause run')
  await waitFor(() => expect(useStore.getState().pausedRuns.run_1).toBe(true))
  await act(async () => protect.resolve({}))
  expect(useStore.getState().predicting.run_1).toBeDefined()
})

test('an open More menu offers predicted verbs again once the pending one is answered', async () => {
  const { promise, resolve } = Promise.withResolvers<unknown>()
  vi.mocked(api.runProtect).mockReturnValueOnce(promise)
  render(<RunActions run={seed({ paused: false })} />)
  await selectMore('Protect run')
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Pause run' })).toBeNull()
  await act(async () => resolve({}))
  expect(menu.getByRole('menuitem', { name: 'Pause run' })).toBeTruthy()
})

test('a status event keeps the estimated change time of a run no verb predicted', () => {
  const record = seed({ run: { status: 'running', status_changed_at: undefined } })
  expect(record.stateChangedAtEstimated).toBe(true)
  act(() => useStore.getState().applyRunStatus('run_1', { to: 'running', state: 'working', status_seq: 1 }, '2030-01-01T00:00:00Z'))
  expect(runOne(useStore.getState()).stateChangedAt).toBe(record.stateChangedAt)
})

test('a pending kill offers no predicted verb until it is answered', async () => {
  const { promise, resolve } = Promise.withResolvers<unknown>()
  vi.mocked(api.runKill).mockReturnValueOnce(promise)
  seed({ paused: false })
  const Live = () => <RunActions run={useStore((s) => s.runs.run_1)} />
  render(<Live />)
  await choose(verbs[2])
  await waitFor(() => expect(runOne(useStore.getState()).status).toBe('abandoned'))
  expect(names(await openMore())).toEqual(['Close run…', 'Delete run'])
  await userEvent.keyboard('{Escape}')
  await act(async () => resolve({}))
  expect((await openMore()).getByRole('menuitem', { name: 'Archive run' })).toBeTruthy()
})

test.each([
  { paused: true, toast: 'success' },
  { paused: false, toast: 'error' },
] as const)('an invalid-state refusal applies the re-read run, paused $paused', async ({ paused, toast: kind }) => {
  vi.mocked(api.runPause).mockRejectedValueOnce(Object.assign(new Error('run.pause: run is already paused'), { code: codeInvalidState }))
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1', paused }))
  render(<RunActions run={seed({ paused: false })} />)
  await selectMore('Pause run')
  await waitFor(() => expect(toast[kind]).toHaveBeenCalled())
  expect(api.runGet).toHaveBeenCalledWith('run_1')
  expect(useStore.getState().pausedRuns.run_1).toBe(paused)
  if (kind === 'success') expect(toast.error).not.toHaveBeenCalled()
  else expect(toast.error).toHaveBeenCalledWith('Paused failed: run is already paused')
})

async function confirmPastDeadline(verb: (typeof verbs)[number]) {
  await selectMore(verb.name)
  const button = within(await screen.findByRole('alertdialog')).getByRole('button', { name: verb.confirm })
  await pastDeadline(() => fireEvent.click(button))
}

test.each([verbs[2], verbs[3]])('$name waits past the deadline and confirms a late success silently', async (verb) => {
  const { promise, resolve } = Promise.withResolvers<unknown>()
  ;(api[verb.method] as Mock).mockReturnValueOnce(promise)
  render(<RunActions run={seed(verb.over)} />)
  await confirmPastDeadline(verb)
  expect(verb.read(useStore.getState())).toBe(verb.to)
  expect(api.runGet).not.toHaveBeenCalled()
  expect(toast.error).not.toHaveBeenCalled()
  expect((await openMore()).getByRole('menuitem', { name: 'Still waiting for the server…' })).toBeTruthy()
  await userEvent.keyboard('{Escape}')
  await act(async () => resolve({}))
  expect(verb.read(useStore.getState())).toBe(verb.to)
  expect(useStore.getState().predicting.run_1).toBeUndefined()
  expect(toast.success).not.toHaveBeenCalled()
  expect(toast.error).not.toHaveBeenCalled()
})

test('a kill refused past the deadline is put back and shows the refusal', async () => {
  const { promise, reject } = Promise.withResolvers<unknown>()
  vi.mocked(api.runKill).mockReturnValueOnce(promise)
  render(<RunActions run={seed({ paused: false })} />)
  await confirmPastDeadline(verbs[2])
  await act(async () => reject(refusal('run is finishing')))
  expect(runOne(useStore.getState()).status).toBe('running')
  expect(toast.error).toHaveBeenCalledWith('Killed failed: run is finishing', { id: undefined, action: undefined, duration: undefined })
  expect(useStore.getState().predicting.run_1).toBeUndefined()
})

test('a kill that fails with no answer past the deadline shows the real error', async () => {
  const { promise, reject } = Promise.withResolvers<unknown>()
  vi.mocked(api.runKill).mockReturnValueOnce(promise)
  vi.mocked(api.runGet).mockResolvedValueOnce(run({ id: 'run_1' }))
  render(<RunActions run={seed({ paused: false })} />)
  await confirmPastDeadline(verbs[2])
  await act(async () => reject(new Error('Failed to fetch')))
  expect(runOne(useStore.getState()).status).toBe('running')
  expect(toast.error).toHaveBeenCalledWith('Killed failed: Failed to fetch', {
    duration: Infinity,
    action: { label: 'Retry', onClick: expect.any(Function) },
  })
})

test('an invalid-state refusal still being checked at the deadline reports once it is checked', async () => {
  const read = Promise.withResolvers<Run>()
  vi.mocked(api.runPause).mockRejectedValueOnce(Object.assign(new Error('run.pause: run is already paused'), { code: codeInvalidState }))
  vi.mocked(api.runGet).mockReturnValueOnce(read.promise)
  render(<RunActions run={seed({ paused: false })} />)
  const menu = await openMore()
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    fireEvent.click(menu.getByRole('menuitem', { name: 'Pause run' }))
    await act(async () => {})
    expect(api.runGet).toHaveBeenCalledTimes(1)
    act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
    await act(async () => {})
  } finally {
    vi.useRealTimers()
  }
  expect(api.runGet).toHaveBeenCalledTimes(1)
  await act(async () => read.resolve(run({ id: 'run_1', paused: false })))
  expect(toast.error).toHaveBeenCalledTimes(1)
  expect(toast.error).toHaveBeenCalledWith('Paused failed: run is already paused', expect.objectContaining({ action: undefined }))
  expect(useStore.getState().predicting.run_1).toBeUndefined()
})

test('ignores an invalid-state re-read that answers after the deadline re-read confirmed the press', async () => {
  const pause = Promise.withResolvers<unknown>()
  const deadlineRead = Promise.withResolvers<Run>()
  const refusalRead = Promise.withResolvers<Run>()
  vi.mocked(api.runPause).mockReturnValueOnce(pause.promise)
  vi.mocked(api.runGet).mockReturnValueOnce(deadlineRead.promise).mockReturnValueOnce(refusalRead.promise)
  render(<RunActions run={seed({ paused: false })} />)
  const menu = await openMore()
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    fireEvent.click(menu.getByRole('menuitem', { name: 'Pause run' }))
    await act(async () => {})
    act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
    await act(async () => {})
  } finally {
    vi.useRealTimers()
  }
  await act(async () => pause.reject(Object.assign(new Error('run.pause: run is already paused'), { code: codeInvalidState })))
  expect(api.runGet).toHaveBeenCalledTimes(2)
  await act(async () => deadlineRead.resolve(run({ id: 'run_1', paused: true })))
  act(() => useStore.getState().setPaused('run_1', false))
  await act(async () => refusalRead.resolve(run({ id: 'run_1', paused: true })))

  expect(useStore.getState().pausedRuns.run_1).toBe(false)
})

test('an invalid-state refusal whose re-read never answers reports the refusal and frees the run', async () => {
  vi.mocked(api.runPause).mockRejectedValueOnce(Object.assign(new Error('run.pause: run is already paused'), { code: codeInvalidState }))
  vi.mocked(api.runGet).mockReturnValueOnce(new Promise(() => {}))
  render(<RunActions run={seed({ paused: false })} />)
  const menu = await openMore()
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    fireEvent.click(menu.getByRole('menuitem', { name: 'Pause run' }))
    await act(async () => {})
    act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
    await act(async () => {})
  } finally {
    vi.useRealTimers()
  }
  expect(toast.error).toHaveBeenCalledWith('Paused failed: run is already paused', expect.objectContaining({ action: undefined }))
  expect(useStore.getState().predicting.run_1).toBeUndefined()
})

test('a re-read served before the commit never overrides the success that answered first', async () => {
  const answer = Promise.withResolvers<unknown>()
  const read = Promise.withResolvers<Run>()
  vi.mocked(api.runProtect).mockReturnValueOnce(answer.promise)
  vi.mocked(api.runGet).mockReturnValueOnce(read.promise)
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  await act(async () => answer.resolve({}))
  await act(async () => read.resolve(run({ id: 'run_1', protected: false })))
  expect(runOne(useStore.getState()).protected).toBe(true)
  expect(toast.success).toHaveBeenCalledWith('Protected')
  expect(toast.error).not.toHaveBeenCalled()
})

test('Retry waits while another verb is pending on the run', async () => {
  vi.mocked(api.runProtect).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runPause).mockReturnValueOnce(new Promise(() => {}))
  vi.mocked(api.runGet).mockRejectedValueOnce(new Error('Failed to fetch'))
  render(<RunActions run={seed({ paused: false })} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  const retry = vi.mocked(toast.error).mock.calls[0][1]?.action as Action
  await selectMore('Pause run')
  const marker = useStore.getState().predicting.run_1
  expect(marker).toBeDefined()

  act(() => retry.onClick(new MouseEvent('click') as never))
  expect(api.runProtect).toHaveBeenCalledTimes(1)
  expect(toast.error).toHaveBeenLastCalledWith('Protected not retried: another action on this run is still waiting for the server.')
  expect(useStore.getState().predicting.run_1).toBe(marker)
})

test('a late refusal of an earlier attempt is dropped once a retry succeeded', async () => {
  const first = Promise.withResolvers<unknown>()
  vi.mocked(api.runProtect).mockReturnValueOnce(first.promise).mockResolvedValueOnce({})
  vi.mocked(api.runGet).mockRejectedValueOnce(new Error('Failed to fetch'))
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  const retry = vi.mocked(toast.error).mock.calls[0][1]?.action as Action
  await act(async () => retry.onClick(new MouseEvent('click') as never))
  expect(toast.success).toHaveBeenCalledWith('Protected')
  await act(async () => first.reject(refusal('run is protected by policy')))
  expect(toast.error).toHaveBeenCalledTimes(1)
  expect(runOne(useStore.getState()).protected).toBe(true)
})

test('a sign-in switch dismisses an unanswered verb\'s Retry and drops its late answer', async () => {
  const first = Promise.withResolvers<unknown>()
  vi.mocked(api.runProtect).mockReturnValueOnce(first.promise)
  vi.mocked(api.runGet).mockRejectedValueOnce(new Error('Failed to fetch'))
  vi.mocked(toast.error).mockReturnValueOnce('timeout')
  render(<RunActions run={seed()} />)
  const menu = await openMore()
  await pastDeadline(() => fireEvent.click(menu.getByRole('menuitem', { name: 'Protect run' })))
  const retry = vi.mocked(toast.error).mock.calls[0][1]?.action as Action
  act(() => useStore.setState({ identityKey: crypto.randomUUID() }))
  expect(toast.dismiss).toHaveBeenCalledWith('timeout')
  act(() => retry.onClick(new MouseEvent('click') as never))
  expect(api.runProtect).toHaveBeenCalledTimes(1)
  await act(async () => first.resolve({}))
  expect(runOne(useStore.getState()).protected).toBeFalsy()
  expect(useStore.getState().predicting.run_1).toBeUndefined()
  expect(toast.success).not.toHaveBeenCalled()
})

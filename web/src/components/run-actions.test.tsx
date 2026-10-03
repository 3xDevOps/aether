import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { RunActions } from '@/components/run-actions'
import { api } from '@/lib/api'
import type { GatewayCapabilities, Member, PullResult, Run } from '@/lib/types'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import type { RunRecord } from '@/store/runs'
import { alice, bob, run, serverInfo, vera, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async () => {
  // Vitest hoists this factory before static fixture imports initialize.
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

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

test.each([false, true, undefined])('pause state %s exposes only the eligible primary verb', (paused) => {
  render(<RunActions run={seed({ paused })} />)
  expect(Boolean(screen.queryByRole('button', { name: 'Pause' }))).toBe(paused === false)
  expect(Boolean(screen.queryByRole('button', { name: 'Resume' }))).toBe(paused === true)
  expect(screen.getByRole('button', { name: 'Message' })).toBeTruthy()
  expect(screen.getAllByRole('button')).toHaveLength(paused === undefined ? 2 : 3)
})

test.each(['fine', 'coarse'] as const)('active actions remain labeled and keyboard reachable for a %s pointer', async (pointer) => {
  atViewport(390, { pointer })
  const record = seed({ paused: false, local: ['pull', 'forward.start'], members: [alice, bob] })
  render(<RunActions run={record} />)
  const message = screen.getByRole('button', { name: 'Message' })
  const pause = screen.getByRole('button', { name: 'Pause' })
  const more = screen.getByRole('button', { name: 'More' })
  await userEvent.tab()
  expect(document.activeElement).toBe(message)
  await userEvent.tab()
  expect(document.activeElement).toBe(pause)
  await userEvent.keyboard('{Enter}')
  await waitFor(() => expect(api.runPause).toHaveBeenCalledWith(record.id))
  await waitFor(() => expect(more.getAttribute('aria-disabled')).toBeNull())
  await userEvent.tab()
  expect(document.activeElement).toBe(more)
  await userEvent.keyboard('{Enter}')
  const menu = within(await screen.findByRole('menu'))
  for (const name of ['Close run...', 'Forward a port...', 'Protect run', 'Pull branch', 'Hand off', 'Kill run', 'Delete run']) {
    expect(menu.getByRole('menuitem', { name })).toBeTruthy()
  }
  expect(menu.queryByRole('menuitem', { name: 'Pause run' })).toBeNull()
  expect(menu.queryByRole('menuitem', { name: 'Send a message to the agent...' })).toBeNull()
  const items = menu.getAllByRole('menuitem')
  expect(items.slice(-2).map((item) => item.textContent)).toEqual(['Kill run', 'Delete run'])
  await userEvent.keyboard('{Escape}')
  await waitFor(() => expect(document.activeElement).toBe(more))
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
    // Lifecycle events, not the action click, move a killed run.
    expect(useStore.getState().runs[record.id]?.status).toBe('running')
  } else {
    await waitFor(() => expect(api.runDelete).toHaveBeenCalledWith(record.id))
    await waitFor(() => expect(useStore.getState().runs[record.id]).toBeUndefined())
  }
})

test.each(['{Enter}', '{Escape}'])('safe confirmation cancellation with %s restores More without mutation', async (key) => {
  render(<RunActions run={seed({ paused: false })} />)
  const more = screen.getByRole('button', { name: 'More' })
  const menu = await openMore()
  // End selects the last (destructive) item; Enter opens, but cannot confirm it.
  await userEvent.keyboard('{End}')
  expect(document.activeElement).toBe(menu.getByRole('menuitem', { name: 'Delete run' }))
  await userEvent.keyboard('{Enter}')
  const dialog = within(await screen.findByRole('alertdialog'))
  await waitFor(() => expect(document.activeElement).toBe(dialog.getByRole('button', { name: 'Cancel' })))
  await userEvent.keyboard(key)
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  await waitFor(() => expect(document.activeElement).toBe(more))
  expect(api.runDelete).not.toHaveBeenCalled()
  expect(api.runKill).not.toHaveBeenCalled()
})

test('a queued run remains deletable without offering Close', async () => {
  const record = seed({ run: { status: 'queued' } })
  render(<RunActions run={record} />)
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Close run...' })).toBeNull()
  fireEvent.click(menu.getByRole('menuitem', { name: 'Delete run' }))
  const dialog = within(await screen.findByRole('alertdialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Delete run' }))
  await waitFor(() => expect(api.runDelete).toHaveBeenCalledWith(record.id))
})

test('completed runs promote Close and eligible Relaunch, not Archive', async () => {
  const record = seed({ run: { status: 'completed', mode: 'tui', reason: 'closed; retained container' } })
  render(<RunActions run={record} />)
  expect(screen.getByRole('button', { name: 'Close' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Relaunch' })).toBeTruthy()
  expect(screen.getAllByRole('button')).toHaveLength(3)
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Archive run' })).toBeNull()
  expect(menu.queryByRole('menuitem', { name: 'Close run...' })).toBeNull()
})

test.each(['merged', 'abandoned', 'failed', 'interrupted'] as const)('Archive is primary for final status %s and remains reversible', async (status) => {
  const record = seed({ run: { status } })
  render(<RunActions run={record} />)
  fireEvent.click(screen.getByRole('button', { name: 'Archive' }))
  expect(screen.queryByRole('alertdialog')).toBeNull()
  await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith(record.id, true))
  expect(useStore.getState().runs[record.id]?.archived_at).toBeUndefined()
})

test('archived runs promote Restore and eligible Relaunch', async () => {
  const record = seed({
    run: {
      status: 'merged', mode: 'tui', reason: 'closed; retained container',
      archived_at: '2026-08-14T10:00:00Z', deletes_at: '2026-08-28T10:00:00Z',
    },
  })
  render(<RunActions run={record} />)
  expect(screen.getByRole('button', { name: 'Relaunch' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Archive' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Restore' }))
  await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith(record.id, false))
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Archive run' })).toBeNull()
})

test('archive follows kill permission', async () => {
  render(<RunActions run={seed({ run: { status: 'merged', protected: true }, members: [alice, bob], self: bob })} />)
  expect(screen.queryByRole('button', { name: 'Archive' })).toBeNull()
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Archive run' })).toBeNull()
})

test.each([undefined, '2026-08-14T10:00:00Z'])('a gateway without run.archive offers neither archive verb (archived %s)', async (archived_at) => {
  const record = seed({ run: { status: 'merged', archived_at } })
  useStore.setState({ capabilities: { gateway: 'remote', methods: [], ws: [] } })
  render(<RunActions run={record} />)
  expect(screen.queryByRole('button', { name: 'Archive' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Restore' })).toBeNull()
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: /^(Archive|Restore) run$/ })).toBeNull()
})

test('handoff includes eligible members only and returns focus to More', async () => {
  const record = seed({ paused: false, members: [alice, bob, vera, { ...bob, id: 'pending', pending: true }] })
  render(<RunActions run={record} />)
  const more = screen.getByRole('button', { name: 'More' })
  await selectMore('Hand off')
  const dialog = within(await screen.findByRole('dialog'))
  expect(dialog.getAllByRole('button', { name: /^Hand off to / })).toHaveLength(1)
  fireEvent.click(dialog.getByRole('button', { name: 'Hand off to Bob' }))
  await waitFor(() => expect(api.runHandoff).toHaveBeenCalledWith(record.id, bob.id))
  await waitFor(() => expect(document.activeElement).toBe(more))
})

test('a run with nobody eligible to hand to has no handoff action', async () => {
  render(<RunActions run={seed({ paused: false, members: [alice, vera] })} />)
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Hand off' })).toBeNull()
})

test('Pull requires local capability and a published commit', async () => {
  const remote = render(<RunActions run={seed({ paused: false })} />)
  expect((await openMore()).queryByRole('menuitem', { name: 'Pull branch' })).toBeNull()
  remote.unmount()
  const local = render(<RunActions run={seed({ paused: false, local: ['pull'] })} />)
  const pull = (await openMore()).getByRole('menuitem', { name: 'Pull branch' })
  expect(pull.getAttribute('aria-disabled')).toBe('true')
  fireEvent.click(pull)
  expect(api.localPull).not.toHaveBeenCalled()
  local.unmount()
  const record = seed({ paused: false, local: ['pull'], run: { last_commit: 'a'.repeat(40) } })
  render(<RunActions run={record} />)
  await selectMore('Pull branch')
  await waitFor(() => expect(api.localPull).toHaveBeenCalledWith(record.id))
})

test('a retained TUI close promotes Relaunch alongside Archive', async () => {
  const record = seed({ run: { status: 'merged', mode: 'tui', reason: 'closed; retained container' } })
  render(<RunActions run={record} />)
  expect(screen.getByRole('button', { name: 'Archive' })).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Relaunch' }))
  await waitFor(() => expect(api.runRelaunch).toHaveBeenCalledWith(record.id))
  const menu = await openMore()
  expect(menu.queryByRole('menuitem', { name: 'Kill run' })).toBeNull()
  expect(menu.getByRole('menuitem', { name: 'Delete run' })).toBeTruthy()
})

test.each([
  { status: 'completed', reason: 'agent reported success; retained container' },
  { status: 'failed', reason: 'agent reported failure; retained container' },
] satisfies Partial<Run>[])('an agent report that kept its container offers Relaunch: %j', async (over) => {
  const record = seed({ run: { ...over, mode: 'tui' } })
  render(<RunActions run={record} />)
  fireEvent.click(screen.getByRole('button', { name: 'Relaunch' }))
  await waitFor(() => expect(api.runRelaunch).toHaveBeenCalledWith(record.id))
})

test.each([
  { status: 'merged', reason: 'closed; retained container', mode: 'tui' },
  { status: 'completed', reason: 'worker finished; retained container', mode: 'headless' },
  { status: 'failed', reason: 'agent reported failure; retained container', mode: 'tui' },
] satisfies Partial<Run>[])('releases retained resources without archiving or removing a %j run', async (over) => {
  const record = seed({ run: over })
  render(<RunActions run={record} />)
  fireEvent.click(screen.getByRole('button', { name: 'Release' }))
  const dialog = within(await screen.findByRole('alertdialog'))
  expect(api.runRelease).not.toHaveBeenCalled()
  fireEvent.click(dialog.getByRole('button', { name: 'Release resources' }))
  await waitFor(() => expect(api.runRelease).toHaveBeenCalledWith(record.id))
  expect(api.runArchive).not.toHaveBeenCalled()
  expect(useStore.getState().runs[record.id]).toBeDefined()
})

test('release failure shows the real gateway error without changing the run', async () => {
  vi.mocked(api.runRelease).mockRejectedValueOnce(new Error('evidence is still pending'))
  const record = seed({ run: { status: 'merged', reason: 'closed; retained container' } })
  render(<RunActions run={record} />)
  fireEvent.click(screen.getByRole('button', { name: 'Release' }))
  fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Release resources' }))
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Released resources failed: evidence is still pending'))
  expect(useStore.getState().runs[record.id].reason).toBe('closed; retained container')
})

test.each([
  { status: 'needs-attention', reason: 'closed; retained container' },
  { status: 'merged', reason: 'retained container expired' },
  { status: 'merged', reason: 'retained container unavailable' },
  { status: 'failed', reason: 'closed; retained container' },
] satisfies Partial<Run>[])('release is absent without a finished retained resource: %j', async (over) => {
  render(<RunActions run={seed({ run: over })} />)
  expect(screen.queryByRole('button', { name: 'Release' })).toBeNull()
  expect((await openMore()).queryByRole('menuitem', { name: 'Release resources...' })).toBeNull()
})

test('release requires Kill permission and the run.release capability', async () => {
  const record = seed({
    run: { status: 'merged', reason: 'closed; retained container', protected: true },
    members: [alice, bob],
    self: bob,
  })
  const view = render(<RunActions run={record} />)
  expect(screen.queryByRole('button', { name: 'Release' })).toBeNull()
  view.unmount()
  const own = seed({ run: { status: 'merged', reason: 'closed; retained container' } })
  useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.archive'], ws: [] } })
  render(<RunActions run={own} />)
  expect(screen.queryByRole('button', { name: 'Release' })).toBeNull()
})

test.each([
  { status: 'running' },
  { status: 'failed', mode: 'tui', reason: 'closed; retained container' },
  { status: 'interrupted', mode: 'tui', reason: 'closed; retained container' },
  { status: 'merged', mode: 'tui', reason: 'retained container expired' },
  { status: 'merged', mode: 'headless', reason: 'closed; retained container' },
  { status: 'completed', mode: 'tui', reason: 'agent reported success' },
  { status: 'completed', mode: 'tui', reason: 'worker finished; retained container' },
] satisfies Partial<Run>[])('Relaunch is absent when ineligible: %j', async (over) => {
  render(<RunActions run={seed({ run: over })} />)
  expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
  expect((await openMore()).queryByRole('menuitem', { name: 'Relaunch run' })).toBeNull()
})

test('a refused verb reports the server error and unlocks the bar', async () => {
  vi.mocked(api.runKill).mockRejectedValueOnce(new Error('run is protected: only its owner or an admin may kill'))
  render(<RunActions run={seed({ paused: false })} />)
  await selectMore('Kill run')
  const dialog = within(await screen.findByRole('alertdialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Kill run' }))
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Killed failed: run is protected: only its owner or an admin may kill'))
  expect(screen.getByRole('button', { name: 'Pause' }).getAttribute('aria-disabled')).toBeNull()
  expect(screen.getByRole('button', { name: 'More' }).getAttribute('aria-disabled')).toBeNull()
})

test('an in-flight menu command locks every action, prevents duplicate RPCs, and retains pull output', async () => {
  const { promise, resolve: finish } = Promise.withResolvers<PullResult>()
  vi.mocked(api.localPull).mockReturnValueOnce(promise)
  const record = seed({ paused: false, local: ['pull'], members: [alice, bob], run: { last_commit: 'a'.repeat(40) } })
  render(<RunActions run={record} />)
  const menu = await openMore()
  const pull = menu.getByRole('menuitem', { name: 'Pull branch' })
  act(() => {
    fireEvent.click(pull)
    fireEvent.click(pull)
  })
  await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
  const more = screen.getByRole('button', { name: 'More' })
  expect(more.getAttribute('aria-disabled')).toBe('true')
  expect(more.querySelector('.animate-spin')).toBeTruthy()
  for (const name of ['Message', 'Pause']) {
    const button = screen.getByRole('button', { name })
    expect(button.getAttribute('aria-disabled')).toBe('true')
    fireEvent.click(button)
  }
  fireEvent.keyDown(more, { key: 'Enter' })
  expect(screen.queryByRole('menu')).toBeNull()
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(api.runPause).not.toHaveBeenCalled()
  expect(useStore.getState().paletteDialog).toBeNull()
  expect(api.localPull).toHaveBeenCalledTimes(1)
  await act(async () => finish({ branch: record.branch, ref: `refs/heads/${record.branch}`, output: 'From ssh://host\n * [new branch] run-1-checkout', current: false, dirty: false }))
  await waitFor(() => expect(toast.success).toHaveBeenCalledWith(`Pulled refs/heads/${record.branch}`))
  expect(useStore.getState().pulls[record.id]?.output).toContain('new branch')
  await waitFor(() => expect(more.getAttribute('aria-disabled')).toBeNull())
  expect((await openMore()).getByRole('menuitem', { name: 'Pull branch' }).getAttribute('aria-disabled')).not.toBe('true')
})

test('an in-flight primary command cannot run twice', async () => {
  const { promise, resolve: finish } = Promise.withResolvers<unknown>()
  vi.mocked(api.runPause).mockReturnValueOnce(promise)
  render(<RunActions run={seed({ paused: false })} />)
  const pause = screen.getByRole('button', { name: 'Pause' })
  act(() => {
    fireEvent.click(pause)
    fireEvent.click(pause)
  })
  expect(api.runPause).toHaveBeenCalledTimes(1)
  expect(pause.querySelector('.animate-spin')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'More' }).getAttribute('aria-disabled')).toBe('true')
  await act(async () => finish({}))
  await waitFor(() => expect(pause.getAttribute('aria-disabled')).toBeNull())
})

test('a viewer gets no mutating actions, but can use eligible local Pull', async () => {
  render(<RunActions run={seed({ paused: false, members: [alice, vera], self: vera, local: ['pull'] })} />)
  expect(screen.getAllByRole('button')).toEqual([screen.getByRole('button', { name: 'More' })])
  const menu = await openMore()
  expect(menu.getAllByRole('menuitem')).toEqual([menu.getByRole('menuitem', { name: 'Pull branch' })])
})

test('a collaborator can steer and kill another member run, but cannot protect or hand it off', async () => {
  render(<RunActions run={seed({ paused: false, members: [alice, bob], self: bob })} />)
  expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Message' })).toBeTruthy()
  const menu = await openMore()
  for (const name of ['Kill run', 'Delete run', 'Close run...']) expect(menu.getByRole('menuitem', { name })).toBeTruthy()
  expect(menu.queryByRole('menuitem', { name: 'Protect run' })).toBeNull()
  expect(menu.queryByRole('menuitem', { name: 'Hand off' })).toBeNull()
})

test('an admin keeps eligible actions on a protected run in an admins-only workspace', async () => {
  const record = seed({ paused: false, members: [bob, alice], self: alice, run: { member_id: bob.id, protected: true }, steerOthers: 'admins_only' })
  render(<RunActions run={record} />)
  expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy()
  const menu = await openMore()
  for (const name of ['Kill run', 'Delete run', 'Unprotect run', 'Hand off']) expect(menu.getByRole('menuitem', { name })).toBeTruthy()
  fireEvent.click(menu.getByRole('menuitem', { name: 'Unprotect run' }))
  await waitFor(() => expect(api.runProtect).toHaveBeenCalledWith(record.id, false))
})

test.each([{ run: { protected: true } }, { steerOthers: 'admins_only' }])('protected ownership and workspace policies still gate all actions: %j', async (policy) => {
  render(<RunActions run={seed({ paused: false, members: [alice, bob], self: bob, ...policy })} />)
  expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Message' })).toBeNull()
  const menu = await openMore()
  for (const name of ['Kill run', 'Delete run', 'Close run...', 'Hand off', 'Protect run']) expect(menu.queryByRole('menuitem', { name })).toBeNull()
})

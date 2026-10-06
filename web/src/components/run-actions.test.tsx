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
})

test('an archived run offers Restore and Reopen, not Archive', async () => {
  const record = seed({
    run: {
      status: 'merged', mode: 'tui', reason: 'closed; retained container',
      archived_at: '2026-08-14T10:00:00Z', deletes_at: '2026-08-28T10:00:00Z',
    },
  })
  render(<RunActions run={record} />)
  const menu = await openMore()
  expect(menu.getByRole('menuitem', { name: 'Reopen run' })).toBeTruthy()
  expect(menu.queryByRole('menuitem', { name: 'Archive run' })).toBeNull()
  fireEvent.click(menu.getByRole('menuitem', { name: 'Restore run' }))
  await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith(record.id, false))
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
  vi.mocked(api.runRelease).mockRejectedValueOnce(new Error('evidence is still pending'))
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
  await waitFor(() => expect(toast.success).toHaveBeenCalledWith(`Pulled refs/heads/${record.branch}`))
  expect(useStore.getState().pulls[record.id]?.output).toContain('new branch')
  await waitFor(() => expect(more.getAttribute('aria-disabled')).toBeNull())
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

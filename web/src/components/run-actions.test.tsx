import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { RunActions } from '@/components/run-actions'
import { api } from '@/lib/api'
import type { GatewayCapabilities, Run } from '@/lib/types'
import { useStore } from '@/store'
import { toRecord, type RunRecord } from '@/store/runs'
import { alice, bob, run, vera, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
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
    members?: typeof alice[]
  } = {},
): RunRecord {
  const record = toRecord(run({ id: 'run_1', ...over.run }))
  const members = over.members ?? [alice]
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: Object.fromEntries(members.map((m) => [m.id, m])),
    runs: { [record.id]: record },
    pausedRuns: over.paused === undefined ? {} : { [record.id]: over.paused },
    capabilities: over.local ? { ...every, local: over.local } : every,
    hydrated: true,
  })
  return record
}

beforeEach(() => {
  vi.clearAllMocks()
})

// The tri-state: hydration seeds the pause state from the run list, and a
// gateway that sends none leaves it unknown. Offering neither verb beats
// offering the one the server would refuse.
test('pause, resume and neither follow the known pause state', () => {
  const { unmount } = render(<RunActions run={seed({ paused: false })} />)
  expect(screen.getByRole('button', { name: 'Pause run' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Resume run' })).toBeNull()
  unmount()

  const resumed = render(<RunActions run={seed({ paused: true })} />)
  expect(screen.getByRole('button', { name: 'Resume run' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Pause run' })).toBeNull()
  resumed.unmount()

  render(<RunActions run={seed()} />)
  expect(screen.queryByRole('button', { name: 'Pause run' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Resume run' })).toBeNull()
})

test('kill asks first, then calls run.kill', async () => {
  const record = seed({ paused: false })
  render(<RunActions run={record} />)

  fireEvent.click(screen.getByRole('button', { name: 'Kill run' }))
  expect(await screen.findByText('Kill this run?')).toBeTruthy()
  expect(api.runKill).not.toHaveBeenCalled()

  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Kill run' }))
  await waitFor(() => expect(api.runKill).toHaveBeenCalledWith(record.id))

  // The store is untouched: the run.status event, not this click, moves it.
  expect(useStore.getState().runs[record.id]?.status).toBe('running')
})

test('hand off offers the members who may own a run, and nobody else', async () => {
  const record = seed({ paused: false, members: [alice, bob, vera] })
  render(<RunActions run={record} />)

  fireEvent.click(screen.getByRole('button', { name: 'Hand off' }))

  const dialog = within(await screen.findByRole('dialog'))
  expect(dialog.getByRole('button', { name: 'Hand off to Bob' })).toBeTruthy()
  // Vera is a viewer and Alice already owns this run.
  expect(dialog.queryByRole('button', { name: 'Hand off to Vera' })).toBeNull()
  expect(dialog.queryByRole('button', { name: 'Hand off to Alice' })).toBeNull()

  fireEvent.click(dialog.getByRole('button', { name: 'Hand off to Bob' }))
  await waitFor(() =>
    expect(api.runHandoff).toHaveBeenCalledWith(record.id, bob.id),
  )
})

test('a run with nobody to hand to shows no hand off button', () => {
  render(<RunActions run={seed({ paused: false, members: [alice, vera] })} />)
  expect(screen.queryByRole('button', { name: 'Hand off' })).toBeNull()
})

// Pull is a local-gateway verb: a remote monitor never shows the button, so
// nobody clicks a verb the server would refuse anyway.
test('pull branch needs the local pull capability', async () => {
  const { unmount } = render(<RunActions run={seed({ paused: false })} />)
  expect(screen.queryByRole('button', { name: 'Pull branch' })).toBeNull()
  unmount()

  const record = seed({ paused: false, local: ['pull'] })
  render(<RunActions run={record} />)
  fireEvent.click(screen.getByRole('button', { name: 'Pull branch' }))
  await waitFor(() => expect(api.localPull).toHaveBeenCalledWith(record.id))
})

test('relaunch is offered on a finished run only', () => {
  const { unmount } = render(<RunActions run={seed({ paused: false })} />)
  expect(screen.queryByRole('button', { name: 'Relaunch run' })).toBeNull()
  unmount()

  render(<RunActions run={seed({ run: { status: 'merged' } })} />)
  expect(screen.getByRole('button', { name: 'Relaunch run' })).toBeTruthy()
  // A finished run has nothing left to steer.
  expect(screen.queryByRole('button', { name: 'Kill run' })).toBeNull()
})

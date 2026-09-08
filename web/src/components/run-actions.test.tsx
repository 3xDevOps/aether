import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { RunActions } from '@/components/run-actions'
import { api } from '@/lib/api'
import type { GatewayCapabilities, Member, Run } from '@/lib/types'
import { useStore } from '@/store'
import { toRecord, type RunRecord } from '@/store/runs'
import { alice, bob, run, serverInfo, vera, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

// jsdom implements no ResizeObserver, and the More trigger watches its own
// box so a menu left open when the row widens does not float loose. The stub
// keeps every watcher against the element it observes, because the open menu
// brings observers of its own and only the trigger's one is under test.
type Resize = (entries: Array<{ contentRect: { width: number } }>) => void
let watchers: { target: Element; notify: Resize }[] = []
vi.stubGlobal(
  'ResizeObserver',
  class {
    constructor(private notify: Resize) {}
    observe(target: Element) {
      watchers.push({ target, notify: this.notify })
    }
    unobserve(target: Element) {
      watchers = watchers.filter((w) => w.target !== target || w.notify !== this.notify)
    }
    disconnect() {
      watchers = watchers.filter((w) => w.notify !== this.notify)
    }
  },
)

/** Reports a new width to whatever watches `target`'s box. */
function resize(target: Element, width: number) {
  const watching = watchers.filter((w) => w.target === target)
  expect(watching.length).toBeGreaterThan(0)
  for (const w of watching) w.notify([{ contentRect: { width } }])
}

const every: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }

function seed(
  over: {
    run?: Partial<Run>
    paused?: boolean
    local?: string[]
    members?: Member[]
    /** Who is looking; the run belongs to Alice unless `run` says otherwise. */
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
  })
  return record
}

beforeEach(() => {
  vi.clearAllMocks()
  watchers = []
})

// The tri-state: hydration seeds the pause state from the run list, and a
// gateway that sends none leaves it unknown. Offering neither verb beats
// offering the one the server would refuse.
test('pause, resume and neither follow the known pause state', () => {
  const { unmount } = render(<RunActions run={seed({ paused: false })} />)
  expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Resume' })).toBeNull()
  unmount()

  const resumed = render(<RunActions run={seed({ paused: true })} />)
  expect(screen.getByRole('button', { name: 'Resume' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
  resumed.unmount()

  render(<RunActions run={seed()} />)
  expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Resume' })).toBeNull()
})

test('kill asks first, then calls run.kill', async () => {
  const record = seed({ paused: false })
  render(<RunActions run={record} />)

  fireEvent.click(screen.getByRole('button', { name: 'Kill' }))
  expect(await screen.findByText('Kill this run?')).toBeTruthy()
  expect(api.runKill).not.toHaveBeenCalled()

  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Kill run' }))
  await waitFor(() => expect(api.runKill).toHaveBeenCalledWith(record.id))

  // The store is untouched: the run.status event, not this click, moves it.
  expect(useStore.getState().runs[record.id]?.status).toBe('running')
})

test('delete asks first, calls run.delete, and removes a live run', async () => {
  // Delete is authorized by the Kill policy, not by lifecycle status.
  const record = seed({ run: { status: 'queued' } })
  render(<RunActions run={record} />)

  fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
  expect(await screen.findByText('Delete this run?')).toBeTruthy()
  expect(api.runDelete).not.toHaveBeenCalled()

  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Delete run' }))
  await waitFor(() => expect(api.runDelete).toHaveBeenCalledWith(record.id))
  expect(useStore.getState().runs[record.id]).toBeUndefined()
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
test('pull branch needs a published commit and local pull capability', async () => {
  const { unmount } = render(<RunActions run={seed({ paused: false })} />)
  expect(screen.queryByRole('button', { name: 'Pull' })).toBeNull()
  unmount()

  const record = seed({
    paused: false,
    local: ['pull'],
    run: { last_commit: 'a'.repeat(40) },
  })
  render(<RunActions run={record} />)
  fireEvent.click(screen.getByRole('button', { name: 'Pull' }))
  await waitFor(() => expect(api.localPull).toHaveBeenCalledWith(record.id))
})

test('relaunch is offered on a finished run only', () => {
  const { unmount } = render(<RunActions run={seed({ paused: false })} />)
  expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
  unmount()
  render(<RunActions run={seed({ run: { status: 'merged' } })} />)
  expect(screen.getByRole('button', { name: 'Relaunch' })).toBeTruthy()
  // A finished agent has nothing to kill, but remains deletable.
  expect(screen.queryByRole('button', { name: 'Kill' })).toBeNull()
  expect(screen.getByRole('button', { name: 'Delete' })).toBeTruthy()
})

// The failure path: the server's refusal is the whole message, prefixed by
// what was being attempted so a toast on its own still says what failed.
test('a refused verb surfaces the server message verbatim', async () => {
  vi.mocked(api.runKill).mockRejectedValue(
    new Error('run is protected: only its owner or an admin may kill'),
  )
  render(<RunActions run={seed({ paused: false })} />)

  fireEvent.click(screen.getByRole('button', { name: 'Kill' }))
  const dialog = within(await screen.findByRole('dialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Kill run' }))

  await waitFor(() =>
    expect(toast.error).toHaveBeenCalledWith(
      'Killed failed: run is protected: only its owner or an admin may kill',
    ),
  )
})

// A pull shells out to `git fetch` and takes seconds. A second click would
// race the first on the same ref, so nothing in the bar is clickable while a
// verb is in flight.
test('the bar locks while a verb is in flight, and names what it fetched', async () => {
  let finish = (_: {
    branch: string
    ref: string
    output: string
    current: boolean
    dirty: boolean
  }) => {}
  vi.mocked(api.localPull).mockReturnValue(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const record = seed({
    paused: false,
    local: ['pull'],
    run: { last_commit: 'a'.repeat(40) },
  })
  render(<RunActions run={record} />)

  fireEvent.click(screen.getByRole('button', { name: 'Pull' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Pull' })).toHaveProperty(
      'disabled',
      true,
    ),
  )
  expect(screen.getByRole('button', { name: 'Kill' })).toHaveProperty(
    'disabled',
    true,
  )

  finish({
    branch: record.branch,
    ref: `refs/heads/${record.branch}`,
    output: 'From ssh://host\n * [new branch] run-1-checkout',
    current: false,
    dirty: false,
  })
  await waitFor(() =>
    expect(toast.success).toHaveBeenCalledWith(
      `Pulled refs/heads/${record.branch}`,
    ),
  )
  // And the git output waits on the store for the diff tab to show.
  expect(useStore.getState().pulls[record.id]?.output).toContain('new branch')
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Kill' })).toHaveProperty(
      'disabled',
      false,
    ),
  )
})

// internal/permissions: a viewer has neither steer nor kill, so every
// mutating verb would come back denied.
test('a viewer is offered nothing to change', () => {
  render(
    <RunActions
      run={seed({ paused: false, members: [alice, vera], self: vera, local: ['pull'] })}
    />,
  )

  for (const name of ['Pause', 'Send', 'Merged', 'Abandoned', 'Kill', 'Delete', 'Protect']) {
    expect(screen.queryByRole('button', { name })).toBeNull()
  }
  expect(screen.queryByRole('button', { name: 'Hand off' })).toBeNull()
  // Pull is this machine fetching a branch, not a call against the run.
  expect(screen.getByRole('button', { name: 'Pull' })).toBeTruthy()
})

// A collaborator may steer, kill, and delete somebody else's run, but handing
// it on and protecting it belong to its owner and to admins.
test('a collaborator on another member run may steer it but not give it away', () => {
  render(
    <RunActions
      run={seed({ paused: false, members: [alice, bob], self: bob })}
    />,
  )

  expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Kill' })).toBeTruthy()
<<<<<<< HEAD
  // Steering a run includes typing into it, which the bar calls Send.
  expect(screen.getByRole('button', { name: 'Send' })).toBeTruthy()
  // Delete is reserved for runs that already ended.
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
=======
  expect(screen.getByRole('button', { name: 'Delete' })).toBeTruthy()
>>>>>>> 2d7500d (fix: reconcile run lifecycle and terminal actions)
  expect(screen.queryByRole('button', { name: 'Protect' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Hand off' })).toBeNull()
})

// An admin is above both restrictions, so a protected run in a locked-down
// workspace still offers everything - including the verbs that are otherwise
// the owner's alone.
test('an admin keeps every verb on a protected run in an admins-only workspace', () => {
  render(
    <RunActions
      run={seed({
        paused: false,
        members: [bob, alice],
        self: alice,
        run: { member_id: bob.id, protected: true },
        steerOthers: 'admins_only',
      })}
    />,
  )

  expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Kill' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Unprotect' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Hand off' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Delete' })).toBeTruthy()
})

// The two restrictions the policy adds on top of the role table.
test('a protected run and an admins-only workspace close steering to others', () => {
  const { unmount } = render(
    <RunActions
      run={seed({ paused: false, members: [alice, bob], self: bob, run: { protected: true } })}
    />,
  )
  expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Kill' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
  unmount()

  render(
    <RunActions
      run={seed({
        paused: false,
        members: [alice, bob],
        self: bob,
        steerOthers: 'admins_only',
      })}
    />,
  )
  expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Kill' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
})

// jsdom evaluates no container query, so the row here keeps every button a
// narrow header would hide. The menu's items are portalled and exist only
// while it is open, so every check below opens it first.

/** A run waiting on review with a local gateway offers the widest verb list. */
function seedWidest() {
  return seed({
    paused: false,
    local: ['pull', 'forward.start'],
    members: [alice, bob],
    run: { status: 'needs-attention', last_commit: 'a'.repeat(40) },
  })
}

function openMore() {
  fireEvent.keyDown(screen.getByRole('button', { name: 'More' }), { key: 'Enter' })
  return screen.findByRole('menu')
}

/**
 * The verbs on the row, split by the classes that decide their fate on a
 * narrow header: one set stays, the other is hidden until `@4xl/header`.
 * jsdom evaluates no container query, so this reads the classes and proves
 * nothing about the resulting layout.
 */
function partition() {
  const stays: string[] = []
  const dropped: string[] = []
  for (const button of screen.getAllByRole('button')) {
    const name = button.textContent ?? ''
    if (name === 'More') {
      // The trigger appears at the same width the overflow verbs disappear.
      // Drift one and those verbs are reachable at no width at all.
      expect(button.className).toMatch(/@4xl\/header:hidden/)
      continue
    }
    if (/\bhidden\b/.test(button.className)) {
      expect(button.className).toMatch(/@4xl\/header:inline-flex/)
      dropped.push(name)
    } else {
      stays.push(name)
    }
  }
  return { stays: stays.sort(), dropped: dropped.sort() }
}

// The three run states it takes to see all six verbs the row keeps: resume
// needs a live paused run and relaunch a finished one, so neither is on the
// widest list.
test('every verb keeps its own side of the narrow-header split', () => {
  const widest = render(<RunActions run={seedWidest()} />)
  expect(partition()).toEqual({
    stays: ['Close', 'Pause', 'Send'],
    dropped: ['Delete', 'Forward', 'Hand off', 'Protect', 'Pull'],
  })
  widest.unmount()

  const paused = render(<RunActions run={seed({ paused: true })} />)
  expect(partition()).toEqual({
    stays: ['Kill', 'Resume', 'Send'],
    dropped: ['Protect'],
  })
  paused.unmount()

  render(<RunActions run={seed({ run: { status: 'merged' } })} />)
  expect(partition()).toEqual({
    stays: ['Relaunch'],
    dropped: ['Delete', 'Protect'],
  })
})

// CSS cannot close what it hides, so the menu has to notice the widening.
test('the More menu closes itself when the row widens past the threshold', async () => {
  render(<RunActions run={seedWidest()} />)
  // The open menu hides the rest of the page from the accessibility tree, so
  // the trigger has to be in hand before it opens.
  const trigger = screen.getByRole('button', { name: 'More' })
  await openMore()

  // A trigger the container query has hidden measures 0x0.
  act(() => resize(trigger, 0))

  await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
})

// Radix hands focus back to a trigger the widening has just hidden, which
// leaves the member on the body with no tab position at all.
test('the forced close leaves focus on the row, not on the body', async () => {
  render(<RunActions run={seedWidest()} />)
  const trigger = screen.getByRole('button', { name: 'More' })
  await openMore()

  act(() => resize(trigger, 0))

  await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
  expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Pause' }))

  // A close nobody forced still hands focus back the way Radix does.
  fireEvent.keyDown(await openMore(), { key: 'Escape' })
  await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
  expect(document.activeElement).toBe(trigger)
})

test('the More menu holds every verb the narrow row drops, and none of the rest', async () => {
  const record = seedWidest()
  render(<RunActions run={record} />)

  // Steering the run is what the row keeps at every width.
  for (const name of ['Pause', 'Send', 'Close']) {
    expect(screen.getByRole('button', { name })).toBeTruthy()
  }

  const menu = within(await openMore())
  for (const name of ['Forward', 'Delete', 'Protect', 'Pull', 'Hand off']) {
    expect(menu.getByRole('menuitem', { name })).toBeTruthy()
  }
  for (const name of ['Pause', 'Send', 'Close']) {
    expect(menu.queryByRole('menuitem', { name })).toBeNull()
  }

  fireEvent.click(menu.getByRole('menuitem', { name: 'Protect' }))
  await waitFor(() => expect(api.runProtect).toHaveBeenCalledWith(record.id, true))
})

// The menu is the only way to reach these verbs on a narrow header, so the
// confirm step and the handoff picker have to open from there too.
test('a verb picked from the More menu still asks first', async () => {
  const record = seedWidest()
  render(<RunActions run={record} />)

  const menu = within(await openMore())
  fireEvent.click(menu.getByRole('menuitem', { name: 'Delete' }))
  expect(await screen.findByText('Delete this run?')).toBeTruthy()
  expect(api.runDelete).not.toHaveBeenCalled()

  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Delete run' }))
  await waitFor(() => expect(api.runDelete).toHaveBeenCalledWith(record.id))
})

test('hand off picked from the More menu opens the member picker', async () => {
  const record = seedWidest()
  render(<RunActions run={record} />)

  const menu = within(await openMore())
  fireEvent.click(menu.getByRole('menuitem', { name: 'Hand off' }))

  const dialog = within(await screen.findByRole('dialog'))
  fireEvent.click(dialog.getByRole('button', { name: 'Hand off to Bob' }))
  await waitFor(() =>
    expect(api.runHandoff).toHaveBeenCalledWith(record.id, bob.id),
  )
})

// The row buttons show their own spinner, but a verb fired from the menu
// leaves the menu closed and the trigger is the only thing left to say that
// something is in flight.
test('the More trigger spins while a verb fired from it runs', async () => {
  vi.mocked(api.localPull).mockReturnValue(new Promise(() => {}))
  render(<RunActions run={seedWidest()} />)

  const menu = within(await openMore())
  fireEvent.click(menu.getByRole('menuitem', { name: 'Pull' }))

  await waitFor(() => {
    const trigger = screen.getByRole('button', { name: 'More' })
    expect(trigger).toHaveProperty('disabled', true)
    expect(trigger.querySelector('.animate-spin')).toBeTruthy()
  })
})

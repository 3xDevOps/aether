import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { WatchDialog } from '@/components/palette/watch-dialog'
import type { Event, Watch, WatchDelivery, WatchNotification } from '@/lib/types'
import { ApiError, codeNotFound, codeUnavailable } from '@/lib/api'
import { SettingsRoute } from '@/routes/settings'
import { WatchNotifySection } from '@/routes/settings/notifications'
import { useStore, type RootState } from '@/store'
import { toRecord } from '@/store/runs'
import { applyWatchChanged } from '@/store/watch-notice'
import { deleteWatch, loadWatches, readWatch, updateWatch } from '@/store/watches'
import { fakeApi, run, serverInfo, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))

const fixRun = run({ id: 'run_fix', task: 'fix the login redirect' })

function watch(over: Partial<Watch> = {}): Watch {
  return {
    id: 'wat-1',
    member_id: serverInfo.member.id,
    workspace_id: workspace.id,
    target: { kind: 'run', run_id: fixRun.id },
    target_title: 'fix the login redirect',
    conditions: ['needs_you', 'finished'],
    state: 'active',
    source: 'dashboard',
    created_at: '2026-10-26T10:00:00Z',
    updated_at: '2026-10-26T10:00:00Z',
    expires_at: '2026-11-09T10:00:00Z',
    observed: { summary: 'Working: editing auth.go', run_status: 'running', activity: 'working' },
    link: '/?page=settings&id=watches&watch=wat-1',
    ...over,
  }
}

const at = (channel: WatchDelivery['channel'], status: WatchDelivery['status'], detail?: string): WatchDelivery => ({ channel, status, at: '2026-10-26T01:00:00Z', ...(detail ? { detail } : {}) })

const notice: WatchNotification = {
  id: 'not-1',
  watch_id: 'wat-1',
  condition: 'needs_you',
  title: 'fix the login redirect needs you',
  body: 'Pick a database',
  link: `/?run=${fixRun.id}`,
  created_at: '2026-10-26T11:00:00Z',
  deliveries: [
    { channel: 'in_app', status: 'delivered', at: '2026-10-26T11:00:00Z' },
    { channel: 'push', status: 'delivered', detail: '2 devices', at: '2026-10-26T11:00:01Z' },
  ],
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    runs: { [fixRun.id]: toRecord(fixRun) },
    info: serverInfo,
    identityKey: 'me',
    capabilities: { gateway: 'server', methods: ['*'], ws: [] },
    hydrated: true,
    route: { name: 'settings', params: { section: 'watches' } },
    watches: {},
    watchesFor: 'me',
    watchNotices: {},
    paletteDialog: null,
    paletteRunID: null,
    paletteWatchTarget: null,
    ...extra,
  })
}

function showSettings(watches: Watch[], over: Parameters<typeof fakeApi>[0] = {}) {
  const client = fakeApi({ watchList: vi.fn(async () => watches), ...over })
  const view = render(<SettingsRoute params={useStore.getState().route.params} client={client} />)
  const rerender = () => view.rerender(<SettingsRoute params={useStore.getState().route.params} client={client} />)
  return { client, rerender }
}

describe('watches in settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed()
  })

  it('lists live watches with their state, the real error and the last notice, and All shows ended ones', async () => {
    const user = userEvent.setup()
    showSettings([
      watch({ last_notification: notice }),
      watch({ id: 'wat-2', target: { kind: 'pr', repo: 'acme/app', number: 12 }, target_title: 'Fix login', conditions: ['ci'], state: 'error', error: 'GitHub refused the token: 401 Bad credentials' }),
      watch({ id: 'wat-3', target_title: 'old run', state: 'done' }),
    ])
    const list = await screen.findByRole('list', { name: 'Active watches' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.textContent).toContain('Cannot check · CI finished')
    expect(rows[0]!.textContent).toContain('GitHub refused the token: 401 Bad credentials')
    expect(rows[0]!.textContent).toContain('PR #12 · acme/app')
    expect(rows[1]!.textContent).toContain('Watching · Needs you, Finished')
    expect(rows[1]!.textContent).toContain('Last notice: fix the login redirect needs you')
    expect(screen.queryByText('old run')).toBeNull()

    await user.click(screen.getByRole('radio', { name: 'All' }))
    expect(within(screen.getByRole('list', { name: 'All watches' })).getAllByRole('listitem')).toHaveLength(3)
    expect(screen.getByText('old run')).toBeDefined()
  })

  it('explains what a watch is when there are none', async () => {
    showSettings([])
    expect(await screen.findByRole('heading', { name: 'No watches yet' })).toBeDefined()
    expect(screen.getByText(/after you close the dashboard, and notify you here and on your push devices/)).toBeDefined()
  })

  it('opens the detail from a row with what the server sees and where each notice went', async () => {
    const user = userEvent.setup()
    const { rerender } = showSettings([watch()], {
      watchGet: vi.fn(async () => ({
        watch: watch(),
        notifications: [
          notice,
          {
            ...notice,
            id: 'not-0',
            title: 'Held one',
            deliveries: [at('push', 'held_quiet_hours', 'Until 07:00 CET')],
          },
          { ...notice, id: 'not-a', title: 'No device', deliveries: [at('push', 'no_destination')] },
          { ...notice, id: 'not-b', title: 'Off', deliveries: [at('push', 'disabled', 'Push is off in your notification settings')] },
          { ...notice, id: 'not-d', title: 'Waiting', deliveries: [at('push', 'pending', 'you are active at a dashboard')] },
          { ...notice, id: 'not-c', title: 'Broken', deliveries: [at('push', 'failed', '410 Gone from fcm.googleapis.com')] },
          { ...notice, id: 'not-e', title: 'Skipped', deliveries: [at('push', 'skipped', 'The run notification already sent it'), at('assistant', 'delivered')] },
          { ...notice, id: 'not-f', title: 'Withdrawn', deliveries: [at('push', 'withdrawn')] },
        ],
      })),
    })
    await user.click(await screen.findByRole('button', { name: /fix the login redirect/ }))
    expect(useStore.getState().route.params).toEqual({ section: 'watches', watch: 'wat-1' })
    rerender()

    const dialog = await screen.findByRole('dialog', { name: 'fix the login redirect' })
    expect(within(dialog).getByText('Working: editing auth.go')).toBeDefined()
    await within(dialog).findByText('Push delivered to 2 devices')
    expect(within(dialog).getByText('Held for quiet hours: Until 07:00 CET')).toBeDefined()
    expect(within(dialog).getByText('No push device - in-app only')).toBeDefined()
    expect(within(dialog).getByText('Push off: Push is off in your notification settings')).toBeDefined()
    expect(within(dialog).getByText('Push pending: you are active at a dashboard')).toBeDefined()
    expect(within(dialog).getByText('Push failed: 410 Gone from fcm.googleapis.com')).toBeDefined()
    expect(within(dialog).getByText('Push skipped: The run notification already sent it')).toBeDefined()
    expect(within(dialog).getByText('In the assistant chat')).toBeDefined()
    expect(within(dialog).getByText('Push withdrawn from your devices: the need ended')).toBeDefined()
    await user.click(within(dialog).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: fixRun.id } })
  })

  it('pauses, resumes and deletes with the server receipt', async () => {
    const user = userEvent.setup()
    seed({ route: { name: 'settings', params: { section: 'watches', watch: 'wat-1' } } })
    let current = watch()
    const { client } = showSettings([current], {
      watchGet: vi.fn(async () => ({ watch: current, notifications: [] })),
      watchUpdate: vi.fn(async (params) => {
        current = { ...current, state: params.paused ? 'paused' : 'active' }
        return { watch: current, receipt: params.paused ? 'Paused watching run fix-login.' : 'Watching run fix-login for: needs you, finished. Push: 2 devices.' }
      }),
      watchDelete: vi.fn(async () => ({})),
    })
    const dialog = await screen.findByRole('dialog')
    await user.click(await within(dialog).findByRole('button', { name: 'Pause' }))
    expect(client.watchUpdate).toHaveBeenCalledWith({ watch_id: 'wat-1', paused: true })
    expect(toast.success).toHaveBeenCalledWith('Paused watching run fix-login.')
    await user.click(await within(dialog).findByRole('button', { name: 'Resume' }))
    expect(client.watchUpdate).toHaveBeenLastCalledWith({ watch_id: 'wat-1', paused: false })

    await user.click(within(dialog).getByRole('button', { name: 'Delete…' }))
    await user.click(await screen.findByRole('button', { name: 'Delete watch' }))
    await waitFor(() => expect(client.watchDelete).toHaveBeenCalledWith('wat-1'))
    await waitFor(() => expect(useStore.getState().watches['wat-1']).toBeUndefined())
    expect(useStore.getState().route.params).toEqual({ section: 'watches' })
  })

  it('renews an expired watch with a new end no later than 90 days from now', async () => {
    const user = userEvent.setup()
    seed({ route: { name: 'settings', params: { section: 'watches', watch: 'wat-1' } } })
    const expired = watch({ state: 'expired', created_at: '2026-01-01T10:00:00Z', expires_at: '2026-01-15T10:00:00Z' })
    const { client } = showSettings([expired], {
      watchGet: vi.fn(async () => ({ watch: expired, notifications: [] })),
      watchUpdate: vi.fn(async (params) => ({ watch: { ...expired, state: 'active' as const, expires_at: params.expires_at!, updated_at: new Date().toISOString() }, receipt: 'Watching run fix-login for: needs you, finished.' })),
    })
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Ended')).toBeDefined()
    const again = await within(dialog).findByRole('button', { name: 'Watch again' })
    expect(again).toHaveProperty('disabled', true)
    const ends = within(dialog).getByLabelText('Ends on') as HTMLInputElement
    const limit = Date.now() + 90 * 24 * 60 * 60 * 1000
    await user.clear(ends)
    await user.type(ends, ends.max)
    await user.click(again)
    const params = vi.mocked(client.watchUpdate).mock.calls[0]![0]
    expect(Object.keys(params).sort()).toEqual(['expires_at', 'watch_id'])
    expect(Date.parse(params.expires_at!)).toBeLessThanOrEqual(limit)
    expect(Date.parse(params.expires_at!)).toBeGreaterThan(limit - 24 * 60 * 60 * 1000)
  })

  it('dates a done watch by when it finished, not by its end', async () => {
    seed({ route: { name: 'settings', params: { section: 'watches', watch: 'wat-1' } } })
    const done = watch({ state: 'done', updated_at: '2026-10-26T12:00:00Z' })
    showSettings([done], { watchGet: vi.fn(async () => ({ watch: done, notifications: [] })) })
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('Done', { selector: 'dt' })).toBeDefined()
    expect(within(dialog).queryByText('Ended')).toBeNull()
    expect(within(dialog).queryByText('Ends')).toBeNull()
    expect(within(dialog).queryByRole('button', { name: 'Save changes' })).toBeNull()
  })

  it('follows changes made elsewhere until the member edits, then sends only their edits', async () => {
    const user = userEvent.setup()
    seed({ route: { name: 'settings', params: { section: 'watches', watch: 'wat-1' } } })
    const { client } = showSettings([watch()], {
      watchGet: vi.fn(async () => ({ watch: watch(), notifications: [] })),
      watchUpdate: vi.fn(async () => ({ watch: watch({ label: 'login', conditions: ['needs_you'], updated_at: '2026-10-26T10:02:00Z' }), receipt: 'Watching run login for: needs you.' })),
    })
    const dialog = await screen.findByRole('dialog')
    const label = await within(dialog).findByLabelText('Label')
    useStore.getState().putWatch(watch({ conditions: ['needs_you'], updated_at: '2026-10-26T10:01:00Z' }))
    await waitFor(() => expect(within(dialog).getByRole('checkbox', { name: 'Finished' }).getAttribute('aria-checked')).toBe('false'))

    await user.type(label, 'login')
    useStore.getState().putWatch(watch({ conditions: ['needs_you'], expires_at: '2026-11-20T10:00:00Z', updated_at: '2026-10-26T10:01:30Z' }))
    expect(label).toHaveProperty('value', 'login')
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))
    expect(client.watchUpdate).toHaveBeenCalledWith({ watch_id: 'wat-1', label: 'login' })
  })

  it('fills the screen on a phone', async () => {
    atViewport(390, { pointer: 'coarse' })
    seed({ route: { name: 'settings', params: { section: 'watches', watch: 'wat-1' } } })
    showSettings([watch()], { watchGet: vi.fn(async () => ({ watch: watch(), notifications: [] })) })
    const dialog = await screen.findByRole('dialog')
    expect(dialog.getAttribute('data-variant')).toBe('side')
    expect(dialog.className).toContain('w-full')
  })

  it('reads a deep link from the address', async () => {
    const { routeFromQuery, routeQuery } = await import('@/lib/url-state')
    const route = routeFromQuery(new URLSearchParams('page=settings&id=watches&watch=wat-9'))
    expect(route).toEqual({ name: 'settings', params: { section: 'watches', watch: 'wat-9' } })
    expect(routeQuery(route).toString()).toBe('page=settings&id=watches&watch=wat-9')
  })
})

describe('creating a watch', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed({ paletteDialog: 'watch', paletteRunID: fixRun.id })
  })

  it('pre-checks the defaults, says what each condition means, and shows the receipt', async () => {
    const user = userEvent.setup()
    const client = fakeApi({
      watchCreate: vi.fn(async () => ({ watch: watch(), receipt: 'Watching run fix-login for: needs you, finished. Push: 2 devices. Expires Nov 9.' })),
    })
    render(<WatchDialog client={client} />)
    expect(screen.getByRole('dialog', { name: 'Watch “fix the login redirect”' })).toBeDefined()
    expect(screen.getByRole('checkbox', { name: 'Needs you' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('checkbox', { name: 'Finished' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('checkbox', { name: 'Pull request merged' }).getAttribute('aria-checked')).toBe('false')
    expect(screen.getByText(/Finished means the agent reported done/)).toBeDefined()
    expect(screen.getByText(/Merged means GitHub merged the PR/)).toBeDefined()

    await user.click(screen.getByRole('checkbox', { name: 'CI finished' }))
    await user.click(screen.getByRole('button', { name: 'Watch' }))
    const params = vi.mocked(client.watchCreate).mock.calls[0]![0]
    expect(params).toMatchObject({ target: { kind: 'run', run_id: fixRun.id }, conditions: ['needs_you', 'finished', 'ci'], source: 'dashboard' })
    expect(params.idempotency_key).toBeTruthy()
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    const [receipt, options] = vi.mocked(toast.success).mock.calls[0]!
    expect(receipt).toBe('Watching run fix-login for: needs you, finished. Push: 2 devices. Expires Nov 9.')
    ;(options as unknown as { action: { onClick: () => void } }).action.onClick()
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'watches', watch: 'wat-1' } })
    expect(useStore.getState().watches['wat-1']).toBeDefined()
  })

  it('keeps the server refusal in the form', async () => {
    const user = userEvent.setup()
    seed({ paletteDialog: 'watch', paletteWatchTarget: { kind: 'pr', repo: 'acme/app', number: 12 } })
    const client = fakeApi({ watchCreate: vi.fn(async () => { throw new Error('watch.create: acme/app is not reachable with your GitHub credentials') }) })
    render(<WatchDialog client={client} />)
    expect(screen.getByRole('checkbox', { name: 'Merged' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('checkbox', { name: 'Closed' }).getAttribute('aria-checked')).toBe('false')
    await user.click(screen.getByRole('button', { name: 'Watch' }))
    expect((await screen.findByRole('alert')).textContent).toContain('acme/app is not reachable with your GitHub credentials')
    expect(useStore.getState().paletteDialog).toBe('watch')
  })
})

function held<T>() {
  let release!: (value: T) => void
  const promise = new Promise<T>((resolve) => { release = resolve })
  return { promise, release }
}

describe('watch answers that arrive late', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('opens the watch dialog where crypto.randomUUID is missing, as on plain HTTP', async () => {
    vi.stubGlobal('crypto', {})
    seed({ paletteDialog: 'watch', paletteRunID: fixRun.id })
    const user = userEvent.setup()
    const client = fakeApi({ watchCreate: vi.fn(async () => ({ watch: watch(), receipt: 'Watching run fix-login.' })) })
    render(<WatchDialog client={client} />)
    await user.click(screen.getByRole('checkbox', { name: 'CI finished' }))
    await user.click(screen.getByRole('button', { name: 'Watch' }))
    expect(vi.mocked(client.watchCreate).mock.calls[0]![0].idempotency_key).toMatch(/^watch-/)
  })

  it("drops a create, change or read answered after the member switched, and never shows the first member's watch", async () => {
    seed({ paletteDialog: 'watch', paletteRunID: fixRun.id })
    const created = held<{ watch: Watch; receipt: string }>()
    const client = fakeApi({ watchCreate: vi.fn(() => created.promise) })
    render(<WatchDialog client={client} />)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Watch' }))
    useStore.setState({ identityKey: 'other', watches: {}, watchesFor: 'other' })
    created.release({ watch: watch(), receipt: 'Watching run fix-login.' })
    await waitFor(() => expect(useStore.getState().paletteDialog).toBeNull())
    expect(useStore.getState().watches).toEqual({})
    expect(toast.success).not.toHaveBeenCalled()

    useStore.setState({ identityKey: 'me', watches: {}, watchesFor: 'me' })
    const updated = held<{ watch: Watch; receipt: string }>()
    const got = held<{ watch: Watch; notifications: WatchNotification[] }>()
    const listed = held<Watch[]>()
    const later = fakeApi({ watchUpdate: vi.fn(() => updated.promise), watchGet: vi.fn(() => got.promise), watchList: vi.fn(() => listed.promise) })
    const update = updateWatch(useStore, later, { watch_id: 'wat-1', paused: true })
    const read = readWatch(useStore, later, 'wat-1')
    const list = loadWatches(useStore, later)
    useStore.setState({ identityKey: 'other', watches: {}, watchesFor: 'other' })
    updated.release({ watch: watch({ state: 'paused' }), receipt: 'Paused.' })
    got.release({ watch: watch(), notifications: [notice] })
    listed.release([watch()])
    expect(await update).toBeNull()
    await read
    await list
    expect(useStore.getState().watches).toEqual({})
    expect(useStore.getState().watchNotices).toEqual({})
  })

  it('keeps the notices of a read sent before the first list of a page', async () => {
    seed({ watchesFor: null })
    const got = held<{ watch: Watch; notifications: WatchNotification[] }>()
    const listed = held<Watch[]>()
    const client = fakeApi({ watchGet: vi.fn(() => got.promise), watchList: vi.fn(() => listed.promise) })
    const read = readWatch(useStore, client, 'wat-1')
    const list = loadWatches(useStore, client)
    got.release({ watch: watch(), notifications: [notice] })
    await read
    listed.release([watch()])
    await list
    expect(useStore.getState().watches['wat-1']).toBeDefined()
    expect(useStore.getState().watchNotices['wat-1']).toEqual([notice])
  })

  it("keeps a read's notices when a change answers first, and a deleted watch gone when a change answers after", async () => {
    const got = held<{ watch: Watch; notifications: WatchNotification[] }>()
    const client = fakeApi({ watchGet: vi.fn(() => got.promise), watchUpdate: vi.fn(async () => ({ watch: watch({ state: 'paused' }), receipt: 'Paused.' })) })
    const read = readWatch(useStore, client, 'wat-1')
    await updateWatch(useStore, client, { watch_id: 'wat-1', paused: true })
    got.release({ watch: watch(), notifications: [notice] })
    await read
    expect(useStore.getState().watches['wat-1']?.state).toBe('paused')
    expect(useStore.getState().watchNotices['wat-1']).toEqual([notice])

    const updated = held<{ watch: Watch; receipt: string }>()
    const racing = fakeApi({ watchUpdate: vi.fn(() => updated.promise), watchDelete: vi.fn(async () => ({})) })
    const update = updateWatch(useStore, racing, { watch_id: 'wat-1', label: 'login' })
    await deleteWatch(useStore, racing, 'wat-1')
    updated.release({ watch: watch({ label: 'login' }), receipt: 'Watching run login.' })
    await update
    expect(useStore.getState().watches['wat-1']).toBeUndefined()
  })

  it('keeps a deleted watch gone when an older read or list answers after the delete', async () => {
    const got = held<{ watch: Watch; notifications: WatchNotification[] }>()
    const listed = held<Watch[]>()
    const client = fakeApi({ watchGet: vi.fn(() => got.promise), watchList: vi.fn(() => listed.promise), watchDelete: vi.fn(async () => ({})) })
    useStore.getState().putWatch(watch())
    const read = readWatch(useStore, client, 'wat-1')
    const list = loadWatches(useStore, client)
    await deleteWatch(useStore, client, 'wat-1')
    got.release({ watch: watch(), notifications: [notice] })
    listed.release([watch()])
    await read
    await list
    expect(useStore.getState().watches['wat-1']).toBeUndefined()
  })

  it('keeps the newest state when watch.changed reads answer out of order', async () => {
    const first = held<{ watch: Watch; notifications: WatchNotification[] }>()
    const second = held<{ watch: Watch; notifications: WatchNotification[] }>()
    const answers = [first.promise, second.promise]
    const client = fakeApi({ watchGet: vi.fn(() => answers.shift()!) })
    const event = (): Event => ({ id: 'ev-1', seq: 1, type: 'watch.changed', time: '2026-01-01T00:00:00Z', workspace_id: '', run_id: '', actor_id: '', payload: { watch_id: 'wat-1', member_id: serverInfo.member.id } })
    const older = applyWatchChanged(useStore, client, event())
    const newer = applyWatchChanged(useStore, client, event())
    second.release({ watch: watch({ state: 'paused' }), notifications: [notice] })
    await newer
    first.release({ watch: watch(), notifications: [] })
    await older
    expect(useStore.getState().watches['wat-1']?.state).toBe('paused')
    expect(useStore.getState().watchNotices['wat-1']).toEqual([notice])

    const gone = fakeApi({ watchGet: vi.fn(async () => { throw new ApiError(404, 'watch.get: no such watch', codeNotFound) }) })
    const stale = held<{ watch: Watch; notifications: WatchNotification[] }>()
    const slow = fakeApi({ watchGet: vi.fn(() => stale.promise) })
    const before = applyWatchChanged(useStore, slow, event())
    await applyWatchChanged(useStore, gone, event())
    stale.release({ watch: watch(), notifications: [] })
    await before
    expect(useStore.getState().watches['wat-1']).toBeUndefined()
  })
})

describe('watch notification preferences', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed()
  })

  it('states the destinations and saves the push toggle with the quiet hours it keeps', async () => {
    const user = userEvent.setup()
    const client = fakeApi({
      notifyPrefsGet: vi.fn(async () => ({ push: true, destinations: { push_devices: 2 } })),
      notifyPrefsSet: vi.fn(async (prefs) => ({ push: prefs.push ?? true, quiet_hours: prefs.quiet_hours, destinations: { push_devices: 2 } })),
    })
    render(<WatchNotifySection client={client} />)
    expect(await screen.findByText(/Push to 2 devices · the desktop app while it is open · in-app on every dashboard/)).toBeDefined()
    await user.click(screen.getByRole('checkbox', { name: 'Watch notifications on push devices' }))
    expect(client.notifyPrefsSet).toHaveBeenCalledWith({ push: false })
    expect(await screen.findByText(/Push is off/)).toBeDefined()
  })

  it('says in-app only with no push device, and quiet hours default to this browser’s time zone', async () => {
    const user = userEvent.setup()
    const client = fakeApi({
      notifyPrefsGet: vi.fn(async () => ({ push: true, destinations: { push_devices: 0 } })),
      notifyPrefsSet: vi.fn(async (prefs) => ({ push: prefs.push ?? true, quiet_hours: prefs.quiet_hours, destinations: { push_devices: 0 } })),
    })
    render(<WatchNotifySection client={client} />)
    expect(await screen.findByText(/No push device subscribed, so in-app only/)).toBeDefined()
    await user.click(screen.getByRole('checkbox', { name: 'Quiet hours' }))
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
    expect(client.notifyPrefsSet).toHaveBeenCalledWith({ quiet_hours: { start: '22:00', end: '07:00', tz: zone } })
    expect(await screen.findByLabelText('Time zone')).toHaveProperty('value', zone)
  })

  it('tells a server without Web Push apart from no device, and shows a quiet-hours error on that row', async () => {
    const user = userEvent.setup()
    const client = fakeApi({
      notifyPrefsGet: vi.fn(async () => ({ push: true, destinations: { push_devices: 0 } })),
      notifyPrefsSet: vi.fn(async () => { throw new Error('notify.prefs.set: unknown time zone "Mars/Olympus"') }),
      pushStatus: vi.fn(async () => { throw new ApiError(503, 'push service not configured', codeUnavailable) }),
    })
    render(<WatchNotifySection client={client} />)
    expect(await screen.findByText(/No push \(push service not configured\), so in-app only/)).toBeDefined()
    await user.click(screen.getByRole('checkbox', { name: 'Quiet hours' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('unknown time zone')
    expect(alert.closest('#quiet-hours')).not.toBeNull()
  })

  it('re-reads the device count when this device subscribes', async () => {
    const notifyPrefsGet = vi.fn(async () => ({ push: true, destinations: { push_devices: 0 } }))
    const client = fakeApi({ notifyPrefsGet })
    const view = render(<WatchNotifySection client={client} subscriptions={0} />)
    expect(await screen.findByText(/No push device subscribed/)).toBeDefined()
    notifyPrefsGet.mockResolvedValue({ push: true, destinations: { push_devices: 1 } })
    view.rerender(<WatchNotifySection client={client} subscriptions={1} />)
    expect(await screen.findByText(/Push to 1 device ·/)).toBeDefined()
  })

  it('does not claim the server lacks Web Push when push.status fails for another reason', async () => {
    const client = fakeApi({
      notifyPrefsGet: vi.fn(async () => ({ push: true, destinations: { push_devices: 2 } })),
      pushStatus: vi.fn(async () => { throw new Error('network down') }),
    })
    render(<WatchNotifySection client={client} />)
    expect(await screen.findByText(/Push to 2 devices/)).toBeDefined()
  })
})

describe('watch.changed', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed()
  })

  const event = (time: string, payload: object): Event => ({ id: 'ev-1', seq: 1, type: 'watch.changed', time, workspace_id: '', run_id: '', actor_id: '', payload })

  it('toasts a live notice with an Open action and re-reads the watch', async () => {
    const client = fakeApi({ watchGet: vi.fn(async () => ({ watch: watch({ last_notification: notice }), notifications: [notice] })) })
    await applyWatchChanged(useStore, client, event(new Date().toISOString(), { watch_id: 'wat-1', member_id: serverInfo.member.id, notification_id: 'not-1' }))
    expect(useStore.getState().watchNotices['wat-1']).toEqual([notice])
    expect(toast).toHaveBeenCalledWith('fix the login redirect needs you', expect.objectContaining({ description: 'Pick a database' }))
    const options = vi.mocked(toast).mock.calls[0]![1] as unknown as { action: { onClick: () => void } }
    options.action.onClick()
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: fixRun.id } })
  })

  it('updates silently for a replayed notice or another member', async () => {
    const client = fakeApi({ watchGet: vi.fn(async () => ({ watch: watch(), notifications: [notice] })) })
    await applyWatchChanged(useStore, client, event('2026-01-01T00:00:00Z', { watch_id: 'wat-1', member_id: serverInfo.member.id, notification_id: 'not-1' }))
    expect(useStore.getState().watches['wat-1']).toBeDefined()
    await applyWatchChanged(useStore, client, event(new Date().toISOString(), { watch_id: 'wat-1', member_id: 'mem_other', notification_id: 'not-1' }))
    expect(toast).not.toHaveBeenCalled()
    expect(client.watchGet).toHaveBeenCalledTimes(1)
  })

  it('leaves the toast of an assistant-made watch to its assistant.notice', async () => {
    const client = fakeApi({ watchGet: vi.fn(async () => ({ watch: watch({ source: 'assistant' }), notifications: [notice] })) })
    await applyWatchChanged(useStore, client, event(new Date().toISOString(), { watch_id: 'wat-1', member_id: serverInfo.member.id, notification_id: 'not-1' }))
    expect(useStore.getState().watches['wat-1']).toBeDefined()
    expect(toast).not.toHaveBeenCalled()
  })
})

describe('the run header hint', () => {
  it('names the watch’s own conditions and state', async () => {
    const { watchHint } = await import('@/lib/watches')
    expect(watchHint(watch({ conditions: ['needs_you', 'ci'] }))).toBe(
      'Notifies you on: Needs you, CI finished. Notices appear in the dashboard, and on your push devices when push is on.',
    )
    expect(watchHint(watch({ state: 'paused' }))).toBe('Watch paused: no notices until you resume it.')
    expect(watchHint(watch({ state: 'error', error: 'run is gone' }))).toContain('run is gone')
  })
})

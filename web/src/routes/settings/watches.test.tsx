import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { WatchDialog } from '@/components/palette/watch-dialog'
import type { Event, Watch, WatchDelivery, WatchNotification } from '@/lib/types'
import { SettingsRoute } from '@/routes/settings'
import { WatchNotifySection } from '@/routes/settings/notifications'
import { useStore, type RootState } from '@/store'
import { toRecord } from '@/store/runs'
import { applyWatchChanged } from '@/store/watch-notice'
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
    link: `/?run=${fixRun.id}`,
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
    expect(await screen.findByRole('heading', { name: 'No watches yet.' })).toBeDefined()
    expect(screen.getByText(/even with every Aether page closed/)).toBeDefined()
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
            deliveries: [at('push', 'held_quiet_hours'), at('desktop', 'failed', 'socket closed')],
          },
          { ...notice, id: 'not-a', title: 'No device', deliveries: [at('push', 'no_destination')] },
          { ...notice, id: 'not-b', title: 'Off', deliveries: [at('push', 'disabled')] },
          { ...notice, id: 'not-d', title: 'Waiting', deliveries: [at('push', 'pending', 'you are active at a dashboard')] },
          { ...notice, id: 'not-c', title: 'Broken', deliveries: [at('push', 'failed', '410 Gone from fcm.googleapis.com')] },
        ],
      })),
    })
    await user.click(await screen.findByRole('button', { name: /fix the login redirect/ }))
    expect(useStore.getState().route.params).toEqual({ section: 'watches', watch: 'wat-1' })
    rerender()

    const dialog = await screen.findByRole('dialog', { name: 'fix the login redirect' })
    expect(within(dialog).getByText('Working: editing auth.go')).toBeDefined()
    await within(dialog).findByText('Push delivered to 2 devices')
    expect(within(dialog).getByText('Held for quiet hours')).toBeDefined()
    expect(within(dialog).getByText('Desktop app failed: socket closed')).toBeDefined()
    expect(within(dialog).getByText('No push device - in-app only')).toBeDefined()
    expect(within(dialog).getByText('Push off')).toBeDefined()
    expect(within(dialog).getByText('Push pending: you are active at a dashboard')).toBeDefined()
    expect(within(dialog).getByText('Push failed: 410 Gone from fcm.googleapis.com')).toBeDefined()
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

describe('watch notification preferences', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed()
  })

  it('states the destinations and saves the push toggle without them', async () => {
    const user = userEvent.setup()
    const client = fakeApi({
      notifyPrefsGet: vi.fn(async () => ({ push: true, destinations: { push_devices: 2 } })),
      notifyPrefsSet: vi.fn(async (prefs) => ({ ...prefs, destinations: { push_devices: 2 } })),
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
      notifyPrefsSet: vi.fn(async (prefs) => prefs),
    })
    render(<WatchNotifySection client={client} />)
    expect(await screen.findByText(/No push device subscribed, so in-app only/)).toBeDefined()
    await user.click(screen.getByRole('checkbox', { name: 'Quiet hours' }))
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
    expect(client.notifyPrefsSet).toHaveBeenCalledWith({ push: true, quiet_hours: { start: '22:00', end: '07:00', tz: zone } })
    expect(await screen.findByLabelText('Time zone')).toHaveProperty('value', zone)
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
})

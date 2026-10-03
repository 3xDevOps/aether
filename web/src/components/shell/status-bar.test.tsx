import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StatusBar } from '@/components/shell/status-bar'
import { api } from '@/lib/api'
import { ApprovalStatus } from '@/routes/team/approvals'
import type { GatewayCapabilities, Member } from '@/lib/types'
import { useStore } from '@/store'
import {
  alice,
  approval,
  bob,
  serverInfo,
  fakeApi,
  serverUpdateStatus,
  updateStatus,
  vera,
  workspace,
} from '@/test/fixtures'
import { hintOn } from '@/test/tooltip'
import { atViewport } from '@/test/viewport'

beforeEach(() => {
  const client = fakeApi()
  vi.spyOn(api, 'presenceRoster').mockImplementation(client.presenceRoster)
  vi.spyOn(api, 'disk').mockImplementation(client.disk)
  vi.spyOn(api, 'approvalList').mockImplementation(client.approvalList)
  vi.spyOn(api, 'budgetGet').mockImplementation(client.budgetGet)
  vi.spyOn(api, 'presenceHeartbeat').mockImplementation(client.presenceHeartbeat)
})

afterEach(() => vi.restoreAllMocks())

function openDetails() {
  const toggle = screen.getByRole('button', { name: 'Show status details' })
  fireEvent.click(toggle)
  return toggle
}
/** The desktop gateway's descriptor, carrying the update verbs. */
function caps(): GatewayCapabilities {
  return {
    gateway: 'local',
    methods: ['*'],
    ws: ['events', 'attach', 'terminal'],
    local: ['link.status', 'update.check', 'update.apply'],
    version: 'v1.2.3',
  }
}

function seed(over: { self?: Member; update?: ReturnType<typeof updateStatus> | null } = {}) {
  useStore.setState({
    info: { ...serverInfo, member: over.self ?? alice },
    capabilities: caps(),
    connection: 'live',
    unreachable: null,
    update: over.update === undefined ? updateStatus() : over.update,
    dismissedUpdates: { cli: 'v1.3.0', server: 'v1.3.0', shell: '' },
    serverUpdate: null,
    serverUpdateProgress: null,
    hydrated: true,
    linkStatus: null,
    inbox: {},
    inboxError: null,
    budgets: {},
    presence: [],
    workspaces: {},
    activeWorkspace: '',
    route: { name: 'board', params: {} },
  })
}

// The version label restores a dismissed update banner.
test('the version label is plain until an update is available', () => {
  seed({ update: null })
  render(<StatusBar />)
  openDetails()

  expect(screen.getByText(`aether ${serverInfo.server_version}`)).toBeTruthy()
  expect(screen.queryByRole('button', { name: /Update available/ })).toBeNull()
})

test('opens and closes the secondary status actions on a phone', async () => {
  atViewport(390, { height: 844 })
  seed()
  render(<StatusBar />)

  const toggle = screen.getByRole('button', { name: 'Show status details' })
  expect(toggle.getAttribute('aria-expanded')).toBe('false')

  toggle.focus()
  await userEvent.keyboard('{Enter}')
  expect(toggle.getAttribute('aria-expanded')).toBe('true')

  await userEvent.keyboard('{Enter}')
  expect(toggle.getAttribute('aria-expanded')).toBe('false')
})


// Touch has no hover to find the trigger with a second time, so the popup
// lets go of the screen the way every other overlay does.
test('the compact details popup closes on Escape and on a tap outside', async () => {
  seed()
  render(<StatusBar />)

  const toggle = screen.getByRole('button', { name: 'Show status details' })
  toggle.focus()
  await userEvent.keyboard('{Enter}')
  expect(toggle.getAttribute('aria-expanded')).toBe('true')

  fireEvent.keyDown(window, { key: 'Escape' })
  expect(toggle.getAttribute('aria-expanded')).toBe('false')
  expect(document.activeElement).toBe(toggle)

  toggle.focus()
  await userEvent.keyboard('{Enter}')
  fireEvent.pointerDown(document.body)
  expect(toggle.getAttribute('aria-expanded')).toBe('false')
})

// A tooltip and a `title` are a pointer's alone. The compact popup is the one
// place in the shell with room to say the same things out loud.
test('the compact details popup writes out what only a hint used to carry', async () => {
  seed()
  useStore.setState({
    linkStatus: {
      server_configured: true,
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
    },
  })
  render(<StatusBar />)

  const toggle = screen.getByRole('button', { name: 'Show status details' })
  toggle.focus()
  await userEvent.keyboard('{Enter}')

  expect(screen.getByText('Protocol 1')).toBeTruthy()
  expect(screen.getByText('Linked to /src/repo')).toBeTruthy()
  expect(screen.getByText('Worktrees 256 MB')).toBeTruthy()
  expect(screen.getByText('Repos 512 MB')).toBeTruthy()
})

test('a CLI update turns the label into a button that clears the dismissals', async () => {
  seed()
  render(<StatusBar />)
  openDetails()

  const badge = screen.getByRole('button', { name: 'Update available: v1.3.0' })
  // The label says which version is installed; only the hint says which one
  // pressing the badge would bring back.
  expect(await hintOn(badge)).toBe('v1.3.0 is available - show the update banner')

  fireEvent.click(badge)

  expect(useStore.getState().dismissedUpdates).toEqual({
    cli: '',
    server: '',
    shell: '',
  })
})


// server_behind is an admin's business: a collaborator can do nothing about
// the server, so it must not put a dot on their status bar.
test('a behind server badges the label for an admin only', () => {
  const serverOnly = updateStatus({
    cli: { ...updateStatus().cli, update_available: false },
    server_version: 'v1.2.9',
    server_behind: true,
  })

  seed({ self: bob, update: serverOnly })
  const collaborator = render(<StatusBar />)
  openDetails()
  expect(screen.queryByRole('button', { name: /Update available/ })).toBeNull()
  collaborator.unmount()

  seed({ self: alice, update: serverOnly })
  render(<StatusBar />)
  openDetails()
  expect(screen.getByRole('button', { name: /Update available/ })).toBeTruthy()
})

// A member who cannot press the buttons still watches the terminals drop.
// The admin has the banner, which says the same thing with the controls
// attached, so the notice would only be noise there.
describe('the server update notice', () => {
  const notice = 'server update scheduled, terminals will reconnect briefly'

  test('tells a collaborator a scheduled update is coming', () => {
    seed({ self: bob })
    useStore.getState().applyServerUpdate({ phase: 'scheduled', version: 'v1.3.0' })
    render(<StatusBar />)

    expect(screen.getByText(notice)).toBeTruthy()
  })

  test('says the update is applying once it starts, to a viewer too', () => {
    seed({ self: vera })
    useStore.getState().applyServerUpdate({ phase: 'applying', version: 'v1.3.0' })
    render(<StatusBar />)

    expect(
      screen.getByText('server update applying, terminals will reconnect briefly'),
    ).toBeTruthy()
  })

  // Someone else scheduled it before this tab loaded: the phase never came
  // over the feed, and the status answer is what carries it.
  test('picks up a pending update from the status answer', () => {
    seed({ self: bob })
    useStore.setState({
      serverUpdate: serverUpdateStatus({
        update_available: true,
        pending: {
          version: 'v1.3.0',
          requested_by: alice.id,
          requested_at: '2026-08-14T10:06:00Z',
        },
      }),
    })
    render(<StatusBar />)

    expect(screen.getByText(notice)).toBeTruthy()
  })

  test('stays away from an admin, and once the update is over', () => {
    seed({ self: alice })
    useStore.getState().applyServerUpdate({ phase: 'scheduled', version: 'v1.3.0' })
    const admin = render(<StatusBar />)
    expect(screen.queryByText(notice)).toBeNull()
    admin.unmount()

    seed({ self: bob })
    useStore.getState().applyServerUpdate({ phase: 'cancelled', version: 'v1.3.0' })
    render(<StatusBar />)
    expect(screen.queryByText(/server update/)).toBeNull()
  })
})


// The bar says the disk is filling; the tooltip says what is filling it.
// Bare workspace repos keep every push, every run branch and the reflogs,
// and nothing reclaims them, so leaving them out of the breakdown makes a
// repo-dominated server's shrinking headroom unexplainable.
describe('the disk gauge breakdown', () => {
  test('names the bare workspace repos alongside the other tenants', () => {
    seed()
    render(<StatusBar />)

    const title = screen.getByLabelText('Disk usage').getAttribute('title') ?? ''
    expect(title).toContain('Worktrees 256 MB')
    expect(title).toContain('Transcripts 128 MB')
    expect(title).toContain('Database 64 MB')
    expect(title).toContain('Repos 512 MB')
  })

  // A server predating the component sends no repo_bytes. Showing it as
  // zero would claim the deployment holds no repositories.
  test('drops the repos line when the server does not report it', () => {
    seed()
    const { repo_bytes: _dropped, ...older } = serverInfo.disk!
    useStore.setState({ info: { ...serverInfo, member: alice, disk: older } })
    render(<StatusBar />)

    const title = screen.getByLabelText('Disk usage').getAttribute('title') ?? ''
    expect(title).toContain('Worktrees 256 MB')
    expect(title).not.toContain('Repos')
  })
})

test('desktop details expose repository facts without navigation', () => {
  atViewport(1440)
  seed({ update: null })
  useStore.setState({
    inbox: { [workspace.id]: [approval()] },
    linkStatus: {
      server_configured: true,
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
    },
  })
  render(<StatusBar />)
  const toggle = openDetails()

  expect(screen.getByText('Linked to /src/repo')).toBeTruthy()
  expect(screen.queryByRole('button', { name: /Linked|Not linked|Activity|waiting|Theme:/ })).toBeNull()
  fireEvent.click(screen.getByText('Linked', { exact: true }))
  expect(useStore.getState().route.name).toBe('board')
  fireEvent.click(toggle)
  expect(toggle.getAttribute('aria-expanded')).toBe('false')
})

test('a missing link answer is not reported as an unlinked repository', () => {
  seed()
  render(<StatusBar />)
  openDetails()

  expect(screen.getByText('Link status unknown')).toBeTruthy()
  expect(screen.queryByText('Not linked')).toBeNull()
})

test('the phone approval signal follows the drawer boundary and capability', () => {
  const resize = atViewport(641)
  seed()
  useStore.setState({ inbox: { [workspace.id]: [approval()] } })
  render(<ApprovalStatus />)
  expect(screen.queryByRole('button', { name: '1 waiting' })).toBeNull()

  resize(640)
  fireEvent.click(screen.getByRole('button', { name: '1 waiting' }))
  expect(useStore.getState().route.name).toBe('approvals')

  act(() => useStore.setState({ inboxError: 'approval.list: database is locked' }))
  expect(screen.getByRole('button', { name: 'queue unreadable' })).toBeTruthy()
  act(() => useStore.setState({ capabilities: { ...caps(), methods: [] } }))
  expect(screen.queryByRole('button')).toBeNull()
})

test('phone attention stays outside closed details and raw errors remain readable', async () => {
  atViewport(390)
  seed()
  const error = 'approval.list: database is locked'
  vi.mocked(api.approvalList).mockRejectedValue(new Error(error))
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    inboxError: error,
  })
  render(<StatusBar />)
  const attention = screen.getByRole('button', { name: 'queue unreadable' })
  expect(attention.closest('#status-details')).toBeNull()

  const toggle = openDetails()
  expect(await screen.findByRole('alert')).toHaveProperty('textContent', error)
  fireEvent.click(toggle)
  fireEvent.click(attention)
  expect(useStore.getState().route.name).toBe('approvals')
})

test('Usage owns its portalled interactions before status details dismiss', async () => {
  const resize = atViewport(1440)
  seed()
  vi.spyOn(api, 'accountList').mockResolvedValue({ accounts: [], shared_with: [] })
  vi.spyOn(api, 'accountUsage').mockRejectedValue(new Error('usage service unavailable'))
  render(<StatusBar />)
  const toggle = openDetails()
  const usage = screen.getByRole('button', { name: 'Usage' })
  fireEvent.click(usage)
  const refresh = await screen.findByRole('button', { name: 'Refresh usage' })
  fireEvent.pointerDown(refresh)
  expect(toggle.getAttribute('aria-expanded')).toBe('true')
  expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'usage service unavailable')

  resize(390)
  refresh.focus()
  await userEvent.keyboard('{Escape}')
  await vi.waitFor(() => expect(document.activeElement).toBe(usage))
  expect(toggle.getAttribute('aria-expanded')).toBe('true')

  await userEvent.keyboard('{Escape}')
  expect(toggle.getAttribute('aria-expanded')).toBe('false')
  expect(document.activeElement).toBe(toggle)
})

test('team refresh survives disclosure and viewport changes without another lifecycle', async () => {
  vi.useFakeTimers()
  const resize = atViewport(1440)
  seed()
  vi.mocked(api.approvalList).mockResolvedValue([approval()])
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
  })
  const view = render(<StatusBar />)
  try {
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(api.presenceRoster).toHaveBeenCalledTimes(1)
    expect(api.presenceHeartbeat).toHaveBeenCalledTimes(1)

    const toggle = openDetails()
    expect(screen.getByText('$0.50')).toBeTruthy()
    fireEvent.click(toggle)
    resize(390)
    expect(screen.getByRole('button', { name: '1 waiting' })).toBeTruthy()
    openDetails()
    resize(1440)
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(api.presenceRoster).toHaveBeenCalledTimes(1)
    expect(api.presenceHeartbeat).toHaveBeenCalledTimes(1)
    expect(screen.getByText('$0.50')).toBeTruthy()

    await act(() => vi.advanceTimersByTimeAsync(15_000))
    expect(api.presenceHeartbeat).toHaveBeenCalledTimes(2)
  } finally {
    view.unmount()
    vi.useRealTimers()
  }
})

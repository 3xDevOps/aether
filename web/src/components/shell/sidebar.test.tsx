import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { Sidebar } from '@/components/shell/sidebar'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { hydrate } from '@/store/sync'
import {
  approval,
  bob,
  fakeApi,
  otherWorkspace,
  run,
  serverInfo,
  vera,
  workspace,
} from '@/test/fixtures'
import { pickOption } from '@/test/select'
import { atViewport } from '@/test/viewport'

/**
 * A window narrow enough for the sidebar's own threshold and no narrower, so
 * the rail tests below stay clear of the drawer layout; `phoneWidth` is past
 * the 640px line, where the expanded pane becomes a modal drawer.
 */
const narrowWidth = 800
const wideWidth = 1200
const phoneWidth = 390

beforeEach(async () => {
  useStore.setState({
    sidebarCollapsed: false,
    activeWorkspace: '',
    groupBy: 'status',
    inbox: {},
    route: { name: 'overview', params: {} },
  })
  await hydrate(useStore, fakeApi())
})

describe('Sidebar', () => {
  it('shows the active workspace and its runs', () => {
    render(<Sidebar />)

    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()
    expect(useStore.getState().activeWorkspace).toBe(workspace.id)
  })

  it('collapses to the rail on a narrow window', () => {
    atViewport(narrowWidth)
    render(<Sidebar />)

    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
    expect(useStore.getState().sidebarCollapsed).toBe(false)
  })

  it('toggles the sidebar from Ctrl+B without involving terminal input', () => {
    render(<Sidebar />)
    const terminal = document.createElement('div')
    terminal.className = 'xterm'
    document.body.append(terminal)
    onTestFinished(() => terminal.remove())

    fireEvent.keyDown(terminal, { key: 'b', ctrlKey: true })
    expect(useStore.getState().sidebarCollapsed).toBe(false)

    fireEvent.keyDown(window, { key: 'b', ctrlKey: true })
    expect(useStore.getState().sidebarCollapsed).toBe(true)
    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()

    fireEvent.keyDown(window, { key: 'b', ctrlKey: true })
    expect(useStore.getState().sidebarCollapsed).toBe(false)
    expect(screen.getByRole('complementary', { name: 'Runs' })).toBeDefined()
  })

  it('toggles at a narrow width without writing the stored preference', () => {
    // A member who stored a collapsed sidebar, then narrows the window and
    // glances at the run list, must not have that glance stored.
    useStore.setState({ sidebarCollapsed: true })
    atViewport(narrowWidth)
    render(<Sidebar />)

    fireEvent.click(screen.getByLabelText('Expand sidebar'))
    expect(screen.getByRole('complementary', { name: 'Runs' })).toBeDefined()
    expect(useStore.getState().sidebarCollapsed).toBe(true)

    fireEvent.click(screen.getByLabelText('Collapse sidebar'))
    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
    expect(useStore.getState().sidebarCollapsed).toBe(true)
  })

  it('drops the narrow-window expansion when the window widens', () => {
    useStore.setState({ sidebarCollapsed: true })
    const resize = atViewport(narrowWidth)
    render(<Sidebar />)
    fireEvent.click(screen.getByLabelText('Expand sidebar'))

    resize(wideWidth)

    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
    expect(useStore.getState().sidebarCollapsed).toBe(true)

    // The next narrowing starts from the rail again: had the glance survived
    // the widening, the sidebar would open itself here.
    resize(narrowWidth)

    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
  })

  it('opens the run list as a modal drawer on a phone', () => {
    atViewport(phoneWidth, { pointer: 'coarse' })
    render(<Sidebar />)

    fireEvent.click(screen.getByLabelText('Expand sidebar'))
    const drawer = screen.getByRole('dialog', { name: 'Runs' })
    // The rail travels with the drawer, so every surface stays one tap away.
    expect(within(drawer).getByRole('navigation', { name: 'Surfaces' })).toBeDefined()

    fireEvent.keyDown(document, { key: 'Escape' })

    expect(screen.queryByRole('dialog', { name: 'Runs' })).toBeNull()
    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
  })

  // A dialog stands the shell's global keys down inside itself, so without
  // the drawer answering it the key that opened the drawer could not close
  // it again.
  it('closes the phone drawer from the key that opened it', () => {
    atViewport(phoneWidth, { pointer: 'coarse' })
    render(<Sidebar />)

    fireEvent.keyDown(window, { key: 'b', ctrlKey: true })
    const drawer = screen.getByRole('dialog', { name: 'Runs' })

    fireEvent.keyDown(drawer, { key: 'b', ctrlKey: true })

    expect(screen.queryByRole('dialog', { name: 'Runs' })).toBeNull()
    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
  })

  it('closes the phone drawer after it navigates', () => {
    atViewport(phoneWidth, { pointer: 'coarse' })
    render(<Sidebar />)
    fireEvent.click(screen.getByLabelText('Expand sidebar'))

    const drawer = screen.getByRole('dialog', { name: 'Runs' })
    fireEvent.click(
      within(drawer).getByRole('button', { name: /rewrite the checkout flow/ }),
    )

    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: 'run_1' },
    })
    expect(screen.queryByRole('dialog', { name: 'Runs' })).toBeNull()
  })

  it('routes to a run when its row is clicked', () => {
    render(<Sidebar />)

    fireEvent.click(screen.getByText('rewrite the checkout flow'))

    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: 'run_1' },
    })
  })

  it('keeps the active row selected across the run tabs', () => {
    render(<Sidebar />)
    act(() =>
      useStore.setState({ route: { name: 'diff', params: { runId: 'run_1' } } }),
    )

    expect(
      screen
        .getByRole('button', { name: /rewrite the checkout flow/ })
        .getAttribute('aria-current'),
    ).toBe('page')
  })

  it('lights the open run alone, and no row off the run tabs', () => {
    const other = run({ id: 'run_api', task: 'tune the rate limiter' })
    act(() =>
      useStore.setState((s) => ({
        runs: { ...s.runs, [other.id]: toRecord(other) },
      })),
    )
    render(<Sidebar />)
    const row = (task: string) =>
      screen.getByRole('button', { name: new RegExp(task) })

    act(() =>
      useStore.setState({
        route: { name: 'terminal', params: { runId: 'run_1' } },
      }),
    )
    expect(row('rewrite the checkout flow').getAttribute('aria-current')).toBe('page')
    expect(row('tune the rate limiter').getAttribute('aria-current')).toBeNull()

    act(() => useStore.setState({ route: { name: 'board', params: {} } }))
    expect(row('rewrite the checkout flow').getAttribute('aria-current')).toBeNull()
  })

  it('switches workspace, rescoping the run list', async () => {
    // Two workspaces means a picker; one run apiece, so the list is proof
    // that the switch is what scopes the tree.
    const elsewhere = run({
      id: 'run_docs',
      task: 'refresh the install guide',
      workspace_id: otherWorkspace.id,
    })
    act(() =>
      useStore.setState((s) => ({
        runs: { ...s.runs, [elsewhere.id]: toRecord(elsewhere) },
      })),
    )
    render(<Sidebar />)

    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()
    expect(screen.queryByText('refresh the install guide')).toBeNull()

    await pickOption(screen.getByLabelText('Workspace'), otherWorkspace.name)

    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
    expect(screen.getByText('refresh the install guide')).toBeDefined()
    expect(screen.queryByText('rewrite the checkout flow')).toBeNull()
  })

  it('names the sole workspace instead of offering a picker', () => {
    act(() =>
      useStore.setState({ workspaces: { [workspace.id]: workspace } }),
    )
    render(<Sidebar />)

    expect(screen.queryByLabelText('Workspace')).toBeNull()
    expect(screen.getByText(workspace.name)).toBeDefined()
    expect(screen.getByText(workspace.base_branch)).toBeDefined()
  })

  it('follows a live status change', () => {
    render(<Sidebar />)
    expect(screen.getAllByTitle('Working').length).toBeGreaterThan(0)

    act(() =>
      useStore
        .getState()
        .applyRunStatus(
          'run_1',
          'needs-attention',
          'plan review',
          '2026-08-14T11:00:00Z',
        ),
    )

    expect(screen.getAllByTitle('Needs you').length).toBeGreaterThan(0)
  })

  it('badges how many runs are waiting on a human', () => {
    render(<Sidebar />)
    expect(screen.queryByLabelText(/needs? you/i)).toBeNull()

    // A stall parks the run at needs-attention; the badge is how the
    // dashboard says so without the member reading every row.
    act(() =>
      useStore
        .getState()
        .applyRunStatus(
          'run_1',
          'needs-attention',
          'stalled: no output or file changes for 10m0s',
          '2026-08-14T11:00:00Z',
        ),
    )

    const badge = screen.getByLabelText('1 run needs you')
    expect(badge.textContent).toBe('1')
  })

  it('surfaces a run waiting on an approval as needs-attention', () => {
    useStore.setState({ inbox: {} })
    render(<Sidebar />)
    expect(screen.queryByTitle('Needs you')).toBeNull()

    // The run still reads `running`; the pending inbox entry is the signal.
    act(() => useStore.getState().setInbox(workspace.id, [approval()]))

    expect(screen.getAllByTitle('Needs you').length).toBeGreaterThan(0)
    // The run groups under Needs you, so the attention sort surfaces it.
    expect(screen.getByRole('heading', { name: /^Needs you/ })).toBeDefined()
  })

  it('starts the Done status group collapsed with its run count visible', () => {
    const done = run({
      id: 'run_done',
      task: 'publish the finished checkout',
      status: 'completed',
    })
    act(() =>
      useStore.setState((s) => ({
        runs: { ...s.runs, [done.id]: toRecord(done) },
      })),
    )
    render(<Sidebar />)

    const doneHeader = screen.getByRole('button', { name: /^Done/, expanded: false })
    expect(doneHeader.getAttribute('aria-expanded')).toBe('false')
    expect(doneHeader.textContent).toContain('1')
    expect(screen.queryByText(done.task)).toBeNull()

    const region = document.getElementById(doneHeader.getAttribute('aria-controls') ?? '')
    expect(region?.hasAttribute('hidden')).toBe(true)

    fireEvent.click(doneHeader)

    expect(doneHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText(done.task)).toBeDefined()
  })

  it('toggles a status group without changing its run ordering', () => {
    render(<Sidebar />)

    const workingHeader = screen.getByRole('button', { name: /^Working/, expanded: true })
    expect(workingHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()

    fireEvent.click(workingHeader)

    expect(workingHeader.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('rewrite the checkout flow')).toBeNull()

    fireEvent.click(workingHeader)

    expect(workingHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()
  })

  it('toggles one member group while leaving other member rows visible', () => {
    const bobRun = run({
      id: 'run_bob',
      member_id: bob.id,
      account_member_id: bob.id,
      task: 'tune the rate limiter',
    })
    act(() =>
      useStore.setState((s) => ({
        runs: { ...s.runs, [bobRun.id]: toRecord(bobRun) },
      })),
    )
    render(<Sidebar />)
    fireEvent.click(
      within(screen.getByRole('group', { name: 'Group runs by' })).getByRole('button', {
        name: 'Member',
      }),
    )

    const aliceHeader = screen.getByRole('button', { name: /^Alice/, expanded: true })
    expect(aliceHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()
    expect(screen.getByText(bobRun.task)).toBeDefined()

    fireEvent.click(aliceHeader)

    expect(aliceHeader.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('rewrite the checkout flow')).toBeNull()
    expect(screen.getByText(bobRun.task)).toBeDefined()

    fireEvent.click(aliceHeader)

    expect(aliceHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()
  })

  it('keeps disclosure state isolated between grouping modes with the same key', () => {
    const sameKey = run({
      id: 'run_same_key',
      member_id: 'done',
      account_member_id: 'done',
      task: 'inspect the matching key',
      status: 'completed',
    })
    act(() =>
      useStore.setState((s) => ({
        runs: { ...s.runs, [sameKey.id]: toRecord(sameKey) },
      })),
    )
    render(<Sidebar />)

    const groupBy = within(screen.getByRole('group', { name: 'Group runs by' }))
    const doneStatusHeader = screen.getByRole('button', { name: /^Done/, expanded: false })
    expect(doneStatusHeader.getAttribute('aria-expanded')).toBe('false')

    fireEvent.click(groupBy.getByRole('button', { name: 'Member' }))

    const doneMemberHeader = screen.getByRole('button', { name: /^done/, expanded: true })
    expect(doneMemberHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText(sameKey.task)).toBeDefined()

    fireEvent.click(doneMemberHeader)
    expect(doneMemberHeader.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText(sameKey.task)).toBeNull()

    fireEvent.click(groupBy.getByRole('button', { name: 'Status' }))
    expect(
      screen.getByRole('button', { name: /^Done/, expanded: false }).getAttribute('aria-expanded'),
    ).toBe(
      'false',
    )

    fireEvent.click(groupBy.getByRole('button', { name: 'Member' }))
    expect(
      screen.getByRole('button', { name: /^done/, expanded: false }).getAttribute('aria-expanded'),
    ).toBe(
      'false',
    )
  })


  it('offers a new run to a member who may start one', () => {
    useStore.setState({ info: { ...serverInfo, member: bob } })
    render(<Sidebar />)

    fireEvent.click(screen.getByText('New run'))

    // The form is hosted app-wide; the sidebar only asks for it.
    expect(useStore.getState().paletteDialog).toBe('launch')
  })

  it('offers no new run to a viewer', () => {
    // A viewer cannot own a run, so the server refuses the launch; do not
    // draw the button that would be refused.
    useStore.setState({ info: { ...serverInfo, member: vera } })
    render(<Sidebar />)

    expect(screen.queryByText('New run')).toBeNull()
  })

  it('leads the nav with the two whole-workspace views, marking the active one', () => {
    useStore.setState({ route: { name: 'board', params: {} } })
    render(<Sidebar />)
    const surfaces = within(screen.getByLabelText('Surfaces'))

    const names = surfaces.getAllByRole('button').map((b) => b.textContent)
    expect(names.slice(0, 2)).toEqual(['Board', 'All runs'])

    const current = () =>
      surfaces.getAllByRole('button').find((b) => b.getAttribute('aria-current') === 'page')
    expect(current()?.textContent).toBe('Board')

    fireEvent.click(surfaces.getByText('All runs'))
    expect(current()?.textContent).toBe('All runs')
  })

  it('opens the approval inbox and the activity feed from the nav', () => {
    render(<Sidebar />)
    const surfaces = within(screen.getByLabelText('Surfaces'))

    fireEvent.click(surfaces.getByText('Approvals'))
    expect(useStore.getState().route).toEqual({ name: 'approvals', params: {} })

    fireEvent.click(surfaces.getByText('Activity'))
    expect(useStore.getState().route).toEqual({ name: 'timeline', params: {} })
  })

  it('counts the waiting requests on the Approvals entry', () => {
    useStore.setState({ inbox: {} })
    render(<Sidebar />)
    const surfaces = within(screen.getByLabelText('Surfaces'))
    expect(surfaces.getByRole('button', { name: 'Approvals' })).toBeDefined()

    act(() => useStore.getState().setInbox(workspace.id, [approval()]))

    // The count is part of the entry's name, not a second thing to find: it
    // sits inside the button, so a reader hears one control, not two.
    const entry = surfaces.getByRole('button', {
      name: 'Approvals, 1 waiting on a decision',
    })
    expect(entry.textContent).toContain('1')
  })

  // CLAUDE.md: show the real error, never a friendlier stand-in. The badge
  // that marks the failure is decorative, so the entry's name is the only
  // place a reader meets what the server actually said.
  it('names the queue error the server reported on the Approvals entry', () => {
    useStore.setState({ inboxError: 'approval.list: database is locked' })
    render(<Sidebar />)

    expect(
      within(screen.getByLabelText('Surfaces')).getByRole('button', {
        name: 'Approvals, approval.list: database is locked',
      }),
    ).toBeDefined()
  })

  it('groups the runs by the pressed segment', () => {
    render(<Sidebar />)
    const control = within(screen.getByRole('group', { name: 'Group runs by' }))
    const status = control.getByRole('button', { name: 'Status' })
    const member = control.getByRole('button', { name: 'Member' })

    // Both choices are on screen, so the pressed one is the state and the
    // other one is the action.
    expect(status.getAttribute('aria-pressed')).toBe('true')
    expect(member.getAttribute('aria-pressed')).toBe('false')

    fireEvent.click(member)

    expect(useStore.getState().groupBy).toBe('member')
    expect(member.getAttribute('aria-pressed')).toBe('true')
    expect(status.getAttribute('aria-pressed')).toBe('false')
    // The runs regroup under their owner rather than their state.
    expect(screen.getByRole('heading', { name: /^Alice/ })).toBeDefined()
  })

  it('shows the admin and desktop surfaces the gateway can serve', () => {
    useStore.setState({
      capabilities: {
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach', 'terminal'],
        local: ['link.status', 'daemon.status', 'pull'],
      },
    })
    render(<Sidebar />)
    const surfaces = within(screen.getByLabelText('Surfaces'))

    fireEvent.click(surfaces.getByText('Members'))

    expect(useStore.getState().route).toEqual({ name: 'members', params: {} })
    expect(surfaces.getByText('Files')).toBeDefined()
    expect(surfaces.getByText('Onboarding')).toBeDefined()
    expect(surfaces.getByText('Settings')).toBeDefined()
  })

  it('draws only what a narrow gateway can serve, and the two ungated views', () => {
    // A gateway advertising a list with neither approval.list nor
    // workspace.timeline on it: the methods behind those views would fail, so
    // neither way in is drawn. member.list is on it and the roster is readable
    // by everyone, so Members stays. Board and All runs are never gated -
    // they are views of the runs the sidebar already has.
    useStore.setState({
      capabilities: {
        gateway: 'remote',
        methods: ['run.list', 'run.get', 'member.list'],
        ws: ['events', 'attach'],
      },
    })
    render(<Sidebar />)

    const surfaces = within(screen.getByLabelText('Surfaces'))
    expect(surfaces.getByText('Board')).toBeDefined()
    expect(surfaces.getByText('All runs')).toBeDefined()
    expect(surfaces.getByText('Members')).toBeDefined()
    expect(surfaces.queryByText('Approvals')).toBeNull()
    expect(surfaces.queryByText('Activity')).toBeNull()
    expect(surfaces.queryByText('Manage workspaces')).toBeNull()
    expect(surfaces.queryByText('Onboarding')).toBeNull()
    expect(surfaces.queryByText('Settings')).toBeNull()
  })

  it('shows the read surfaces on a legacy monitor without capabilities', () => {
    // The capabilities endpoint 404ed: only the pre-capabilities allowlist
    // may be assumed. approval.list, workspace.timeline and member.list are
    // all on it, so the inbox, the feed and the roster stay reachable; no
    // admin method is on it, so nothing behind one is drawn.
    useStore.setState({ capabilities: null })
    render(<Sidebar />)

    const surfaces = within(screen.getByLabelText('Surfaces'))
    expect(surfaces.getByText('Approvals')).toBeDefined()
    expect(surfaces.getByText('Activity')).toBeDefined()
    expect(surfaces.getByText('Members')).toBeDefined()
    expect(surfaces.queryByText('Templates')).toBeNull()
    expect(surfaces.queryByText('Agents')).toBeNull()
  })
})

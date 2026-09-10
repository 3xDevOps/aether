import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { Sidebar } from '@/components/shell/sidebar'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { hydrate } from '@/store/sync'
import {
  alice,
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

afterEach(() => vi.unstubAllGlobals())

/**
 * A narrow window: only the sidebar's own threshold matches, so a component
 * asking a different question gets the wide answer. Returns the resize the
 * component listens for.
 */
function narrowWindow() {
  const wide = window.matchMedia
  const listeners = new Set<(e: MediaQueryListEvent) => void>()
  vi.stubGlobal('matchMedia', (query: string) => ({
    ...wide(query),
    matches: query === '(max-width: 1000px)',
    addEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) =>
      listeners.add(fn),
    removeEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) =>
      listeners.delete(fn),
  }))
  return (matches: boolean) =>
    act(() => {
      for (const fn of listeners) fn({ matches } as MediaQueryListEvent)
    })
}

describe('Sidebar', () => {
  it('shows the active workspace and its runs', () => {
    render(<Sidebar />)

    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()
    expect(useStore.getState().activeWorkspace).toBe(workspace.id)
  })

  it('collapses to the rail on a narrow window', () => {
    narrowWindow()
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
    narrowWindow()
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
    const resize = narrowWindow()
    render(<Sidebar />)
    fireEvent.click(screen.getByLabelText('Expand sidebar'))

    resize(false)

    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
    expect(useStore.getState().sidebarCollapsed).toBe(true)

    // The next narrowing starts from the rail again: had the glance survived
    // the widening, the sidebar would open itself here.
    resize(true)

    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
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
    expect(screen.getByRole('heading', { name: 'Needs you' })).toBeDefined()
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
    expect(screen.getByRole('heading', { name: alice.display_name })).toBeDefined()
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

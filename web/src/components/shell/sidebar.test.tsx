import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { Sidebar } from '@/components/shell/sidebar'
import { ViewHeader } from '@/components/view-header'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { hydrate } from '@/store/sync'
import { approval, fakeApi, otherWorkspace, run, workspace } from '@/test/fixtures'

beforeEach(async () => {
  useStore.setState({
    sidebarCollapsed: false,
    activeWorkspace: '',
    groupBy: 'status',
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

  it('routes to a run when its row is clicked', () => {
    render(<Sidebar />)

    fireEvent.click(screen.getByText('rewrite the checkout flow'))

    expect(useStore.getState().route).toEqual({
      name: 'run',
      params: { runId: 'run_1' },
    })
  })

  it('switches workspace, rescoping the run list', () => {
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

    const picker = screen.getByLabelText('Workspace')
    fireEvent.change(picker, { target: { value: otherWorkspace.id } })

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

  it('keeps the explorer and main top bars the same height', () => {
    render(
      <>
        <Sidebar />
        <ViewHeader title="All runs" />
      </>,
    )

    const sidebar = screen.getByRole('complementary', { name: 'Runs' })
    expect(sidebar.firstElementChild?.className).toContain('h-9')
    expect(screen.getByRole('banner').className).toContain('h-9')
  })

  it('shows the admin and desktop surfaces the gateway can serve', () => {
    useStore.setState({
      capabilities: {
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach', 'shell'],
        local: ['link.status', 'daemon.status', 'pull'],
      },
    })
    render(<Sidebar />)

    fireEvent.click(screen.getByText('Members'))

    expect(useStore.getState().route).toEqual({ name: 'members', params: {} })
    expect(screen.getByText('Onboarding')).toBeDefined()
    expect(screen.getByText('Settings')).toBeDefined()
  })

  it('keeps Members reachable behind the narrow remote allowlist', () => {
    // The remote gateway advertises its allowlist: no admin methods and no
    // local verbs, but member.list is on it, and the roster is readable by
    // everyone, so Members is the one entry point that stays.
    useStore.setState({
      capabilities: {
        gateway: 'remote',
        methods: ['run.list', 'run.get', 'member.list'],
        ws: ['events', 'attach'],
      },
    })
    render(<Sidebar />)

    const surfaces = within(screen.getByLabelText('Surfaces'))
    expect(surfaces.getByText('Members')).toBeDefined()
    expect(surfaces.queryByText('Manage workspaces')).toBeNull()
    expect(surfaces.queryByText('Onboarding')).toBeNull()
    expect(surfaces.queryByText('Settings')).toBeNull()
  })

  it('shows only Members on a legacy monitor without capabilities', () => {
    // The capabilities endpoint 404ed: only the pre-capabilities allowlist
    // may be assumed. member.list is on it; no admin method is.
    useStore.setState({ capabilities: null })
    render(<Sidebar />)

    const surfaces = within(screen.getByLabelText('Surfaces'))
    expect(surfaces.getByText('Members')).toBeDefined()
    expect(surfaces.queryByText('Templates')).toBeNull()
    expect(surfaces.queryByText('Agents')).toBeNull()
  })
})

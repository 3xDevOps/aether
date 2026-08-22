import { act, fireEvent, render, screen } from '@testing-library/react'
import { Sidebar } from '@/components/shell/sidebar'
import { useStore } from '@/store'
import { hydrate } from '@/store/sync'
import { approval, fakeApi, session } from '@/test/fixtures'

beforeEach(async () => {
  useStore.setState({
    sidebarCollapsed: false,
    collapsedSessions: [],
    groupBy: 'status',
    route: { name: 'overview', params: {} },
  })
  await hydrate(useStore, fakeApi())
})

describe('Sidebar', () => {
  it('shows hydrated sessions with their runs nested', () => {
    render(<Sidebar />)

    expect(screen.getByText('checkout rewrite')).toBeDefined()
    expect(screen.getByText('rewrite the checkout flow')).toBeDefined()
  })

  it('routes to a run when its row is clicked', () => {
    render(<Sidebar />)

    fireEvent.click(screen.getByText('rewrite the checkout flow'))

    expect(useStore.getState().route).toEqual({
      name: 'run',
      params: { runId: 'run_1' },
    })
  })

  it('collapses a session, hiding its runs', () => {
    render(<Sidebar />)

    fireEvent.click(screen.getAllByLabelText('Collapse session')[0])

    expect(screen.queryByText('rewrite the checkout flow')).toBeNull()
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
    act(() => useStore.getState().setInbox(session.id, [approval()]))

    expect(screen.getAllByTitle('Needs you').length).toBeGreaterThan(0)
    // The session groups under Needs you, so the attention sort surfaces it.
    expect(screen.getByRole('heading', { name: 'Needs you' })).toBeDefined()
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

  it('shows no surface links behind the remote allowlist', () => {
    // The remote gateway advertises its allowlist: no admin methods, no
    // local verbs, so no new entry points appear.
    useStore.setState({
      capabilities: {
        gateway: 'remote',
        methods: ['run.list', 'run.get', 'member.list'],
        ws: ['events', 'attach'],
      },
    })
    render(<Sidebar />)

    expect(screen.queryByLabelText('Surfaces')).toBeNull()
  })

  it('shows no surface links on a legacy monitor without capabilities', () => {
    // The capabilities endpoint 404ed: only the pre-capabilities allowlist
    // may be assumed, and no admin method is on it.
    useStore.setState({ capabilities: null })
    render(<Sidebar />)

    expect(screen.queryByLabelText('Surfaces')).toBeNull()
  })
})

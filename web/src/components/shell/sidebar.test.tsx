import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Sidebar } from '@/components/shell/sidebar'
import { TitleBar } from '@/components/shell/title-bar'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { hydrate } from '@/store/sync'
import {
  approval,
  bob,
  fakeApi,
  otherWorkspace,
  run,
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
    sidebarDrawerOpen: false,
    activeWorkspace: '',
    groupBy: 'status',
    inbox: {},
    roomMessages: {},
    inboxError: null,
    route: { name: 'overview', params: {} },
  })
  await hydrate(useStore, fakeApi())
})

afterEach(() => {
  vi.restoreAllMocks()
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

  it('opens the phone rail only in the drawer and returns focus to the titlebar', async () => {
    atViewport(phoneWidth, { pointer: 'coarse' })
    render(<><TitleBar /><Sidebar /></>)
    const opener = screen.getByRole('button', { name: 'Expand sidebar' })
    expect(screen.queryByRole('navigation', { name: 'Surfaces' })).toBeNull()
    opener.focus()
    fireEvent.click(opener)
    const drawer = screen.getByRole('dialog', { name: 'Runs' })
    expect(within(drawer).getByRole('navigation', { name: 'Surfaces' })).toBeDefined()
    expect(drawer.contains(document.activeElement)).toBe(true)
    const last = within(drawer).getAllByRole('button').at(-1)!
    last.focus()
    await userEvent.tab()
    expect(drawer.contains(document.activeElement)).toBe(true)

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog', { name: 'Runs' })).toBeNull()
    expect(screen.queryByRole('navigation', { name: 'Surfaces' })).toBeNull()
    await waitFor(() => expect(document.activeElement).toBe(opener))
    expect(useStore.getState().sidebarCollapsed).toBe(false)
  })

  // A dialog stands the shell's global keys down inside itself, so without
  // the drawer answering it the key that opened the drawer could not close
  // it again.
  it('closes the phone drawer from the key that opened it', () => {
    atViewport(phoneWidth, { pointer: 'coarse' })
    render(<><TitleBar /><Sidebar /></>)

    fireEvent.keyDown(window, { key: 'b', ctrlKey: true })
    const drawer = screen.getByRole('dialog', { name: 'Runs' })

    // Off Apple platforms Meta+B is not the binding, inside the drawer or out.
    fireEvent.keyDown(drawer, { key: 'b', metaKey: true })
    expect(screen.getByRole('dialog', { name: 'Runs' })).toBeDefined()

    fireEvent.keyDown(drawer, { key: 'b', ctrlKey: true })

    expect(screen.queryByRole('dialog', { name: 'Runs' })).toBeNull()
    expect(screen.getByLabelText('Expand sidebar')).toBeDefined()
  })

  it('closes the phone drawer after it navigates', () => {
    atViewport(phoneWidth, { pointer: 'coarse' })
    render(<><TitleBar /><Sidebar /></>)
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

  it('does not carry an open phone drawer through widening or unmounting', () => {
    const resize = atViewport(phoneWidth)
    const { unmount } = render(<><TitleBar /><Sidebar /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Expand sidebar' }))
    resize(wideWidth)
    expect(useStore.getState().sidebarDrawerOpen).toBe(false)
    expect(useStore.getState().sidebarCollapsed).toBe(false)
    resize(phoneWidth)
    expect(screen.queryByRole('dialog', { name: 'Runs' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Expand sidebar' }))
    unmount()
    expect(useStore.getState().sidebarDrawerOpen).toBe(false)
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

    await pickOption(screen.getByRole('combobox', { name: 'Workspace' }), otherWorkspace.name)

    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
    expect(screen.getByText('refresh the install guide')).toBeDefined()
    expect(screen.queryByText('rewrite the checkout flow')).toBeNull()
  })

  it('names the sole workspace instead of offering a picker', () => {
    act(() =>
      useStore.setState({ workspaces: { [workspace.id]: workspace } }),
    )
    render(<Sidebar />)

    expect(screen.queryByRole('combobox', { name: 'Workspace' })).toBeNull()
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

    expect(screen.getAllByTitle('Idle').length).toBeGreaterThan(0)
  })

  it('does not infer input from a stalled or idle run', () => {
    render(<Sidebar />)
    expect(screen.queryByLabelText(/\d+ runs? needs? input/i)).toBeNull()

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

    expect(screen.queryByLabelText(/\d+ runs? needs? input/i)).toBeNull()
    expect(screen.queryByRole('img', { name: /Needs input:/ })).toBeNull()
  })

  it('badges an approval without moving a busy run into Idle', () => {
    useStore.setState({ inbox: {} })
    render(<Sidebar />)
    expect(screen.queryByTitle('Idle')).toBeNull()

    // The run still reads `running`; the pending inbox entry is the signal.
    act(() => useStore.getState().setInbox(workspace.id, [approval()]))

    expect(screen.getByLabelText('1 run needs input')).toBeDefined()
    expect(screen.getByRole('img', { name: /Needs input: 1 approval/ })).toBeDefined()
    expect(screen.getByRole('heading', { name: /^Working/ })).toBeDefined()
    expect(screen.queryByRole('heading', { name: /^Idle/ })).toBeNull()

    act(() => useStore.getState().setInbox(workspace.id, [
      approval({ decision: 'approved' }),
    ]))
    expect(screen.queryByRole('img', { name: /Needs input:/ })).toBeNull()
    expect(screen.queryByLabelText('1 run needs input')).toBeNull()
    expect(screen.getByRole('button', { name: /rewrite the checkout flow/ })).toBeDefined()
    expect(screen.getByRole('heading', { name: /^Working/ })).toBeDefined()
  })

  it('keeps a finished unanswered run in its lifecycle group', () => {
    const finished = run({
      id: 'run_finished_question',
      task: 'answer after completion',
      status: 'failed',
      unanswered_questions: 1,
    })
    act(() =>
      useStore.setState((state) => ({
        runs: { ...state.runs, [finished.id]: toRecord(finished) },
      })),
    )
    render(<Sidebar />)

    expect(screen.getByRole('button', { name: /^Failed/, expanded: true })).toBeDefined()
    expect(screen.getByRole('img', { name: /Needs input: 1 unanswered question/ })).toBeDefined()
    expect(screen.getByText('answer after completion')).toBeDefined()
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


  it('marks the active workspace destination without offering All runs in the rail', () => {
    useStore.setState({ route: { name: 'board', params: {} } })
    render(<Sidebar />)
    const surfaces = within(screen.getByLabelText('Surfaces'))
    expect(surfaces.queryByRole('button', { name: 'All runs' })).toBeNull()
    expect(surfaces.getByRole('button', { name: 'Board' }).getAttribute('aria-current')).toBe('page')

    fireEvent.click(surfaces.getByRole('button', { name: 'Activity' }))
    expect(surfaces.getByRole('button', { name: 'Activity' }).getAttribute('aria-current')).toBe('page')
    expect(surfaces.getByRole('button', { name: 'Board' }).getAttribute('aria-current')).toBeNull()
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

  it('keeps every permitted destination reachable as the available rail height changes', async () => {
    atViewport(960, { height: 600 })
    useStore.setState({
      capabilities: { gateway: 'local', methods: ['*'], ws: [], local: ['link.status'] },
    })
    let railHeight = 543
    const bounds = HTMLElement.prototype.getBoundingClientRect
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      return this.getAttribute('aria-label') === 'Surfaces'
        ? { ...bounds.call(this), height: railHeight } as DOMRect
        : bounds.call(this)
    })
    let resize = () => {}
    vi.stubGlobal('ResizeObserver', class {
      private callback: () => void
      constructor(callback: () => void) { this.callback = callback }
      observe(element: Element) {
        if (element.getAttribute('aria-label') === 'Surfaces') resize = this.callback
      }
      unobserve() {}
      disconnect() {}
    })
    onTestFinished(() => { vi.unstubAllGlobals() })
    render(<Sidebar />)

    const destinations = [
      ['Board', 'board'], ['Missions', 'missions'], ['Approvals', 'approvals'],
      ['Activity', 'timeline'], ['Files', 'files'], ['Templates', 'templates'],
      ['Agents', 'agents'], ['Configuration', 'configuration'],
      ['Members', 'members'], ['Devices', 'devices'], ['Manage workspaces', 'workspaces'],
      ['Onboarding', 'onboarding'], ['Settings', 'settings'],
    ]
    for (const [label, name] of destinations) {
      const direct = screen.queryByRole('button', { name: label })
      if (direct) fireEvent.click(direct)
      else {
        const menu = ['members', 'devices', 'workspaces', 'onboarding'].includes(name) ? 'Admin' : 'More'
        fireEvent.keyDown(screen.getByRole('button', { name: new RegExp(`^${menu}`) }), { key: 'ArrowDown' })
        const item = await screen.findByRole('menuitem', { name: label })
        fireEvent.click(item)
      }
      expect(useStore.getState().route.name).toBe(name)
    }
    act(() => {
      railHeight = 900
      resize()
    })
    expect(screen.queryByRole('button', { name: /^More/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Configuration' })).toBeDefined()

    act(() => {
      useStore.setState({ route: { name: 'configuration', params: {} } })
      railHeight = 543
      resize()
    })
    const more = screen.getByRole('button', { name: 'More, Configuration' })
    fireEvent.keyDown(more, { key: 'ArrowDown' })
    expect((await screen.findByRole('menuitem', { name: 'Configuration' })).getAttribute('aria-current')).toBe('page')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(more))
  })

  it('retains approval counts and server errors when the inbox is in More', async () => {
    const bounds = HTMLElement.prototype.getBoundingClientRect
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      return this.getAttribute('aria-label') === 'Surfaces'
        ? { ...bounds.call(this), height: 200 } as DOMRect
        : bounds.call(this)
    })
    useStore.getState().setInbox(workspace.id, [approval()])
    render(<Sidebar />)
    const more = screen.getByRole('button', { name: 'More, Approvals, 1 waiting on a decision' })
    fireEvent.keyDown(more, { key: 'ArrowDown' })
    const approvalItem = await screen.findByRole('menuitem', { name: 'Approvals, 1 waiting on a decision' })
    fireEvent.click(approvalItem)
    expect(useStore.getState().route.name).toBe('approvals')
    act(() => useStore.setState({ inboxError: 'approval.list: database is locked' }))
    fireEvent.keyDown(screen.getByRole('button', { name: /More.*approval.list: database is locked/ }), { key: 'ArrowDown' })
    expect((await screen.findByRole('menuitem', { name: 'Approvals, approval.list: database is locked' })).getAttribute('aria-current')).toBe('page')
  })

  it('keeps Settings reachable without exposing unavailable gateway destinations', async () => {
    useStore.setState({
      capabilities: {
        gateway: 'remote',
        methods: ['run.list', 'run.get', 'member.list'],
        ws: ['events', 'attach'],
      },
    })
    render(<Sidebar />)
    const surfaces = within(screen.getByLabelText('Surfaces'))
    fireEvent.click(surfaces.getByRole('button', { name: 'Settings' }))
    expect(useStore.getState().route.name).toBe('settings')
    expect(surfaces.queryByRole('button', { name: 'Approvals' })).toBeNull()
    expect(surfaces.queryByRole('button', { name: 'Activity' })).toBeNull()
    fireEvent.keyDown(surfaces.getByRole('button', { name: 'Admin' }), { key: 'ArrowDown' })
    const members = await screen.findByRole('menuitem', { name: 'Members' })
    expect(screen.queryByRole('menuitem', { name: 'Manage workspaces' })).toBeNull()
    expect(screen.queryByRole('menuitem', { name: 'Onboarding' })).toBeNull()
    fireEvent.click(members)
    expect(useStore.getState().route.name).toBe('members')
  })

  it('shows legacy read destinations without assuming administrative capabilities', async () => {
    useStore.setState({ capabilities: null })
    render(<Sidebar />)
    const surfaces = within(screen.getByLabelText('Surfaces'))
    expect(surfaces.getByRole('button', { name: 'Approvals' })).toBeDefined()
    expect(surfaces.getByRole('button', { name: 'Activity' })).toBeDefined()
    expect(surfaces.getByRole('button', { name: 'Settings' })).toBeDefined()
    expect(surfaces.queryByRole('button', { name: 'Templates' })).toBeNull()
    expect(surfaces.queryByRole('button', { name: 'Agents' })).toBeNull()
    fireEvent.keyDown(surfaces.getByRole('button', { name: 'Admin' }), { key: 'ArrowDown' })
    expect(await screen.findByRole('menuitem', { name: 'Members' })).toBeDefined()
    expect(screen.queryByRole('menuitem', { name: 'Devices' })).toBeNull()
  })
})

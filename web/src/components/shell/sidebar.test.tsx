import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AppShell } from '@/components/shell/app-shell'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { hydrate } from '@/store/sync'
import { fakeApi, mission, otherWorkspace, run, updateStatus, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

Element.prototype.scrollIntoView = vi.fn()

const runs = [
  run(),
  run({ id: 'run_2', task: 'answer the schema question', status: 'needs-attention' }),
  run({ id: 'run_3', task: 'ship the invoice export', status: 'completed', outcome_unseen: true, workspace_id: otherWorkspace.id }),
  run({ id: 'run_4', task: 'tidy the readme', status: 'merged' }),
]

beforeEach(async () => {
  useStore.setState({
    sidebarCollapsed: false,
    sidebarDrawerOpen: false,
    sidebarWidth: 260,
    activeWorkspace: '',
    mineOnly: false,
    inbox: {},
    roomMessages: {},
    route: { name: 'board', params: {} },
    update: null,
    dismissedUpdates: { cli: '', server: '', shell: '' },
    updatesOpen: false,
    shortcutsOpen: false,
  })
  useStore.getState().setOnboarded(true)
  await hydrate(useStore, fakeApi({ workspaceListFull: vi.fn(async () => [workspace, otherWorkspace]) }))
  useStore.setState({
    activeWorkspace: workspace.id,
    runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach', 'terminal'], local: ['update.check'] },
  })
})

const nav = () => within(screen.getByRole('navigation', { name: 'Aether' }))
// jsdom counts a header inside main as a banner; a browser does not.
const topBanners = () => screen.queryAllByRole('banner').filter((header) => !header.closest('main'))
const runList = () => within(nav().getByRole('region', { name: 'Runs' }))

describe('shell landmarks', () => {
  it('puts the skip link first, then the sidebar, then one main with one h1', async () => {
    render(<AppShell />)

    await userEvent.tab()
    expect(document.activeElement?.textContent).toBe('Skip to content')
    fireEvent.click(document.activeElement!)
    expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: 'Board' }))

    expect(screen.getAllByRole('main')).toHaveLength(1)
    expect(within(screen.getByRole('main')).getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(topBanners()).toHaveLength(0)
    expect(screen.queryByRole('contentinfo')).toBeNull()
  })

  it('groups runs under an h2 each, Finished collapsed by default', () => {
    render(<AppShell />)

    const groups = runList().getAllByRole('heading', { level: 2 })
    expect(groups.map((heading) => heading.textContent)).toEqual(['Needs you2', 'Working1', 'Finished1'])
    const finished = runList().getByRole('button', { name: /^Finished/ })
    expect(finished.getAttribute('aria-expanded')).toBe('false')
    expect(runList().queryByText('tidy the readme')).toBeNull()
    fireEvent.click(finished)
    expect(runList().getByText('tidy the readme')).toBeDefined()
  })
})

describe('run rows', () => {
  it('names the state first, marks the open run, and opens its terminal', () => {
    render(<AppShell />)

    const row = runList().getByRole('button', { name: /^Working · rewrite the checkout flow/ })
    fireEvent.click(row)

    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: 'run_1' } })
    expect(row.getAttribute('aria-current')).toBe('page')
  })

  it('prefixes a Needs you row from another workspace and answers it where it waits', () => {
    render(<AppShell />)

    const row = runList().getByRole('button', { name: /^Needs you · docs-site · ship the invoice export/ })
    const item = row.closest('[data-slot="list-row"]') as HTMLElement
    fireEvent.click(within(item).getByRole('button', { name: 'Review' }))

    expect(useStore.getState().route).toEqual({ name: 'diff', params: { runId: 'run_3' } })
  })

  it('keeps one tab stop in the list and walks it with j, k and the arrows', () => {
    render(<AppShell />)
    const rows = runList().getAllByRole('button').filter((b) => b.hasAttribute('data-run-row'))
    expect(rows.filter((row) => row.tabIndex === 0)).toHaveLength(1)

    fireEvent.keyDown(document.body, { key: 'j' })
    expect(document.activeElement).toBe(rows[0])
    fireEvent.keyDown(document.body, { key: 'j' })
    expect(document.activeElement).toBe(rows[1])
    fireEvent.keyDown(rows[1], { key: 'ArrowUp' })
    expect(document.activeElement).toBe(rows[0])
    expect(rows[0].tabIndex).toBe(0)
    expect(rows[1].tabIndex).toBe(-1)
  })

  it('opens what a Needs you row waits on, not always its terminal', () => {
    render(<AppShell />)
    fireEvent.click(runList().getByRole('button', { name: /^Needs you · docs-site · ship the invoice export/ }))
    expect(useStore.getState().route).toEqual({ name: 'diff', params: { runId: 'run_3' } })
  })

  it('walks on from the open run when focus is outside the list', () => {
    act(() => useStore.getState().navigate('terminal', { runId: 'run_2' }))
    render(<AppShell />)
    const rows = runList().getAllByRole('button').filter((b) => b.hasAttribute('data-run-row'))
    const open = rows.findIndex((row) => row.getAttribute('aria-current') === 'page')

    fireEvent.keyDown(document.body, { key: 'j' })

    expect(document.activeElement).toBe(rows[open + 1])
  })

  it('opens the next run that needs you on u, oldest first, and wraps', () => {
    render(<AppShell />)

    fireEvent.keyDown(document.body, { key: 'u' })
    const first = useStore.getState().route
    fireEvent.keyDown(document.body, { key: 'u' })
    const second = useStore.getState().route
    fireEvent.keyDown(document.body, { key: 'u' })

    expect(new Set([first.params.runId, second.params.runId])).toEqual(new Set(['run_2', 'run_3']))
    expect(useStore.getState().route).toEqual(first)
  })

  it('moves on from a swarm that needs you on u', () => {
    act(() => {
      useStore.setState((s) => ({
        runs: {
          ...s.runs,
          run_integrator: toRecord(run({
            id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator',
            started_at: '2026-08-14T09:00:00Z',
          })),
        },
        missions: { mission_1: mission({ open_questions: 1 }) },
      }))
    })
    render(<AppShell />)

    const seen = []
    for (let i = 0; i < 4; i++) {
      fireEvent.keyDown(document.body, { key: 'u' })
      const { route } = useStore.getState()
      seen.push(route.params.runId ?? route.params.missionId)
    }

    expect(new Set(seen.slice(0, 3))).toEqual(new Set(['run_2', 'run_3', 'mission_1']))
    expect(seen[3]).toBe(seen[0])
  })

  it('narrows Working and Finished to my runs, never Needs you', () => {
    render(<AppShell />)
    fireEvent.click(runList().getByRole('button', { name: 'Mine' }))
    expect(useStore.getState().mineOnly).toBe(true)
    expect(runList().getByText('answer the schema question')).toBeDefined()
  })
})

describe('navigation rows', () => {
  it('marks the open page and lists Members for an admin only', () => {
    useStore.setState((s) => ({ info: s.info && { ...s.info, member: { ...s.info.member, role: 'collaborator' } } }))
    render(<AppShell />)

    fireEvent.click(nav().getByRole('button', { name: 'Environment' }))
    expect(useStore.getState().route.name).toBe('environment')
    expect(nav().getByRole('button', { name: 'Environment' }).getAttribute('aria-current')).toBe('page')
    expect(nav().getByRole('button', { name: 'Board' }).getAttribute('aria-current')).toBeNull()
    expect(nav().queryByRole('button', { name: 'Members' })).toBeNull()

    act(() => useStore.setState((s) => ({ info: s.info && { ...s.info, member: { ...s.info.member, role: 'admin' } } })))
    expect(nav().getByRole('button', { name: 'Members' })).toBeDefined()
  })
})

describe('workspace switcher', () => {
  it('shows the Needs you total closed and each workspace its own count', async () => {
    render(<AppShell />)

    const trigger = nav().getByRole('button', { name: 'Workspace: main-repo, 2 runs need you' })
    fireEvent.keyDown(trigger, { key: 'Enter' })

    expect(await screen.findByRole('menuitemradio', { name: 'main-repo, 1 run needs you' })).toBeDefined()
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'docs-site, 1 run needs you' }))
    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
  })

  it('opens every workspace from All workspaces', async () => {
    render(<AppShell />)
    fireEvent.keyDown(nav().getByRole('button', { name: /^Workspace:/ }), { key: 'Enter' })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'All workspaces' }))
    expect(useStore.getState().route.name).toBe('overview')
  })
})

describe('sidebar visibility', () => {
  it('hides on Mod+B, leaves a terminal its keys, and reopens from the pane header', () => {
    render(<AppShell />)
    const terminal = document.createElement('div')
    terminal.className = 'xterm'
    document.body.append(terminal)
    onTestFinished(() => terminal.remove())

    fireEvent.keyDown(terminal, { key: 'b', ctrlKey: true })
    expect(useStore.getState().sidebarCollapsed).toBe(false)

    fireEvent.keyDown(window, { key: 'b', ctrlKey: true })
    expect(screen.queryByRole('navigation', { name: 'Aether' })).toBeNull()

    fireEvent.click(within(screen.getByRole('main')).getByRole('button', { name: 'Open sidebar' }))
    expect(screen.getByRole('navigation', { name: 'Aether' })).toBeDefined()
  })
})

describe('update notice and footer', () => {
  it('says what is available in one row and opens the updates dialog', async () => {
    useStore.setState({ update: updateStatus() })
    render(<AppShell />)

    expect(nav().getByText(/is available/)).toBeDefined()
    fireEvent.click(nav().getByRole('button', { name: 'Update' }))
    expect(await screen.findByRole('heading', { name: 'Updates' })).toBeDefined()
  })

  it('brings a dismissed update back from the footer menu', async () => {
    const update = updateStatus()
    useStore.setState({ update, dismissedUpdates: { cli: update.cli.latest ?? '', server: '', shell: '' } })
    render(<AppShell />)
    expect(nav().queryByText(/is available/)).toBeNull()

    fireEvent.keyDown(nav().getByRole('button', { name: 'Alice, Connecting' }), { key: 'Enter' })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Update…' }))

    expect(useStore.getState().updatesOpen).toBe(true)
    expect(useStore.getState().dismissedUpdates.cli).toBe('')
  })

  it('opens the shortcuts and switches the theme from the footer menu', async () => {
    render(<AppShell />)
    const footer = nav().getByRole('button', { name: 'Alice, Connecting' })

    fireEvent.keyDown(footer, { key: 'Enter' })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Keyboard shortcuts' }))
    expect(useStore.getState().shortcutsOpen).toBe(true)
  })
})

describe('phone shell', () => {
  it('opens the drawer from the top bar and closes it on navigation, focusing the view', async () => {
    atViewport(390, { pointer: 'coarse' })
    render(<AppShell />)

    expect(screen.queryByRole('navigation', { name: 'Aether' })).toBeNull()
    const banner = within(topBanners()[0]!)
    expect(banner.getByText('Board')).toBeDefined()
    fireEvent.click(banner.getByRole('button', { name: 'Open sidebar, 2 runs need you' }))

    const drawer = within(await screen.findByRole('dialog', { name: 'Aether' }))
    fireEvent.click(drawer.getByRole('button', { name: 'Activity' }))

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Aether' })).toBeNull())
    expect(banner.getByText('Activity')).toBeDefined()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: 'Activity' })))
  })
})

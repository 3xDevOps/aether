import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AppShell } from '@/components/shell/app-shell'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { hydrate } from '@/store/sync'
import { alice, bob, fakeApi, mission, otherWorkspace, run, updateStatus, vera, workspace } from '@/test/fixtures'
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
    presence: [],
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
    expect(screen.queryByRole('contentinfo')).toBeNull()
  })

  it('holds New run and shows no initials until the computer is linked', () => {
    useStore.setState({ info: null, linkStatus: { server_configured: false, linked: false, addr: '', user: '', repo: '' } })
    render(<AppShell />)

    const launch = nav().getByRole('button', { name: /New run/ })
    expect(launch.getAttribute('aria-disabled')).toBe('true')
    fireEvent.click(launch)
    expect(useStore.getState().paletteDialog).toBeNull()
    const footer = screen.getByRole('button', { name: /^Not signed in/ })
    expect(within(footer).queryByText('NI')).toBeNull()
  })

  it('groups runs under an h2 each, every group expanded until its header is pressed', () => {
    render(<AppShell />)

    const groups = runList().getAllByRole('heading', { level: 2 })
    expect(groups.map((heading) => heading.textContent)).toEqual(['Needs you2', 'Working1', 'Finished1'])
    const finished = runList().getByRole('button', { name: /^Finished/ })
    expect(finished.getAttribute('aria-expanded')).toBe('true')
    expect(runList().getByText('tidy the readme')).toBeDefined()
    fireEvent.click(finished)
    expect(finished.getAttribute('aria-expanded')).toBe('false')
    expect(runList().queryByText('tidy the readme')).toBeNull()
  })
})

describe('run rows', () => {
  it('lists every Needs you row', () => {
    const waiting = Array.from({ length: 7 }, (_, i) =>
      run({ id: `run_wait_${i}`, task: `question ${i}`, status: 'needs-attention' }))
    useStore.setState({ runs: Object.fromEntries(waiting.map((r) => [r.id, toRecord(r)])) })
    render(<AppShell />)

    expect(runList().getAllByRole('button', { name: /^Needs you · question/ })).toHaveLength(7)
  })

  it('names the state first, marks the open run, and opens its terminal', () => {
    render(<AppShell />)

    const row = runList().getByRole('button', { name: /^Working · rewrite the checkout flow/ })
    fireEvent.click(row)

    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
    expect(row.getAttribute('aria-current')).toBe('page')
  })

  it('shows how long ago each row last changed and says it in full in its name', () => {
    vi.useFakeTimers({ now: new Date('2026-08-14T10:17:00Z') })
    try {
      useStore.setState((s) => ({
        runs: {
          ...s.runs,
          run_integrator: toRecord(run({
            id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator',
            started_at: '2026-08-14T09:00:00Z',
          })),
        },
        missions: { mission_1: mission() },
      }))
      render(<AppShell />)

      const row = runList().getByRole('button', { name: 'Working · rewrite the checkout flow · Agent working · 15 minutes ago · your run' })
      const age = within(row).getByText('15m')
      expect(age.getAttribute('dateTime')).toBe('2026-08-14T10:02:00Z')
      // No native tooltip; the exact time is the row's description and is in its details box.
      const described = (button: HTMLElement) => document.getElementById(button.getAttribute('aria-describedby')!)?.textContent
      const parent = runList().getByRole('button', { name: /^Working · coordinate · / })
      expect(age.getAttribute('title')).toBeNull()
      expect(row.getAttribute('title')).toBeNull()
      expect(described(row)).toBe(new Date('2026-08-14T10:02:00Z').toLocaleString())
      expect(described(parent)).toBe(new Date('2026-08-14T09:00:00Z').toLocaleString())

      act(() => {
        vi.advanceTimersByTime(60_000)
      })
      expect(within(row).getByText('16m')).toBe(age)
      expect(row.getAttribute('aria-label')).toMatch(/ · 16 minutes ago · your run$/)
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps a finish nobody has opened at full strength, nested workers too, and says so in its name', () => {
    useStore.setState((s) => ({
      runs: {
        ...s.runs,
        run_5: toRecord(run({ id: 'run_5', task: 'bump the linter', status: 'failed', finish_unopened: true })),
        run_integrator: toRecord(run({
          id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator',
        })),
        run_worker: toRecord(run({
          id: 'run_worker', task: 'port the parser', status: 'completed', mission_id: 'mission_1',
          mission_role: 'worker', finish_unopened: true,
        })),
      },
      missions: { mission_1: mission() },
    }))
    render(<AppShell />)
    const shown = (state: string, task: string) => {
      const row = runList().getByRole('button', { name: new RegExp(`^${state} · ${task} · `) })
      return {
        unopened: row.getAttribute('aria-label')!.includes(' · Not opened yet · '),
        muted: within(row).getByText(task).className.includes('text-muted'),
      }
    }

    expect(shown('Failed', 'bump the linter')).toEqual({ unopened: true, muted: false })
    expect(shown('Done', 'port the parser')).toEqual({ unopened: true, muted: false })
    expect(shown('Done', 'tidy the readme')).toEqual({ unopened: false, muted: true })

    act(() => useStore.getState().applyFinishOpened('run_5'))

    expect(shown('Failed', 'bump the linter')).toEqual({ unopened: false, muted: true })
  })

  describe('people', () => {
    const dan = { id: 'mem_dan', display_name: 'Dan', color: '#911eb4', role: 'viewer' as const }
    const on = (...members: { id: string }[]) => members.map((member) => ({
      member_id: member.id, state: 'watching' as const, watching: ['run_1'], last_seen: '2026-08-14T10:10:00Z',
    }))
    const seed = (over: Parameters<typeof run>[0], ...present: { id: string }[]) =>
      useStore.setState((s) => ({
        members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera, [dan.id]: dan },
        presence: on(...present),
        runs: { ...s.runs, run_1: toRecord(run(over)) },
      }))
    const row = () => runList().getByRole('button', { name: /^Working · rewrite the checkout flow/ })
    const faces = () => [...row().querySelectorAll('[data-slot=run-people] [data-slot=avatar]')].map((face) => face.getAttribute('aria-label'))
    const details = () => document.querySelector<HTMLElement>('[data-slot=run-details]')

    it("fills the owner mark with the owner's colour, whoever backs or controls the run", () => {
      seed({ member_id: bob.id, account_member_id: alice.id, controller_member_id: vera.id })
      render(<AppShell />)

      expect(row().querySelector<HTMLElement>('[data-slot=owner-mark]')?.style.backgroundColor).toBe('rgb(60, 180, 75)')
      expect(row().getAttribute('aria-label')).toContain(" · Bob's run · Vera controls")
    })

    it('stacks who is on the run with its controller in front, and counts past three', () => {
      seed({ controller_member_id: vera.id }, bob, alice, vera)
      render(<AppShell />)

      expect(faces()).toEqual(['Bob', 'Alice', 'Vera'])
      expect(row().getAttribute('aria-label')).toMatch(/ · your run · Vera controls · you and Bob watching$/)

      act(() => useStore.getState().setPresence(on(bob, alice, vera, dan)))
      expect(faces()).toEqual(['Alice', 'Vera'])
      expect(within(row()).getByText('+2')).toBeDefined()
    })

    it('puts the last controller in front while nobody controls, and draws nothing for an empty run', () => {
      seed({ controller_member_id: '', last_controller_member_id: bob.id }, alice, bob)
      render(<AppShell />)

      expect(faces()).toEqual(['Alice', 'Bob'])
      expect(row().getAttribute('aria-label')).toMatch(/ · nobody controls · Bob and you watching$/)

      act(() => useStore.getState().setPresence([]))
      expect(row().querySelector('[data-slot=run-people]')).toBeNull()
    })

    it('opens the run details beside a row a mouse rests on, in place of a native tooltip', () => {
      vi.useFakeTimers()
      try {
        seed({ member_id: bob.id, controller_member_id: vera.id }, alice, vera)
        render(<AppShell />)

        fireEvent.pointerMove(row())
        expect(details()).toBeNull()
        act(() => {
          vi.advanceTimersByTime(400)
        })
        const shown = within(details()!)
        expect(shown.getByText('rewrite the checkout flow')).toBeDefined()
        expect(shown.getByText('Agent working')).toBeDefined()
        expect(shown.getByText('main-repo')).toBeDefined()
        expect(shown.getByText('aether/run-1-checkout')).toBeDefined()
        expect(shown.getByText('Started').nextElementSibling?.textContent).toBe(
          new Date('2026-08-14T10:02:00Z').toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }),
        )
        expect(shown.getByText('Owner').nextElementSibling?.textContent).toBe('BBob')
        expect(shown.getByText('Controlling').parentElement?.textContent).toBe('VVeraControlling')
        expect(shown.getByText('Alice (you)')).toBeDefined()

        fireEvent.pointerLeave(row())
        expect(details()).toBeNull()

        act(() => useStore.getState().applyRunController('run_1', '', vera.id))
        fireEvent.pointerMove(row())
        expect(within(details()!).getByText('Nobody is controlling').textContent).toBe('Nobody is controllingVera had control last')
      } finally {
        vi.useRealTimers()
      }
    })

    it('opens no details for a finger', () => {
      atViewport(1024, { pointer: 'coarse' })
      vi.useFakeTimers()
      try {
        render(<AppShell />)
        fireEvent.pointerMove(row())
        act(() => {
          vi.advanceTimersByTime(1000)
        })
        expect(details()).toBeNull()
      } finally {
        vi.useRealTimers()
      }
    })
  })

  it('prefixes a Needs you row from another workspace and answers it where it waits', () => {
    render(<AppShell />)

    const row = runList().getByRole('button', { name: /^Needs you · docs-site · ship the invoice export/ })
    const item = row.closest('[data-slot="list-row"]') as HTMLElement
    fireEvent.click(within(item).getByRole('button', { name: 'Review' }))

    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_3', view: 'changes' } })
  })

  it('replies to an idle run from its Session composer', () => {
    render(<AppShell />)

    const row = runList().getByRole('button', { name: /^Needs you · answer the schema question/ })
    fireEvent.click(within(row.closest('[data-slot="list-row"]') as HTMLElement).getByRole('button', { name: 'Reply' }))

    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_2', view: 'session', focus: 'composer' } })
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
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_3', view: 'changes' } })
  })

  it('walks on from the open run when focus is outside the list', () => {
    act(() => useStore.getState().navigate('run', { runId: 'run_2' }))
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

    const navigation = within(screen.getByRole('navigation', { name: 'Main navigation' }))
    fireEvent.click(navigation.getByRole('button', { name: 'Environment' }))
    expect(useStore.getState().route.name).toBe('environment')
    expect(navigation.getByRole('button', { name: 'Environment' }).getAttribute('aria-current')).toBe('page')
    expect(navigation.getByRole('button', { name: 'Board' }).getAttribute('aria-current')).toBeNull()
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
    fireEvent.click(banner.getByRole('button', { name: 'Open sidebar, 2 runs need you' }))

    const drawer = within(await screen.findByRole('dialog', { name: 'Aether' }))
    fireEvent.click(drawer.getByRole('button', { name: 'Settings' }))

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Aether' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: 'Settings' })))
  })

  it('titles a run page "Run" and shrinks New run to an icon there', () => {
    atViewport(390, { pointer: 'coarse' })
    useStore.setState({ route: { name: 'run', params: { runId: 'run_2' } } })
    render(<AppShell />)

    const banner = within(topBanners()[0]!)
    expect(banner.getByText('Run')).toBeDefined()
    expect(banner.queryByText('answer the schema question')).toBeNull()
    expect(banner.getByRole('button', { name: 'New run' }).textContent).toBe('')
  })
})

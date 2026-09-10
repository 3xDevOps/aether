import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CommandPalette } from '@/components/palette'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { agentInfo, alice, bob, otherWorkspace, run, vera, workspace } from '@/test/fixtures'
import { openSelect, pickOption } from '@/test/select'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const active = run({ id: 'run_1', task: 'rewrite the checkout flow' })

beforeEach(() => {
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    runs: { [active.id]: toRecord(active) },
    acked: {},
    pausedRuns: {},
    paletteOpen: false,
    paletteDialog: null,
    paletteRunID: null,
    route: { name: 'board', params: {} },
    hydrated: true,
    // Null is the legacy remote monitor: the pre-capabilities allowlist
    // (steering, launch, templates), no admin methods, no local verbs.
    capabilities: null,
  })
  vi.clearAllMocks()
  vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice], shared_with: [] })
  vi.mocked(api.agentList).mockResolvedValue([
    agentInfo(),
    agentInfo({ name: 'myagent', source: 'member', install_script: undefined }),
  ])
})

function open() {
  // The launch and inject forms are the shell's, not the palette's; render
  // the host beside it the way AppShell does.
  render(
    <>
      <CommandPalette />
      <PaletteDialogs />
    </>,
  )
  fireEvent.keyDown(window, { key: 'k', metaKey: true })
}

/** The agent field, once its roster has landed: until then it is disabled and
 * still reads its placeholder, so no list can be dropped from it. */
async function agentField(): Promise<HTMLElement> {
  const agent = await screen.findByLabelText('Agent')
  await waitFor(() => expect(agent.textContent).not.toBe('Choose an agent'))
  return agent
}

/** Appends markup the guard has to notice, removed however the test ends. */
function overlay(markup: string): void {
  const host = document.createElement('div')
  host.setAttribute('data-probe', '')
  host.innerHTML = markup
  document.body.append(host)
  onTestFinished(() => host.remove())
}

describe('command palette', () => {
  // Every way into a run lands on the Terminal tab, the terminal takes the
  // focus when it mounts, and xterm swallows Tab. This shortcut is the way
  // out, so a terminal has no claim on it; a modal does.
  it.each([
    ['a terminal', '<div class="xterm"><span></span></div>', true],
    ['a dialog', '<div role="dialog"><button type="button">ok</button></div>', false],
    ['a menu', '<div role="menu"><div role="menuitem">Kill run</div></div>', false],
    // A select list is portalled out of the dialog that hosts it, so there is
    // no dialog above it to stand the chord down. It says it is open, which is
    // what tells it apart from cmdk's own list inside the palette.
    [
      'an open list',
      '<div role="listbox" data-state="open"><div role="option">claude</div></div>',
      false,
    ],
  ])('opens from inside %s: %s', (_, markup, opens) => {
    render(<CommandPalette />)
    overlay(markup)

    fireEvent.keyDown(document.querySelector('[data-probe] *') as HTMLElement, {
      key: 'k',
      ctrlKey: true,
    })

    expect(useStore.getState().paletteOpen).toBe(opens)
  })

  // A control that disables itself mid-flight - the Send button on the form
  // this key would stack over - drops the keyboard on the body without a
  // focusout, and Radix's focus scope watches children rather than attributes,
  // so it does not take it back.
  it.each([
    ['a dialog', '<div role="dialog"><button type="button" disabled>ok</button></div>'],
    ['a menu', '<div role="menu"><div role="menuitem">Kill run</div></div>'],
  ])('stays shut under an open %s when focus has fallen to the body', (_, markup) => {
    render(<CommandPalette />)
    overlay(markup)

    fireEvent.keyDown(document.body, { key: 'k', ctrlKey: true })

    expect(useStore.getState().paletteOpen).toBe(false)
  })

  it('keeps the chord away from the browser whether or not it acts on it', () => {
    render(<CommandPalette />)
    overlay('<div role="dialog"><button type="button">ok</button></div>')

    const standDown = fireEvent.keyDown(document.body, { key: 'k', ctrlKey: true })
    // fireEvent returns false once a listener has called preventDefault.
    expect(standDown).toBe(false)
  })

  // The forms this component hosts are store state, so they are asked of the
  // store: they may be mid-render, or have dropped the keyboard entirely.
  it('stays shut while it is hosting a form of its own', () => {
    useStore.setState({ paletteDialog: 'launch' })
    render(<CommandPalette />)

    fireEvent.keyDown(document.body, { key: 'k', ctrlKey: true })

    expect(useStore.getState().paletteOpen).toBe(false)
  })

  it('leaves a dismissed dialog no claim on the chord', () => {
    render(<CommandPalette />)
    overlay('<div role="dialog" data-state="closed"></div>')

    fireEvent.keyDown(document.body, { key: 'k', ctrlKey: true })

    expect(useStore.getState().paletteOpen).toBe(true)
  })

  it('names the modifier the reader actually has', () => {
    // shortcutLabel prefers userAgentData; jsdom defines neither, so both
    // have to be stubbed or the test stops testing what it claims.
    const platform = (value: string) => {
      Object.defineProperty(navigator, 'platform', { value, configurable: true })
      Object.defineProperty(navigator, 'userAgentData', {
        value: { platform: value },
        configurable: true,
      })
    }
    const original = navigator.platform
    onTestFinished(() => {
      platform(original)
    })

    platform('Linux x86_64')
    const { unmount } = render(<CommandPalette />)
    expect(screen.getByRole('button', { name: 'Commands' }).textContent).toContain('Ctrl+K')
    unmount()

    platform('MacIntel')
    render(<CommandPalette />)
    expect(screen.getByRole('button', { name: 'Commands' }).textContent).toContain('⌘K')
  })

  it('opens on the shortcut and jumps to a run', async () => {
    open()

    const item = await screen.findByText('rewrite the checkout flow')
    fireEvent.click(item)

    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: 'run_1' },
    })
    expect(useStore.getState().paletteOpen).toBe(false)
    // Revealing a run acknowledges it, here as everywhere else.
    expect(useStore.getState().acked[active.id]).toEqual({
      status: active.status,
      at: active.started_at,
    })
  })

  it('switches the active workspace and opens it', async () => {
    open()

    fireEvent.click(await screen.findByText(otherWorkspace.name))

    // Scope and view move together: everything else in the app follows the
    // active id, not the route.
    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
    expect(useStore.getState().route).toEqual({
      name: 'workspace',
      params: { workspaceId: otherWorkspace.id },
    })
  })

  it('steers the run the centre view is showing, on any of its tabs', async () => {
    // The terminal tab is a route of its own; it carries the same runId.
    useStore.setState({
      route: { name: 'terminal', params: { runId: 'run_1' } },
      pausedRuns: { run_1: false },
    })
    open()

    fireEvent.click(await screen.findByText('Pause run'))

    await waitFor(() => expect(api.runPause).toHaveBeenCalledWith('run_1'))
  })

  it('offers neither pause nor resume while the paused state is unknown', async () => {
    // Hydration seeds pausedRuns from the run list's `paused` field, but a
    // legacy gateway sends none: with no entry the client cannot tell which
    // verb the server would accept, so it offers neither.
    useStore.setState({ route: { name: 'terminal', params: { runId: 'run_1' } } })
    open()

    await screen.findByText('Kill run')
    expect(screen.queryByText('Pause run')).toBeNull()
    expect(screen.queryByText('Resume run')).toBeNull()
  })

  it('offers no steering verbs without a run in view', async () => {
    open()
    await screen.findByText('rewrite the checkout flow')
    expect(screen.queryByText('Pause run')).toBeNull()
  })

  it('launches a run into the active workspace', async () => {
    open()

    fireEvent.click(await screen.findByText('Launch a run...'))
    // Where it lands is stated, not asked: there is no workspace picker.
    const target = await screen.findByLabelText('Target workspace')
    expect(target.textContent).toContain(workspace.name)
    expect(target.textContent).toContain(workspace.base_branch)

    // Nothing is launchable until the roster names the harness it will send.
    await agentField()
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.runLaunch).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        harness: 'claude',
      }),
    )
    // A launch drops the user straight into the agent terminal.
    await waitFor(() => expect(useStore.getState().route.name).toBe('terminal'))
  })

  it('offers member-registered agents in the launch harness dropdown', async () => {
    open()

    fireEvent.click(await screen.findByText('Launch a run...'))
    // agent.list is the source of truth for who this server can run, so a
    // member's registered harness must be selectable here, not just the
    // shipped names.
    await openSelect(await agentField())
    await screen.findByRole('option', { name: 'myagent' })
    expect(api.agentList).toHaveBeenCalled()
    // The deployment escape hatch stays reachable alongside the roster.
    expect(screen.getByRole('option', { name: 'custom' })).toBeTruthy()
  })

  it('launches with the selected shared account and its agent roster', async () => {
    vi.mocked(api.accountList).mockResolvedValue({
      accounts: [alice, bob],
      shared_with: [],
    })
    vi.mocked(api.agentList).mockImplementation(async (accountID) =>
      accountID === bob.id
        ? [agentInfo({ name: 'bob-agent', source: 'member', install_script: undefined })]
        : [agentInfo()],
    )
    open()

    fireEvent.click(await screen.findByText('Launch a run...'))
    await pickOption(await screen.findByLabelText('Account'), 'Bob (shared)')
    const agent = await agentField()
    await openSelect(agent)
    await screen.findByRole('option', { name: 'bob-agent' })
    // Radix hides the rest of the document while a list is open.
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(agent.textContent).toBe('bob-agent'))
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.runLaunch).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        harness: 'bob-agent',
        account_member_id: bob.id,
      }),
    )
    expect(api.agentList).toHaveBeenCalledWith(bob.id)
  })

  it('launches a templated run into the active workspace', async () => {
    open()

    fireEvent.click(await screen.findByText('Launch from a template...'))
    // The workspace's templates arrive from template.list.
    const template = await screen.findByLabelText('Template')
    await waitFor(() => expect(template.textContent).toBe('nightly triage'))
    expect(api.templateList).toHaveBeenCalledWith(workspace.id)

    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.templateLaunch).toHaveBeenCalledWith(workspace.id, 'nightly triage'),
    )
    await waitFor(() =>
      expect(useStore.getState().route).toEqual({
        name: 'terminal',
        params: { runId: 'run_tpl' },
      }),
    )
    // Seeded, so the terminal tab it lands on does not call the run deleted.
    expect(useStore.getState().runs.run_tpl).toBeDefined()
  })

  it('offers the admin surfaces when the gateway serves their methods', async () => {
    useStore.setState({
      capabilities: {
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach', 'terminal'],
        local: ['link.status', 'daemon.status', 'pull'],
      },
    })
    open()

    fireEvent.click(await screen.findByText('Members'))

    expect(useStore.getState().route).toEqual({ name: 'members', params: {} })
  })

  it('keeps the roster reachable behind the remote allowlist', async () => {
    // A remote gateway advertises its allowlist; the admin verbs are not on
    // it, but member.list is, and the roster is worth reading, so the one
    // Go-to entry that survives is Members.
    useStore.setState({
      capabilities: {
        gateway: 'remote',
        methods: ['run.list', 'run.get', 'member.list'],
        ws: ['events', 'attach'],
      },
    })
    open()

    await screen.findByText('rewrite the checkout flow')
    expect(screen.getByText('Members')).toBeDefined()
    // The gate lives in the shared list, so a palette that stopped using it
    // would start offering these again.
    expect(screen.queryByText('Approvals')).toBeNull()
    expect(screen.queryByText('Activity')).toBeNull()
    expect(screen.queryByText('Files')).toBeNull()
    expect(screen.queryByText('Manage workspaces')).toBeNull()
    expect(screen.queryByText('Onboarding')).toBeNull()
  })

  it('hides the admin surfaces on a legacy monitor without capabilities', async () => {
    // capabilities stays null (the beforeEach default): the endpoint 404ed,
    // so only the pre-capabilities allowlist may render. member.list is on
    // it; the methods behind the other entries would all answer 403.
    open()

    await screen.findByText('rewrite the checkout flow')
    expect(screen.getByText('Members')).toBeDefined()
    expect(screen.queryByText('Manage workspaces')).toBeNull()
    expect(screen.queryByText('Templates')).toBeNull()
    expect(screen.queryByText('Agents')).toBeNull()
  })

  it('jumps to the approval inbox, the activity feed and the files tree', async () => {
    // The three surfaces that were reachable only from an 11px status-bar
    // button or the sidebar nav. The palette renders the same gated list the
    // nav does, so they arrive together.
    useStore.setState({
      capabilities: {
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach', 'terminal'],
        local: ['link.status', 'daemon.status'],
      },
    })
    open()
    await screen.findByText('rewrite the checkout flow')

    for (const [label, route] of [
      ['Approvals', 'approvals'],
      ['Activity', 'timeline'],
      ['Files', 'files'],
    ]) {
      // Selecting an item closes the palette; reopen it for the next one
      // rather than mounting a second copy of it.
      act(() => useStore.setState({ paletteOpen: true }))
      fireEvent.click(await screen.findByText(label))
      expect(useStore.getState().route).toEqual({ name: route, params: {} })
    }
  })

  it('pulls the focused run branch through the local gateway', async () => {
    useStore.setState({
      runs: { [active.id]: toRecord(run({ last_commit: 'abc1234' })) },
      route: { name: 'terminal', params: { runId: 'run_1' } },
      capabilities: {
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach', 'terminal'],
        local: ['pull'],
      },
    })
    open()

    fireEvent.click(await screen.findByText('Pull branch'))

    await waitFor(() => expect(api.localPull).toHaveBeenCalledWith('run_1'))
  })

  it('offers handoff targets who can own a run, never a viewer', async () => {
    // The run belongs to alice; bob may take it, vera may not, because the
    // server refuses to hand a run to someone who cannot own one.
    useStore.setState({
      members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera },
      route: { name: 'terminal', params: { runId: 'run_1' } },
    })
    open()

    expect(await screen.findByText('Hand off to Bob')).toBeDefined()
    expect(screen.queryByText('Hand off to Vera')).toBeNull()

    fireEvent.click(screen.getByText('Hand off to Bob'))

    await waitFor(() => expect(api.runHandoff).toHaveBeenCalledWith('run_1', bob.id))
  })

  it('offers relaunch only on a terminal run', async () => {
    useStore.setState({
      runs: { [active.id]: toRecord(run({ status: 'failed' })) },
      route: { name: 'terminal', params: { runId: 'run_1' } },
      capabilities: {
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach', 'terminal'],
        local: [],
      },
    })
    open()

    fireEvent.click(await screen.findByText('Relaunch run'))

    await waitFor(() => expect(api.runRelaunch).toHaveBeenCalledWith('run_1'))
  })
})

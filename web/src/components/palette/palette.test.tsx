import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CommandPalette } from '@/components/palette'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, bob, otherWorkspace, run, vera, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// jsdom has neither of the two browser APIs the dialog and cmdk reach for.
Element.prototype.scrollIntoView = vi.fn()
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
)

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

describe('command palette', () => {
  it('opens on the shortcut and jumps to a run', async () => {
    open()

    const item = await screen.findByText('rewrite the checkout flow')
    fireEvent.click(item)

    expect(useStore.getState().route).toEqual({
      name: 'run',
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
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1' } } })
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
    await screen.findByRole('option', { name: 'myagent' })
    expect(api.agentList).toHaveBeenCalled()
    // The deployment escape hatch stays reachable alongside the roster.
    expect(screen.getByRole('option', { name: 'custom' })).toBeTruthy()
  })

  it('launches a templated run into the active workspace', async () => {
    open()

    fireEvent.click(await screen.findByText('Launch from a template...'))
    // The workspace's templates arrive from template.list.
    await screen.findByRole('option', { name: 'nightly triage' })
    expect(api.templateList).toHaveBeenCalledWith(workspace.id)

    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.templateLaunch).toHaveBeenCalledWith(workspace.id, 'nightly triage'),
    )
    await waitFor(() =>
      expect(useStore.getState().route).toEqual({
        name: 'run',
        params: { runId: 'run_tpl' },
      }),
    )
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

  it('pulls the focused run branch through the local gateway', async () => {
    useStore.setState({
      runs: { [active.id]: toRecord(run({ last_commit: 'abc1234' })) },
      route: { name: 'run', params: { runId: 'run_1' } },
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
      route: { name: 'run', params: { runId: 'run_1' } },
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
      route: { name: 'run', params: { runId: 'run_1' } },
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

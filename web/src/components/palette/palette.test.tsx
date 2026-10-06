import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useRef } from 'react'
import { CommandPalette } from '@/components/palette'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { agentInfo, alice, bob, otherWorkspace, run, serverInfo, vera, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const active = run({ id: 'run_1', task: 'rewrite the checkout flow' })

beforeEach(() => {
  useStore.setState({
    identityKey: 'identity-a',
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    runs: { [active.id]: toRecord(active) },
    pausedRuns: {},
    paletteOpen: false,
    paletteDialog: null,
    paletteRunID: null,
    route: { name: 'board', params: {} },
    hydrated: true,
    // Null is the legacy remote monitor: the pre-capabilities allowlist only.
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
  // The launch and inject forms are the shell's; host them as AppShell does.
  render(
    <>
      <CommandPalette />
      <PaletteDialogs />
    </>,
  )
  fireEvent.keyDown(window, { key: 'k', ctrlKey: true })
}

/** Until its roster lands nothing is launchable. */
async function launchReady(): Promise<void> {
  await waitFor(() => expect((screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement).disabled).toBe(false))
}

function overlay(markup: string): void {
  const host = document.createElement('div')
  host.setAttribute('data-probe', '')
  host.innerHTML = markup
  document.body.append(host)
  onTestFinished(() => host.remove())
}

function SearchButton() {
  return <button type="button" onClick={() => useStore.getState().togglePalette(true)}>Search</button>
}

describe('command palette', () => {
  // xterm swallows Tab, so this shortcut is the way out of a terminal; a modal
  // still claims it.
  it.each([
    ['a terminal', '<div class="xterm"><span></span></div>', true],
    ['a dialog', '<div role="dialog"><button type="button">ok</button></div>', false],
    ['a menu', '<div role="menu"><div role="menuitem">Kill run</div></div>', false],
    ['a confirm', '<div role="alertdialog"><button type="button">ok</button></div>', false],
    // A select list is portalled out of its dialog; data-state="open" tells it
    // apart from cmdk's own list.
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

  it.each([
    ['Ctrl+K', { key: 'k', ctrlKey: true }],
    ['Ctrl+Shift+P', { key: 'p', ctrlKey: true, shiftKey: true }],
  ])('opens from a focused terminal descendant before its handler sees %s', async (_, init) => {
    render(<CommandPalette />)
    const host = document.createElement('div')
    host.className = 'xterm'
    const terminalInput = document.createElement('textarea')
    host.append(terminalInput)
    document.body.append(host)

    let descendantHandled = false
    const terminalHandler = (event: KeyboardEvent) => {
      descendantHandled = true
      event.stopPropagation()
    }
    terminalInput.addEventListener('keydown', terminalHandler)
    onTestFinished(() => {
      terminalInput.removeEventListener('keydown', terminalHandler)
      host.remove()
    })

    terminalInput.focus()
    fireEvent.keyDown(terminalInput, init)

    expect(descendantHandled).toBe(false)
    expect(await screen.findByRole('dialog')).toBeTruthy()
  })

  // A control that disables itself mid-flight drops focus on the body without
  // a focusout, and Radix's focus scope does not watch attributes to take it back.
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

  // Hosted forms are read from the store: they may be mid-render or have dropped focus.
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

  it('returns focus to the opener when Escape dismisses the palette', async () => {
    render(
      <>
        <SearchButton />
        <CommandPalette />
      </>,
    )
    const trigger = screen.getByRole('button', { name: 'Search' })
    trigger.focus()
    fireEvent.click(trigger)
    await screen.findByRole('dialog')

    const focused = document.activeElement ?? document.body
    fireEvent.keyDown(focused, { key: 'Escape' })

    await waitFor(() => expect(useStore.getState().paletteOpen).toBe(false))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).toBe(trigger)
  })

  it('restores a terminal invoker on Escape without leaking the shortcut to xterm', async () => {
    render(<CommandPalette />)
    overlay('<div class="xterm"><textarea aria-label="Terminal input"></textarea></div>')
    const terminal = screen.getByRole('textbox', { name: 'Terminal input' })
    terminal.focus()
    fireEvent.keyDown(terminal, { key: 'k', ctrlKey: true })
    const search = await screen.findByRole('combobox')
    expect(document.activeElement).toBe(search)
    fireEvent.keyDown(search, { key: 'Escape' })
    await waitFor(() => expect(document.activeElement).toBe(terminal))
  })

  it('starts with navigation rather than a focused destructive command', async () => {
    useStore.setState({ route: { name: 'terminal', params: { runId: active.id } } })
    open()
    const search = await screen.findByRole('combobox')
    await waitFor(() => expect(screen.getByRole('option', { name: 'Open the board' }).getAttribute('aria-selected')).toBe('true'))
    fireEvent.keyDown(search, { key: 'Enter' })
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
    expect(api.runKill).not.toHaveBeenCalled()
    expect(api.runDelete).not.toHaveBeenCalled()
  })

  it('leaves focus with a navigation destination instead of returning it to the opener', async () => {
    function Destination() {
      const route = useStore((state) => state.route)
      const input = useRef<HTMLInputElement>(null)
      useEffect(() => {
        if (route.name === 'terminal') input.current?.focus()
      }, [route])
      return route.name === 'terminal' ? <input ref={input} aria-label="Destination terminal" /> : null
    }
    render(<><SearchButton /><CommandPalette /><Destination /></>)
    const trigger = screen.getByRole('button', { name: 'Search' })
    trigger.focus()
    fireEvent.click(trigger)
    fireEvent.click(await screen.findByText(active.task))
    const destination = await screen.findByRole('textbox', { name: 'Destination terminal' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).toBe(destination)
  })

  it.each(['Kill run', 'Delete run'])('requires explicit confirmation before %s calls the gateway', async (label) => {
    useStore.setState({ route: { name: 'terminal', params: { runId: active.id } } })
    open()
    fireEvent.click(await screen.findByRole('option', { name: label }))
    const confirmation = await screen.findByRole('alertdialog')
    expect(screen.queryByRole('combobox')).toBeNull()
    expect(useStore.getState().paletteOpen).toBe(false)
    expect(api.runKill).not.toHaveBeenCalled()
    expect(api.runDelete).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(within(confirmation).getByRole('button', { name: 'Cancel' }))
    fireEvent.click(within(confirmation).getByRole('button', { name: label }))
    await waitFor(() => expect(label === 'Kill run' ? api.runKill : api.runDelete).toHaveBeenCalledWith(active.id))
    expect(label === 'Kill run' ? api.runDelete : api.runKill).not.toHaveBeenCalled()
  })

  it.each(['Cancel', 'Escape'])('cancels a destructive command with %s and returns to the terminal', async (dismiss) => {
    useStore.setState({ route: { name: 'terminal', params: { runId: active.id } } })
    render(<CommandPalette />)
    overlay('<div class="xterm"><textarea aria-label="Terminal input"></textarea></div>')
    const terminal = screen.getByRole('textbox', { name: 'Terminal input' })
    terminal.focus()
    fireEvent.keyDown(terminal, { key: 'k', ctrlKey: true })
    fireEvent.click(await screen.findByRole('option', { name: 'Kill run' }))
    const confirmation = await screen.findByRole('alertdialog')
    fireEvent.keyDown(document.body, { key: 'k', ctrlKey: true })
    expect(useStore.getState().paletteOpen).toBe(false)
    const cancel = within(confirmation).getByRole('button', { name: 'Cancel' })
    if (dismiss === 'Cancel') fireEvent.click(cancel)
    else fireEvent.keyDown(cancel, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(terminal))
    expect(api.runKill).not.toHaveBeenCalled()
    expect(api.runDelete).not.toHaveBeenCalled()
  })

  it.each(['Kill run', 'Delete run'])(
    'discards a pending %s handoff when the identity changes',
    async (label) => {
      useStore.setState({ route: { name: 'terminal', params: { runId: active.id } } })
      render(<CommandPalette />)
      overlay('<textarea aria-label="Old terminal"></textarea><input aria-label="New terminal" />')
      const oldTerminal = screen.getByRole('textbox', { name: 'Old terminal' })
      const newTerminal = screen.getByRole('textbox', { name: 'New terminal' })
      oldTerminal.focus()
      fireEvent.keyDown(oldTerminal, { key: 'k', ctrlKey: true })
      const option = await screen.findByRole('option', { name: label })

      // Hold the closing focus scope's deferred handoff, not the command itself.
      vi.useFakeTimers()
      onTestFinished(() => { vi.useRealTimers() })
      fireEvent.click(option)
      expect(screen.queryByRole('alertdialog')).toBeNull()
      act(() => useStore.setState({
        identityKey: 'identity-b',
        runs: { [active.id]: toRecord(run({ id: active.id, task: 'Fresh scope task' })) },
      }))
      newTerminal.focus()
      await act(() => vi.runOnlyPendingTimersAsync())
      vi.useRealTimers()

      expect(screen.queryByRole('alertdialog')).toBeNull()
      expect(document.activeElement).toBe(newTerminal)
      expect(api.runKill).not.toHaveBeenCalled()
      expect(api.runDelete).not.toHaveBeenCalled()

      fireEvent.keyDown(newTerminal, { key: 'k', ctrlKey: true })
      fireEvent.click(await screen.findByRole('option', { name: label }))
      const fresh = await screen.findByRole('alertdialog')
      expect(within(fresh).getByText(/Fresh scope task/)).toBeTruthy()
      fireEvent.click(within(fresh).getByRole('button', { name: label }))
      await waitFor(() => expect(label === 'Kill run' ? api.runKill : api.runDelete).toHaveBeenCalledExactlyOnceWith(active.id))
      expect(label === 'Kill run' ? api.runDelete : api.runKill).not.toHaveBeenCalled()
      await waitFor(() => expect(document.activeElement).toBe(newTerminal))
    },
  )

  it.each([
    ['Kill run', false],
    ['Delete run', false],
    ['Kill run', true],
    ['Delete run', true],
  ] as const)(
    'invalidates a visible %s confirmation on identity change (confirm before render: %s)',
    async (label, confirmBeforeRender) => {
      useStore.setState({ route: { name: 'terminal', params: { runId: active.id } } })
      render(<CommandPalette />)
      overlay('<textarea aria-label="Old terminal"></textarea><input aria-label="New terminal" />')
      const oldTerminal = screen.getByRole('textbox', { name: 'Old terminal' })
      const newTerminal = screen.getByRole('textbox', { name: 'New terminal' })
      oldTerminal.focus()
      fireEvent.keyDown(oldTerminal, { key: 'k', ctrlKey: true })
      fireEvent.click(await screen.findByRole('option', { name: label }))
      const stale = await screen.findByRole('alertdialog')
      const staleAction = within(stale).getByRole('button', { name: label })

      act(() => {
        useStore.setState({
          identityKey: 'identity-b',
          runs: { [active.id]: toRecord(run({ id: active.id, task: 'Fresh scope task' })) },
        })
        // A click can reach the old handler before React commits the new scope.
        if (confirmBeforeRender) fireEvent.click(staleAction)
      })
      newTerminal.focus()
      await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
      // Let the removed dialog finish its deferred focus restoration.
      await act(() => {
        const { promise, resolve } = Promise.withResolvers<void>()
        setTimeout(resolve, 0)
        return promise
      })
      expect(document.activeElement).toBe(newTerminal)
      expect(api.runKill).not.toHaveBeenCalled()
      expect(api.runDelete).not.toHaveBeenCalled()

      fireEvent.keyDown(newTerminal, { key: 'k', ctrlKey: true })
      fireEvent.click(await screen.findByRole('option', { name: label }))
      const fresh = await screen.findByRole('alertdialog')
      expect(within(fresh).getByText(/Fresh scope task/)).toBeTruthy()
      fireEvent.click(within(fresh).getByRole('button', { name: label }))
      await waitFor(() => expect(label === 'Kill run' ? api.runKill : api.runDelete).toHaveBeenCalledExactlyOnceWith(active.id))
      expect(label === 'Kill run' ? api.runDelete : api.runKill).not.toHaveBeenCalled()
      await waitFor(() => expect(document.activeElement).toBe(newTerminal))
    },
  )

  it('restores navigation order after clearing or backspacing a ranked search', async () => {
    open()
    const search = await screen.findByRole('combobox')
    await userEvent.type(search, 'rewrite')
    await waitFor(() => expect(screen.getByRole('option', { name: /rewrite the checkout flow/ }).getAttribute('aria-selected')).toBe('true'))
    await userEvent.clear(search)
    await waitFor(() => expect(screen.getByRole('option', { name: 'Open the board' }).getAttribute('aria-selected')).toBe('true'))
    await userEvent.type(search, 'rewrite')
    await userEvent.keyboard('{Backspace>7/}')
    await waitFor(() => expect(screen.getByRole('option', { name: 'Open the board' }).getAttribute('aria-selected')).toBe('true'))
    fireEvent.keyDown(search, { key: 'Enter' })
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
  })

  it('keeps an updated title searchable and selects the updated run with Enter', async () => {
    open()
    const search = await screen.findByRole('combobox')
    await userEvent.type(search, 'quasar')
    expect(screen.queryAllByRole('option')).toEqual([])
    act(() => useStore.getState().applyRunTitle(active.id, 'quasar migration'))
    await waitFor(() => expect(screen.getByRole('option', { name: /quasar migration/ }).getAttribute('aria-selected')).toBe('true'))
    fireEvent.keyDown(search, { key: 'Enter' })
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: active.id } })
  })

  it('drops archived results and selects the remaining match as live status changes arrive', async () => {
    const second = run({ id: 'run_2', task: 'rewrite the billing flow' })
    useStore.setState({ runs: { [active.id]: toRecord(active), [second.id]: toRecord(second) } })
    open()
    const search = await screen.findByRole('combobox')
    await userEvent.type(search, 'rewrite')
    const first = screen.getByRole('option', { name: /rewrite the checkout flow/ })
    fireEvent.pointerMove(first)
    act(() => {
      useStore.getState().applyRunStatus(active.id, 'merged', undefined, '2026-10-02T10:00:00Z')
      useStore.getState().applyRunArchived(active.id, '2026-10-02T10:01:00Z', null)
    })
    await waitFor(() => expect(screen.queryByRole('option', { name: /rewrite the checkout flow/ })).toBeNull())
    await waitFor(() => expect(screen.getByRole('option', { name: /rewrite the billing flow/ }).getAttribute('aria-selected')).toBe('true'))
    fireEvent.keyDown(search, { key: 'Enter' })
    expect(useStore.getState().route.params.runId).toBe(second.id)
  })

  it('does not execute a disabled match, then follows its live enablement', async () => {
    useStore.setState({
      route: { name: 'terminal', params: { runId: active.id } },
      capabilities: { gateway: 'local', methods: ['*'], ws: [], local: ['pull'] },
    })
    open()
    const search = await screen.findByRole('combobox')
    await userEvent.type(search, 'Pull branch')
    expect(screen.getByRole('option', { name: /Pull branch/ }).getAttribute('aria-disabled')).toBe('true')
    fireEvent.keyDown(search, { key: 'Enter' })
    expect(api.localPull).not.toHaveBeenCalled()
    act(() => useStore.getState().applyLastCommit(active.id, 'abc1234', '2026-10-02T10:00:00Z'))
    await waitFor(() => expect(screen.getByRole('option', { name: 'Pull branch' }).getAttribute('aria-selected')).toBe('true'))
    fireEvent.keyDown(search, { key: 'Enter' })
    await waitFor(() => expect(api.localPull).toHaveBeenCalledWith(active.id))
  })

  it('browses the 50 most urgent runs and searches every run by its label, not its task text', async () => {
    const runs = Array.from({ length: 120 }, (_, index) => run({
      id: `run_${index}`,
      title: `quasar ${String(index).padStart(3, '0')}`,
      task: `${'Preserve the current behavior. '.repeat(40)}nebula`,
    }))
    useStore.setState({ runs: Object.fromEntries(runs.map((value) => [value.id, toRecord(value)])) })
    open()
    const search = await screen.findByRole('combobox')
    expect(screen.getAllByText(/^quasar \d+$/)).toHaveLength(50)
    await userEvent.type(search, 'nebula')
    await waitFor(() => expect(screen.queryAllByText(/^quasar \d+$/)).toHaveLength(0))
    await userEvent.clear(search)
    await userEvent.type(search, 'quasar 119')
    await waitFor(() => expect(screen.getByRole('option', { selected: true }).getAttribute('data-value')).toContain('run_119'))
    fireEvent.keyDown(search, { key: 'Enter' })
    expect(useStore.getState().route.params.runId).toBe('run_119')
    act(() => useStore.setState({ paletteOpen: true }))
    const reopened = await screen.findByRole('combobox')
    await userEvent.type(reopened, 'quasar')
    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(120))
    fireEvent.keyDown(reopened, { key: 'End' })
    const last = screen.getAllByRole('option').at(-1)!
    await waitFor(() => expect(last.getAttribute('aria-selected')).toBe('true'))
    const lastID = last.getAttribute('data-value')!.match(/run_\d+/)![0]
    fireEvent.keyDown(reopened, { key: 'Enter' })
    expect(useStore.getState().route.params.runId).toBe(lastID)
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
  })


  it('switches the active workspace and opens it', async () => {
    open()

    fireEvent.click(await screen.findByText(otherWorkspace.name))

    // The rest of the app follows the active id, not the route.
    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
    expect(useStore.getState().route).toEqual({
      name: 'workspace',
      params: { workspaceId: otherWorkspace.id },
    })
  })

  it('steers the run the centre view is showing, on any of its tabs', async () => {
    useStore.setState({
      route: { name: 'terminal', params: { runId: 'run_1' } },
      pausedRuns: { run_1: false },
    })
    open()

    fireEvent.click(await screen.findByText('Pause run'))

    await waitFor(() => expect(api.runPause).toHaveBeenCalledWith('run_1'))
  })

  it('offers neither pause nor resume while the paused state is unknown', async () => {
    // A legacy gateway sends no `paused` field, so the client cannot tell which
    // verb the server would accept.
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

    fireEvent.click(await screen.findByText('New run…'))
    const target = await screen.findByLabelText('Target workspace')
    expect(target.textContent).toContain(workspace.name)
    expect(target.textContent).toContain(workspace.base_branch)

    await launchReady()
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({
        workspace_id: workspace.id,
        harness: 'claude',
      })),
    )
    await waitFor(() => expect(useStore.getState().route.name).toBe('terminal'))
  })

  it('opens the launch dialog on Swarm from New swarm', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    open()

    fireEvent.click(await screen.findByText('New swarm…'))
    expect(await screen.findByRole('dialog', { name: 'New swarm' })).toBeDefined()
    expect(screen.getByLabelText('Objective')).toBeDefined()
  })

  it('offers New swarm only where the gateway carries mission.create', async () => {
    open()
    await screen.findByText('New run…')
    expect(screen.queryByText('New swarm…')).toBeNull()
  })

  it('hides New swarm from a role that cannot launch', async () => {
    useStore.setState({
      capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
      info: { ...serverInfo, member: vera },
    })
    onTestFinished(() => {
      useStore.setState({ info: null })
    })
    open()
    await screen.findByText('Open the board')
    expect(screen.queryByText('New swarm…')).toBeNull()
  })

  it('offers member-registered agents in the launch harness dropdown', async () => {
    open()

    fireEvent.click(await screen.findByText('New run…'))
    // agent.list is the source of truth, not the shipped names.
    expect(await screen.findByRole('radio', { name: /^myagent/ })).toBeDefined()
    expect(api.agentList).toHaveBeenCalled()
    expect(screen.getByRole('radio', { name: /^custom/ })).toBeDefined()
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

    fireEvent.click(await screen.findByText('New run…'))
    const options = await screen.findByRole('button', { name: /^Options/ })
    if (options.getAttribute('aria-expanded') === 'false') await userEvent.click(options)
    await pickOption(screen.getByLabelText('Account'), 'Bob (shared)')
    await waitFor(() => expect(screen.getByRole('radio', { name: /^bob-agent/ }).getAttribute('aria-checked')).toBe('true'))
    await launchReady()
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
    // The remote allowlist omits the admin verbs but keeps member.list.
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
    expect(screen.queryByText('Approvals')).toBeNull()
    expect(screen.queryByText('Activity')).toBeNull()
    expect(screen.queryByText('Files')).toBeNull()
    expect(screen.queryByText('Manage workspaces')).toBeNull()
    expect(screen.queryByText('Onboarding')).toBeNull()
  })

  it.each(['local', 'remote'] as const)(
    'finds configuration and remote files with config-only capabilities on a %s gateway',
    async (gateway) => {
      useStore.setState({
        capabilities: {
          gateway,
          methods: ['config.roots', 'config.import', 'config.tree', 'config.read'],
          ws: [],
        },
        workspaces: {},
        activeWorkspace: '',
        runs: {},
        onboarded: true,
      })
      open()

      const search = await screen.findByRole('combobox')
      await userEvent.type(search, 'config')
      fireEvent.click(await screen.findByText('Agent config files'))
      expect(useStore.getState().route).toEqual({ name: 'configuration', params: {} })
      expect(useStore.getState().paletteOpen).toBe(false)

      act(() => useStore.setState({ paletteOpen: true }))
      const reopenedSearch = await screen.findByRole('combobox')
      await userEvent.clear(reopenedSearch)
      await userEvent.type(reopenedSearch, 'files')
      fireEvent.click(await screen.findByText('Files'))
      expect(useStore.getState().route).toEqual({ name: 'files', params: {} })
    },
  )

  it.each(['config.roots', 'config.import'])(
    'hides configuration when %s is not advertised',
    async (missing) => {
      useStore.setState({
        capabilities: {
          gateway: 'remote',
          methods: ['config.roots', 'config.import'].filter(
            (method) => method !== missing,
          ),
          ws: [],
        },
      })
      open()
      await screen.findByRole('combobox')
      expect(screen.queryByText('Agent config files')).toBeNull()
    },
  )

  it('hides the admin surfaces on a legacy monitor without capabilities', async () => {
    // capabilities null: only the pre-capabilities allowlist, which has member.list.
    open()

    await screen.findByText('rewrite the checkout flow')
    expect(screen.getByText('Members')).toBeDefined()
    expect(screen.queryByText('Templates')).toBeNull()
    expect(screen.queryByText('Agents')).toBeNull()
  })

  it('jumps to the approval inbox, the activity feed and the files tree', async () => {
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
      // Selecting closes the palette; reopen it rather than mount a second copy.
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
    // The server refuses to hand a run to someone who cannot own one.
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

  it('offers restore on an archived run, reached from its own page', async () => {
    // Run lists exclude an archived final run, so it resolves from the run map.
    useStore.setState({
      runs: {
        [active.id]: toRecord(
          run({
            status: 'merged',
            archived_at: '2026-08-14T10:00:00Z',
            deletes_at: '2026-08-28T10:00:00Z',
          }),
        ),
      },
      route: { name: 'terminal', params: { runId: 'run_1' } },
      capabilities: {
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach', 'terminal'],
        local: [],
      },
    })
    open()

    fireEvent.click(await screen.findByText('Restore run'))

    await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith('run_1', false))
  })

  it('opens the same confirm dialog as the board button, and does not archive before it confirms', async () => {
    const done = run({
      id: 'run_done',
      status: 'merged',
      finished_at: '2026-08-14T10:10:00Z',
    })
    useStore.setState({
      runs: { [active.id]: toRecord(active), [done.id]: toRecord(done) },
      capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    })
    open()

    fireEvent.click(await screen.findByText('Archive closed runs...'))

    expect(api.runArchive).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Archive 1' }))

    await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith(done.id, true))
  })

  it('offers relaunch only on a retained TUI Done run', async () => {
    useStore.setState({
      runs: {
        [active.id]: toRecord(
          run({
            status: 'merged',
            mode: 'tui',
            reason: 'closed; retained container',
          }),
        ),
      },
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
  it('offers release for archived retained history in the active workspace, even when no Done card is visible', async () => {
    const archived = run({
      id: 'retained_archived', status: 'merged', reason: 'closed; retained container',
      archived_at: '2026-08-14T10:00:00Z',
    })
    useStore.setState({
      runs: { [active.id]: toRecord(active), [archived.id]: toRecord(archived) },
      capabilities: { gateway: 'remote', methods: ['run.release'], ws: [] },
    })
    open()
    fireEvent.click(await screen.findByText('Free retained containers...'))
    expect(api.runRelease).not.toHaveBeenCalled()
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Free 1' }))
    await waitFor(() => expect(api.runRelease).toHaveBeenCalledWith(archived.id))
    expect(useStore.getState().runs[archived.id]?.archived_at).toBeDefined()
  })

  it('confirms focused release before calling the gateway', async () => {
    useStore.setState({
      runs: { [active.id]: toRecord(run({ ...active, status: 'merged', reason: 'closed; retained container' })) },
      route: { name: 'terminal', params: { runId: active.id } },
      capabilities: { gateway: 'remote', methods: ['run.release'], ws: [] },
    })
    open()
    fireEvent.click(await screen.findByText('Release resources...'))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(api.runRelease).not.toHaveBeenCalled()
    fireEvent.click(dialog.getByRole('button', { name: 'Release resources' }))
    await waitFor(() => expect(api.runRelease).toHaveBeenCalledWith(active.id))
  })
})

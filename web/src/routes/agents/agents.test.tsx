import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { vi } from 'vitest'
import { splitArgv } from '@/components/agents/add-agent'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import { configPrompt } from '@/lib/config-prompt'
import { lookupRoute } from '@/routes/registry'
import '@/routes/agents'
import { useStore } from '@/store'
import { useLaunchDefaults } from '@/store/launch-defaults'
import { agentInfo } from '@/test/fixtures'
import { pickOption } from '@/test/select'

// vi.mock factories are hoisted above static imports, so the fixture module
// must be loaded inside the factory (same as terminal.test.tsx).
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

function mount() {
  const View = lookupRoute('agents')
  if (!View) throw new Error('agents route not registered')
  return render(<View params={{}} />)
}

async function flush() {
  await act(async () => {})
}

beforeEach(() => {
  useStore.setState(useStore.getInitialState(), true)
  vi.mocked(api.agentList).mockResolvedValue([
    agentInfo({ display_name: 'Claude Code', glyph: 'claude', login_found: true, enhanced: 'adapter' }),
    agentInfo({ name: 'myagent', display_name: 'myagent', glyph: 'custom', source: 'member', installed: false, install_script: undefined, enhanced: 'none' }),
  ])
  useStore.setState({
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
  })
})

describe('splitArgv', () => {
  it('splits on spaces and drops empty words', () => {
    expect(splitArgv('mycli -p {task}')).toEqual(['mycli', '-p', '{task}'])
    expect(splitArgv('  mycli  {task} ')).toEqual(['mycli', '{task}'])
  })
})

describe('agents page', () => {
  it('shows list-shaped placeholders while agent.list is pending', async () => {
    vi.mocked(api.agentList).mockReturnValue(new Promise<never>(() => {}))
    mount()

    expect((await screen.findByRole('status', { name: 'Loading agents' })).querySelectorAll('li')).toHaveLength(3)
  })

  it('reads agent.list once, under the identity hydration sets', async () => {
    vi.mocked(api.agentList).mockClear()
    useStore.setState({ hydrated: false })
    mount()
    await flush()
    expect(api.agentList).not.toHaveBeenCalled()
    act(() => useStore.setState({ identityKey: 'alice' }))
    act(() => useStore.setState({ hydrated: true }))
    await flush()
    expect(api.agentList).toHaveBeenCalledTimes(1)
    expect(useStore.getState().agentList?.identity).toBe('alice')
  })

  it('lists installed agents first and folds the rest under More agents', async () => {
    mount()
    await flush()

    const rows = within(screen.getByRole('list', { name: 'Agents' })).getAllByRole('listitem')
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain('Claude Code')
    expect(rows[0].textContent).toContain('Installed · Login found')
    expect(rows[0].textContent).toContain('Standard · Enhanced')
    expect(within(rows[0]).getByRole('button', { name: 'Run Claude Code' })).toBeDefined()
    expect(within(rows[0]).getByRole('combobox', { name: 'Default mode for Claude Code' })).toBeDefined()
    expect(screen.queryByRole('list', { name: 'More agents' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'More agents (1)' }))
    const more = within(screen.getByRole('list', { name: 'More agents' })).getAllByRole('listitem')
    expect(more[0].textContent).toContain('Not installed')
    expect(within(more[0]).getByLabelText('Supports Standard only')).toBeDefined()
    expect(within(more[0]).getByRole('button', { name: 'Set up myagent' })).toBeDefined()
    expect(within(more[0]).queryByRole('combobox')).toBeNull()
  })

  it('asks an installed agent without a login to log in', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ display_name: 'Claude Code', login_found: false })])
    mount()
    await flush()

    expect(screen.getByRole('button', { name: 'Log in Claude Code' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'More agents (0)' })).toBeNull()
  })

  it('explains Standard and Enhanced in a dialog', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: "What's the difference?" }))
    const dialog = within(screen.getByRole('dialog', { name: 'Standard or Enhanced' }))
    expect(dialog.getByText('Works with every agent.')).toBeDefined()
    expect(dialog.queryByRole('radio')).toBeNull()
  })

  it('keeps a default mode per agent without making it the most recent launch', async () => {
    useStore.setState({ launchDefaults: { claude: { mode: 'tui', at: 42 } } })
    mount()
    await flush()

    await pickOption(screen.getByRole('combobox', { name: 'Default mode for Claude Code' }), 'Enhanced')
    expect(useStore.getState().launchDefaults.claude).toEqual({ mode: 'acp', at: 42 })
  })

  it('opens the launch dialog on the agent Run names', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: 'Run Claude Code' }))
    expect(useStore.getState().paletteDialog).toBe('launch')
    expect(useStore.getState().launchDefaults.claude?.mode).toBe('tui')
  })

  it('runs an agent in the mode saved on the server, not this browser\'s', async () => {
    useLaunchDefaults.setState({ result: null, error: null })
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 2, defaults: { agents: { claude: { mode: 'acp' } } } })
    useStore.setState({ launchDefaults: { claude: { mode: 'tui', at: 42 } } })
    mount()
    await flush()
    expect(screen.getByRole('combobox', { name: 'Default mode for Claude Code' }).textContent).toContain('Enhanced')

    fireEvent.click(screen.getByRole('button', { name: 'Run Claude Code' }))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({ defaults: { agent: 'claude', agents: { claude: { mode: 'acp' } } }, revision: 2 }))
  })

  it('opens the setup with the comparison, and comes back from it', async () => {
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Set up again' }))
    expect(screen.getByRole('radiogroup', { name: 'How sessions show Claude Code' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'All agents' }))
    expect(screen.getByRole('list', { name: 'Agents' })).toBeDefined()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Run Claude Code' }))
  })

  it('removes an agent after confirming, and reloads the list', async () => {
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Remove…' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove Claude Code?' })
    expect(dialog.textContent).toContain('Deletes ~/.local/bin/claude')
    expect(dialog.textContent).toContain("New sessions can't start it; running sessions keep going.")
    expect(dialog.textContent).toContain('Its login, settings and package files stay. Set up installs it again.')

    vi.mocked(api.agentList).mockClear()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }))
    await flush()
    expect(api.agentRemove).toHaveBeenCalledWith('claude')
    expect(api.agentList).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect((document.activeElement as HTMLElement | null)?.dataset.agentAction).toBe('claude')
  })

  it('keeps the dialog open with the refusal when the server refuses a removal', async () => {
    vi.mocked(api.agentRemove).mockRejectedValueOnce(new Error('agent "claude" is not installed in your environment: ~/.local/bin/claude does not exist'))
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Remove…' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Remove' }))
    await flush()
    expect(within(screen.getByRole('alertdialog')).getByRole('alert').textContent).toContain('is not installed in your environment')
  })

  it('offers removing a member agent', async () => {
    mount()
    await flush()
    fireEvent.click(screen.getByRole('button', { name: 'More agents (1)' }))
    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for myagent' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Remove…' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove myagent?' })
    expect(dialog.textContent).toContain('Deletes your myagent definition.')
    expect(dialog.textContent).toContain('Add agent… registers it again.')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }))
    await flush()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add agent…' }))
  })

  it('hides Remove without agent.remove', async () => {
    useStore.setState({ capabilities: { gateway: 'server', methods: ['agent.list', 'agent.install', 'run.launch'], ws: [] } })
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    expect(await screen.findByRole('menuitem', { name: 'Set up again' })).toBeDefined()
    expect(screen.queryByRole('menuitem', { name: 'Remove…' })).toBeNull()
  })

  it('adds a custom agent from the header', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: 'Add agent…' }))
    expect(screen.getByRole('form', { name: 'Add agent' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('list', { name: 'Agents' })).toBeDefined()
  })

  it('keeps git identity, GitHub and agent config files collapsed', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: /^Git identity/ }))
    expect(screen.queryByRole('form', { name: 'Git identity' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Set it in Profile' })).toBeDefined()
    expect(screen.getByRole('button', { name: /^GitHub/ })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /^Agent config files/ }))
    expect(screen.getByRole('region', { name: 'Agent config files' })).toBeDefined()
  })

  it.each(['config.roots', 'config.import'])('offers no agent config files when %s is unavailable', async (missing) => {
    useStore.setState({
      capabilities: {
        gateway: 'remote',
        methods: ['agent.list', 'config.roots', 'config.import'].filter((method) => method !== missing),
        ws: [],
      },
    })
    mount()
    await flush()

    expect(screen.queryByRole('button', { name: /^Agent config files/ })).toBeNull()
  })
})

describe('agent config files', () => {
  const writeText = vi.fn<(text: string) => Promise<void>>()

  beforeEach(() => {
    writeText.mockReset().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
  })

  afterEach(() => {
    Reflect.deleteProperty(navigator, 'clipboard')
    vi.restoreAllMocks()
  })

  async function open() {
    mount()
    await flush()
    fireEvent.click(screen.getByRole('button', { name: /^Agent config files/ }))
    return within(screen.getByRole('region', { name: 'Agent config files' }))
  }

  it('shows the prompt and copies exactly it', async () => {
    const section = await open()
    expect(section.getByLabelText('Prompt').textContent).toBe(configPrompt)
    await act(async () => fireEvent.click(section.getByRole('button', { name: 'Copy prompt' })))
    expect(writeText.mock.calls).toEqual([[configPrompt]])
  })

  it('selects the prompt and says so when the clipboard refuses', async () => {
    writeText.mockRejectedValue(new DOMException('Clipboard denied', 'NotAllowedError'))
    const info = vi.spyOn(toast, 'info')
    const section = await open()
    await act(async () => fireEvent.click(section.getByRole('button', { name: 'Copy prompt' })))
    expect(info).toHaveBeenCalledWith('Selected. Press Ctrl+C to copy.')
    expect(window.getSelection()?.toString()).toBe(configPrompt)
  })

  it('opens remote files', async () => {
    const section = await open()
    fireEvent.click(section.getByRole('button', { name: 'Open remote files' }))
    expect(useStore.getState().route).toEqual({ name: 'files', params: { home: 'open' } })
  })
})

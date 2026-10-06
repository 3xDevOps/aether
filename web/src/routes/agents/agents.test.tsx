import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { vi } from 'vitest'
import { splitArgv } from '@/components/agents/add-agent'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import { lookupRoute } from '@/routes/registry'
import '@/routes/agents'
import { ConfigurationRoute } from '@/routes/configuration'
import { useStore } from '@/store'
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
  })
})

describe('splitArgv', () => {
  it('splits on spaces and drops empty words', () => {
    expect(splitArgv('mycli -p {task}')).toEqual(['mycli', '-p', '{task}'])
    expect(splitArgv('  mycli  {task} ')).toEqual(['mycli', '{task}'])
  })
})

describe('agents page', () => {
  it('lists each agent with its install and login state, its modes and one action', async () => {
    mount()
    await flush()

    const rows = within(screen.getByRole('list', { name: 'Agents' })).getAllByRole('listitem')
    expect(rows[0].textContent).toContain('Claude Code')
    expect(rows[0].textContent).toContain('Installed · Login found')
    expect(rows[0].textContent).toContain('Standard · Enhanced')
    expect(within(rows[0]).getByRole('button', { name: 'Run Claude Code' })).toBeDefined()
    expect(rows[1].textContent).toContain('Not installed')
    expect(within(rows[1]).getByLabelText('Supports Standard only')).toBeDefined()
    expect(within(rows[1]).getByRole('button', { name: 'Set up myagent' })).toBeDefined()
    expect(within(rows[1]).queryByRole('button', { name: 'Run myagent' })).toBeNull()
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

  it('opens the setup with the comparison, and comes back from it', async () => {
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Set up again' }))
    expect(screen.getByRole('radiogroup', { name: 'How runs show Claude Code' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'All agents' }))
    expect(screen.getByRole('list', { name: 'Agents' })).toBeDefined()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Run Claude Code' }))
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

    expect(screen.queryByRole('form', { name: 'Git identity' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /^Git identity/ }))
    expect(screen.getByRole('form', { name: 'Git identity' })).toBeDefined()
    expect(screen.getByRole('button', { name: /^GitHub/ })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /^Agent config files/ }))
    expect(screen.getByRole('region', { name: 'Bring your configuration' })).toBeDefined()
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

  it('still opens the configuration route without a workspace', async () => {
    useStore.setState({
      capabilities: { gateway: 'remote', methods: ['agent.list', 'config.roots', 'config.import', 'config.tree'], ws: [] },
      onboarded: true,
      workspaces: {},
      activeWorkspace: '',
    })
    render(<ConfigurationRoute params={{}} client={api} />)
    await flush()
    expect((screen.getByRole('button', { name: 'Choose directory' }) as HTMLButtonElement).disabled).toBe(false)
  })
})

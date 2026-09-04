import { act, fireEvent, render, screen } from '@testing-library/react'
import { vi } from 'vitest'
import { api } from '@/lib/api'
import { lookupRoute } from '@/routes/registry'
import '@/routes/agents'
import { AgentWizard, splitArgv } from '@/routes/agents/wizard'
import { useStore } from '@/store'
import {
  initialEnvTerminal,
  registerEnvTerminalSocket,
} from '@/store/env-terminal'
import { StubSocket } from '@/test/stub-socket'
// vi.mock factories are hoisted above static imports, so the fixture module
// must be loaded inside the factory (same as terminal.test.tsx).
vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})


function mount() {
  const View = lookupRoute('agents')
  if (!View) throw new Error('agents route not registered')
  return render(<View params={{}} />)
}

async function flush() {
  await act(async () => {})
}

class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
  useStore.setState({
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})


describe('splitArgv', () => {
  it('splits on spaces and drops empty words', () => {
    expect(splitArgv('mycli -p {task}')).toEqual(['mycli', '-p', '{task}'])
    expect(splitArgv('  mycli  {task} ')).toEqual(['mycli', '{task}'])
  })
})

describe('agents view', () => {
  it('lists agents with shipped and member badges', async () => {
    const view = mount()
    await flush()

    expect(screen.getByText('claude')).toBeDefined()
    expect(screen.getByText('shipped')).toBeDefined()
    expect(screen.getByText('myagent')).toBeDefined()
    expect(screen.getByText('member')).toBeDefined()
    view.unmount()
  })

  it('shows the add button with regular gateway capabilities', async () => {
    const view = mount()
    await flush()

    expect(screen.getByText('Add agent')).toBeDefined()
    view.unmount()
  })

  it('prefills argv templates and renders the live terminal dock', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })

    expect(screen.getByDisplayValue('mycli {task}')).toBeDefined()
    expect(screen.getByDisplayValue('mycli -p {task}')).toBeDefined()

    fireEvent.click(screen.getByText('Continue'))
    await flush()

    expect(screen.getByRole('region', { name: 'Terminal dock' })).toBeDefined()
    expect(screen.getByText('install mycli into ~/.local/bin')).toBeDefined()
    view.unmount()
  })

  it('sends the wizard install line once across instructions remounts', async () => {
    useStore.getState().resetEnvTerminal()
    useStore.setState({
      envTerminal: {
        ...initialEnvTerminal,
        tabs: ['main', 't2'],
        activeTab: 't2',
      },
    })
    const connection = {
      send: vi.fn(),
      resize: vi.fn(),
      reopen: vi.fn(),
      close: vi.fn(),
    }
    registerEnvTerminalSocket('main', connection)

    const props = {
      agents: [{ name: 'claude', source: 'shipped' as const, install_script: 'install claude' }],
      harness: 'claude',
      onRegistered: vi.fn(),
      onCancel: vi.fn(),
      client: api,
    }
    const first = render(<AgentWizard {...props} />)
    await flush()
    first.unmount()

    const second = render(<AgentWizard {...props} />)
    await flush()

    expect(useStore.getState().envTerminal.activeTab).toBe('main')
    expect(connection.send).toHaveBeenCalledWith('install claude\n')
    expect(connection.send).toHaveBeenCalledTimes(1)
    second.unmount()
    useStore.getState().resetEnvTerminal()
  })

  it('keeps static terminal instructions on an older gateway', async () => {
    useStore.setState({
      capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach'] },
    })
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })
    fireEvent.click(screen.getByText('Continue'))
    await flush()

    expect(screen.getByText(/Open your environment terminal/)).toBeDefined()
    expect(screen.getByText('aether terminal')).toBeDefined()
    view.unmount()
  })

  it('shows the shipped harness install script', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'claude' },
    })
    fireEvent.click(screen.getByText('Continue'))
    await flush()

    expect(screen.getByText('curl -fsSL https://claude.ai/install.sh | bash')).toBeDefined()
    view.unmount()
  })

  it('registers a custom agent after the member finishes setup', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })
    fireEvent.click(screen.getByText('Continue'))
    await flush()

    fireEvent.click(screen.getByText("I've installed and logged in"))
    await flush()

    expect(vi.mocked(api.agentRegister)).toHaveBeenCalledWith({
      name: 'mycli',
      executable: 'mycli',
      tui_args: ['mycli', '{task}'],
      headless_args: ['mycli', '-p', '{task}'],
    })
    expect(screen.getByText('Agent registered')).toBeDefined()
    view.unmount()
  })

  it('omits argv inputs for a shipped name', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'claude' },
    })

    expect(screen.queryByText('TUI command')).toBeNull()
    view.unmount()
  })
})

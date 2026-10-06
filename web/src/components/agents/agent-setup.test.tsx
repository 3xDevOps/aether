import { fireEvent, render, screen, within } from '@testing-library/react'
import { AgentSetup } from '@/components/agents/agent-setup'
import { ModeComparison } from '@/components/agents/mode-comparison'
import type { Api } from '@/lib/api'
import type { AgentInfo, GatewayCapabilities } from '@/lib/types'
import { useStore } from '@/store'
import { agentInfo, fakeApi } from '@/test/fixtures'

const caps: GatewayCapabilities = { gateway: 'local', methods: ['*'], ws: ['events', 'attach'] }

const claude = agentInfo({ display_name: 'Claude Code', installed: false, enhanced: 'adapter', default_mode: 'tui' })
const codex = agentInfo({
  name: 'codex',
  display_name: 'Codex',
  installed: true,
  enhanced: 'adapter',
  enhanced_installed: true,
  default_mode: 'acp',
  install_script: 'npm install -g --prefix "$HOME/.local" @openai/codex',
})
const custom = agentInfo({ name: 'mycli', display_name: 'mycli', source: 'member', glyph: 'custom', enhanced: 'none', install_script: undefined })

beforeEach(() => {
  useStore.setState(useStore.getInitialState(), true)
  useStore.setState({ capabilities: caps })
})

function setUp(agent: AgentInfo, client: Api = fakeApi()) {
  const onDone = vi.fn()
  render(<AgentSetup agent={agent} client={client} onDone={onDone} />)
  return { client, onDone }
}

describe('the Standard and Enhanced comparison', () => {
  it('draws both modes as one radio group with the copy and the per-agent lines', () => {
    render(<ModeComparison agent={claude} value="tui" onChange={vi.fn()} />)

    const group = screen.getByRole('radiogroup', { name: 'How runs show Claude Code' })
    const standard = within(group).getByRole('radio', { name: 'Standard' })
    const enhanced = within(group).getByRole('radio', { name: 'Enhanced' })
    expect(standard.getAttribute('aria-checked')).toBe('true')
    expect(standard.getAttribute('aria-describedby')).toBeTruthy()
    expect(group.textContent).toContain("Your agent's own terminal, exactly as on your machine.")
    expect(group.textContent).toContain('No structured view; the agent acts without asking, and anything it asks is answered in the terminal.')
    expect(group.textContent).toContain('Runs through an adapter, not the agent\'s own screen; some agent-specific commands and screens are missing; starts a few seconds slower.')
    expect(enhanced.getAttribute('aria-disabled')).toBeNull()

    const lines = screen.getByText('Support').closest('dl')!
    expect(lines.textContent).toContain('Claude Code: supported through an adapter published by Anthropic, Zed and JetBrains')
    expect(lines.textContent).toContain('Installed with the agent in the next step')
    expect(lines.textContent).toContain('Chosen when the run starts')
    expect(lines.textContent).toContain('If the adapter fails to start, the run tells you why and offers the terminal')
    expect(lines.textContent).toContain('Uses your Claude login through the Claude Agent SDK; the run shows which account pays')
  })

  it('shows the same moment in both mocks, with the permission only in Enhanced, hidden from assistive tech', () => {
    const { container } = render(<ModeComparison agent={claude} value="tui" onChange={vi.fn()} />)
    const [terminal, session] = container.querySelectorAll('[aria-hidden="true"].min-h-40')
    expect(terminal.textContent).toContain('Fix the flaky login test')
    expect(terminal.textContent).toContain('Run go test ./auth/...')
    expect(terminal.textContent).not.toContain('Approve')
    expect(session.textContent).toContain('Fix the flaky login test')
    expect(session.textContent).toContain('Permission')
    expect(session.textContent).toContain('go test ./auth/...')
    expect(session.textContent).toContain('Approve')
  })

  it('says when a running agent can switch, and leaves billing to Claude', () => {
    render(<ModeComparison agent={{ ...codex, switchable: true }} value="acp" onChange={vi.fn()} />)
    const lines = screen.getByText('Support').closest('dl')!
    expect(lines.textContent).toContain('Switch a running agent from its header')
    expect(lines.textContent).toContain('If the adapter fails to start')
    expect(lines.textContent).toContain('Already installed')
    expect(lines.textContent).not.toContain('Billing')
  })

  it('says Enhanced, not an adapter, can fail to start for a native agent', () => {
    render(<ModeComparison agent={{ ...codex, name: 'opencode', display_name: 'OpenCode', enhanced: 'native' }} value="acp" onChange={vi.fn()} />)
    const lines = screen.getByText('Support').closest('dl')!
    expect(lines.textContent).toContain("OpenCode: supported by the agent's own CLI")
    expect(lines.textContent).toContain('If Enhanced fails to start, the run tells you why and offers the terminal')
    expect(lines.textContent).not.toContain('adapter')
  })

  it('disables Enhanced with the reason for an agent that has none', () => {
    render(<ModeComparison agent={custom} value="tui" onChange={vi.fn()} />)
    const enhanced = screen.getByRole('radio', { name: 'Enhanced' })
    expect((enhanced as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('mycli: not available. Add an enhanced command to the agent to use it.')).toBeDefined()
    expect(screen.queryByText('Support')).toBeNull()
  })
})

describe('agent setup', () => {
  it('starts on the agent default mode', () => {
    setUp(codex)
    expect(screen.getByRole('radio', { name: 'Enhanced' }).getAttribute('aria-checked')).toBe('true')
  })

  it('starts on Enhanced for an agent that prefers it before its adapter is installed', () => {
    setUp({ ...codex, installed: false, enhanced_installed: false, default_mode: 'tui', enhanced_default: true })
    expect(screen.getByRole('radio', { name: 'Enhanced' }).getAttribute('aria-checked')).toBe('true')
  })

  it('starts on the mode the member chose over the one the agent prefers', () => {
    useStore.getState().setLaunchDefault('codex', 'tui')
    setUp({ ...codex, enhanced_default: true })
    expect(screen.getByRole('radio', { name: 'Standard' }).getAttribute('aria-checked')).toBe('true')
  })

  it('installs the adapter with the agent when Enhanced is chosen, then checks agent.list', async () => {
    const client = fakeApi({
      agentInstall: vi.fn(async () => ({ log_tail: 'added 1 package\n', installed: true, enhanced_installed: true })),
      agentList: vi.fn(async () => [{ ...claude, installed: true, enhanced_installed: true, login_found: true }]),
    })
    const { onDone } = setUp(claude, client)

    fireEvent.click(screen.getByRole('radio', { name: 'Enhanced' }))
    fireEvent.click(screen.getByRole('button', { name: 'Install Claude Code' }))

    const status = within(await screen.findByRole('list', { name: 'Claude Code status' }))
    expect(client.agentInstall).toHaveBeenCalledWith('claude', true)
    expect(status.getByText('Installed')).toBeDefined()
    expect(status.getByText('Enhanced installed')).toBeDefined()
    expect(status.getByText('Login found')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Install output' }))
    expect(screen.getByText('added 1 package')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(onDone).toHaveBeenCalledTimes(1)
    expect(useStore.getState().launchDefaults.claude.mode).toBe('acp')
  })

  it('installs the agent alone for Standard', async () => {
    const { client } = setUp(claude)
    fireEvent.click(screen.getByRole('button', { name: 'Install Claude Code' }))
    await screen.findByRole('list', { name: 'Claude Code status' })
    expect(client.agentInstall).toHaveBeenCalledWith('claude', false)
  })

  it('shows a failed install with its error and output, and checks nothing', async () => {
    const client = fakeApi({
      agentInstall: vi.fn(async () => ({
        log_tail: 'sh: curl: not found\n',
        installed: false,
        enhanced_installed: false,
        error: 'the install command exited 127',
      })),
    })
    setUp(claude, client)
    fireEvent.click(screen.getByRole('button', { name: 'Install Claude Code' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Install failed')
    expect(alert.textContent).toContain('the install command exited 127')
    expect(screen.getByText('sh: curl: not found')).toBeDefined()
    expect(client.agentList).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Install Claude Code' })).toBeDefined()
  })

  it('says No login found and keeps the check open, never claiming a sign-in', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [{ ...claude, installed: true, login_found: false }]),
    })
    setUp(claude, client)
    fireEvent.click(screen.getByRole('button', { name: 'Install Claude Code' }))

    expect(await screen.findByText('No login found')).toBeDefined()
    expect(screen.queryByText(/signed in/i)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Done' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Check again' })).toBeDefined()
  })

  it('names the login command for the agent once it is installed', () => {
    setUp({ ...claude, installed: true })
    expect(screen.getByText(/Start Claude Code, then type \/login/)).toBeDefined()
    expect(screen.getByText(/aether terminal/).textContent).toBe('aether terminal\nclaude')
  })

  it('types the install command on a gateway without agent.install', () => {
    useStore.setState({ capabilities: { gateway: 'local', methods: ['agent.list', 'agent.register'], ws: ['events', 'attach'] } })
    setUp(claude)
    expect(screen.queryByRole('button', { name: 'Install Claude Code' })).toBeNull()
    expect(screen.getByText(/aether terminal/).textContent).toContain('curl -fsSL https://claude.ai/install.sh | bash')
  })
})

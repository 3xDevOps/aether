import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import type { Api } from '@/lib/api'
import { configPrompt } from '@/lib/config-prompt'
import type { GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { AgentStep } from '@/routes/onboarding/agent-step'
import { FirstRunStep } from '@/routes/onboarding/first-run-step'
import { useStore } from '@/store'
import { capability } from '@/store/hooks'
import {
  agentInfo,
  alice,
  fakeApi,
  serverInfo,
  workspace,
} from '@/test/fixtures'

const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: ['link.status', 'link.repo', 'env.agents'],
}

function seed(caps: GatewayCapabilities = localCaps) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    identityKey: `${window.location.origin}\u0000${serverInfo.tailnet_hostname ?? ''}\u0000${alice.id}`,
    capabilities: caps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'onboarding', params: {} },
    onboardingStep: 'Connect',
    onboardingFurthest: 'Connect',
    onboardingWorkspace: '',
    onboardingRepo: null,
    onboardingFirstRun: { harness: '', task: '' },
  })
}

function renderStep(client: Api, caps: GatewayCapabilities = localCaps) {
  seed(caps)
  const onNext = vi.fn()
  function Host() {
    const [setup, onSetup] = useState('')
    return (
      <AgentStep
        client={client}
        caps={capability(caps)}
        setup={setup}
        onSetup={onSetup}
        onNext={onNext}
        back={setup ? <button type="button" onClick={() => onSetup('')}>Back</button> : null}
      />
    )
  }
  const view = render(<Host />)
  expandConfiguration()
  return { onNext, view }
}

/** The prompt sits behind the step's Agent config files disclosure. */
function expandConfiguration() {
  const trigger = screen.queryByRole('button', { name: /^Agent config files/ })
  if (trigger && trigger.getAttribute('aria-expanded') !== 'true') fireEvent.click(trigger)
}

beforeEach(() => {
  vi.restoreAllMocks()
  useStore.setState(useStore.getInitialState(), true)
  seed()
})

describe('agents step', () => {
  it('keeps setup and agent config files independent of local gateway capabilities', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ display_name: 'Claude Code', installed: false })]),
    })
    renderStep(client, {
      gateway: 'remote',
      methods: ['*'],
      ws: ['events', 'attach'],
      local: ['link.status'],
    })

    expect(await screen.findByText('Claude Code')).toBeDefined()
    expect(screen.getByLabelText('Prompt').textContent).toBe(configPrompt)
    expect(screen.getByRole('button', { name: 'Copy prompt' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Set up Claude Code' }))
    expect(await screen.findByRole('button', { name: 'Install Claude Code' })).toBeDefined()
  })

  it('leaves agent config files optional when skipping agent setup', async () => {
    const client = fakeApi()
    const { onNext } = renderStep(client)
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalledTimes(1)
  })
})

describe('the agent the step set up', () => {
  it('opens the setup for one agent and comes back to the list when it is done', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ display_name: 'Claude Code', installed: false, enhanced: 'adapter' })]),
    })
    renderStep(client)
    fireEvent.click(await screen.findByRole('button', { name: 'Set up Claude Code' }))
    expect(await screen.findByRole('radiogroup', { name: 'How runs show Claude Code' })).toBeDefined()

    vi.mocked(client.agentList).mockResolvedValue([agentInfo({ display_name: 'Claude Code', login_found: true, enhanced: 'adapter' })])
    fireEvent.click(screen.getByRole('button', { name: 'Install Claude Code' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Done' }))

    expect(await screen.findByText('Installed · Login found')).toBeDefined()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Run Claude Code' }))
    expect(useStore.getState().launchDefaults.claude?.mode).toBe('tui')
  })

  it('remembers Run as the agent the first run starts on and moves on', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ display_name: 'Claude Code', login_found: true })]),
    })
    const { onNext } = renderStep(client)
    fireEvent.click(await screen.findByRole('button', { name: 'Run Claude Code' }))
    expect(onNext).toHaveBeenCalledTimes(1)
    expect(useStore.getState().launchDefaults.claude).toBeDefined()
  })

  it('starts the first run on the default the server reports when nothing was chosen', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ name: 'codex', display_name: 'Codex', login_found: true, enhanced: 'adapter', enhanced_installed: true, default_mode: 'acp' })]),
    })
    renderStep(client)
    fireEvent.click(await screen.findByRole('button', { name: 'Run Codex' }))
    expect(useStore.getState().launchDefaults.codex?.mode).toBe('acp')
  })

  it('adds a custom agent and opens its setup', async () => {
    const client = fakeApi()
    renderStep(client)
    fireEvent.click(await screen.findByRole('button', { name: 'Add agent…' }))
    vi.mocked(client.agentList).mockResolvedValue([
      agentInfo({ name: 'mycli', display_name: 'mycli', source: 'member', glyph: 'custom', installed: false, install_script: undefined, enhanced: 'native' }),
    ])
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'mycli' } })
    fireEvent.change(screen.getByLabelText('Enhanced command'), { target: { value: 'mycli acp' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add agent' }))

    expect(await screen.findByRole('heading', { name: 'Set up mycli' })).toBeDefined()
    expect(client.agentRegister).toHaveBeenCalledWith({
      name: 'mycli',
      executable: 'mycli',
      tui_args: ['mycli', '{task}'],
      headless_args: ['mycli', '-p', '{task}'],
      acp_args: ['mycli', 'acp'],
    })
  })
})

describe('add agent form', () => {
  it('registers an agent whose command differs from its name, with its mode and login', async () => {
    const client = fakeApi()
    renderStep(client)
    fireEvent.click(await screen.findByRole('button', { name: 'Add agent…' }))
    vi.mocked(client.agentList).mockResolvedValue([
      agentInfo({ name: 'mycursor', display_name: 'mycursor', source: 'member', glyph: 'custom', executable: 'agent' }),
    ])
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'mycursor' } })
    fireEvent.change(screen.getByLabelText('Standard command'), { target: { value: 'agent {task}' } })
    expect((screen.getByLabelText('Background command') as HTMLInputElement).value).toBe('agent -p {task}')
    fireEvent.change(screen.getByLabelText('Enhanced command'), { target: { value: 'agent acp' } })
    fireEvent.change(screen.getByLabelText('Enhanced start mode'), { target: { value: 'agent' } })
    fireEvent.change(screen.getByLabelText('Login file'), { target: { value: '~/.cursor/auth.json' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add agent' }))

    expect(await screen.findByRole('heading', { name: 'Set up mycursor' })).toBeDefined()
    expect(client.agentRegister).toHaveBeenCalledWith({
      name: 'mycursor',
      executable: 'agent',
      tui_args: ['agent', '{task}'],
      headless_args: ['agent', '-p', '{task}'],
      acp_args: ['agent', 'acp'],
      acp_mode: 'agent',
      credential_paths: ['~/.cursor/auth.json'],
    })
  })
})

describe('first run', () => {
  it('points back to the Agent step when no agent is installed', async () => {
    seed()
    const onBackToAgent = vi.fn()
    render(
      <FirstRunStep
        client={fakeApi({ agentList: vi.fn(async () => []) })}
        workspace={workspace}
        onBackToRepository={vi.fn()}
        onBackToAgent={onBackToAgent}
      />,
    )
    expect(await screen.findByText('No agent is installed yet')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Launch' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Set up an agent' }))
    expect(onBackToAgent).toHaveBeenCalledTimes(1)
  })
})

describe('onboarding route', () => {
  it('reaches the Agent step with the extras collapsed', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    fireEvent.click(await screen.findByRole('button', { name: `Use ${workspace.name}` }))
    fireEvent.click(await screen.findByRole('button', { name: 'Link local repository' }))
    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))

    expect(await screen.findByRole('region', { name: 'Agent' })).toBeDefined()
    expect(screen.queryByRole('region', { name: 'Agent config files' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /^Agent config files/ }))
    expect(screen.getByRole('region', { name: 'Agent config files' })).toBeDefined()
  })
})

import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { Api, EnvScanHandlers, EnvScanSession } from '@/lib/api'
import type { EnvScanRequest, GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { EnvironmentStep } from '@/routes/onboarding/environment-step'
import { useStore, type RootState } from '@/store'
import {
  alice,
  bob,
  fakeApi,
  scanResult,
  serverInfo,
  workspace,
} from '@/test/fixtures'

// The local gateway's descriptor: full method map, shell socket, and the
// client-machine verbs the wizard rides on.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'shell'],
  local: ['link.status', 'link.repo', 'pull', 'daemon.status'],
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    capabilities: localCaps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'onboarding', params: {} },
    ...extra,
  })
}

/** Walks the wizard from mount past the link step. */
async function toWorkspaceStep() {
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

/** Walks on to the environment step by picking the fixture workspace. */
async function toEnvironmentStep() {
  await toWorkspaceStep()
  fireEvent.click(
    await screen.findByRole('button', { name: `Use ${workspace.name}` }),
  )
}

/** Walks on to the repo step by keeping the standard environment. */
async function toRepoStep() {
  await toEnvironmentStep()
  fireEvent.click(
    await screen.findByRole('radio', { name: 'Keep the standard environment' }),
  )
  fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
}

/** Walks all the way to the first-run step, through a repo link. */
async function toFirstRunStep() {
  await toRepoStep()
  fireEvent.change(await screen.findByLabelText('Repository path'), {
    target: { value: '/home/alice/code/myproject' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

describe('onboarding wizard', () => {
  it('renders the desktop-only empty state on a remote gateway', () => {
    // The remote descriptor has no local verbs, so there is nothing to link.
    seed({
      capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    expect(screen.getByText(/runs in the desktop app/)).toBeDefined()
    expect(screen.queryByLabelText('Steps')).toBeNull()
  })

  it('checks link status on mount and steps to the workspace picker', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)

    // Linked: the summary carries the address and user from link.status,
    // and the store mirror is updated for the status bar.
    expect(await screen.findByText('host:2222')).toBeDefined()
    expect(screen.getByText('alice')).toBeDefined()
    expect(client.localLinkStatus).toHaveBeenCalledTimes(1)
    expect(useStore.getState().linkStatus?.linked).toBe(true)

    await toWorkspaceStep()
    expect(
      await screen.findByRole('region', { name: 'Workspace' }),
    ).toBeDefined()
    expect(client.workspaceListFull).toHaveBeenCalled()
  })

  it('offers a retry with terminal instructions when not linked', async () => {
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => ({
        linked: false,
        addr: '',
        user: '',
        repo: '',
      })),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)

    // The gateway has no SSH identity yet; the wizard says to link from a
    // terminal and re-checks on demand.
    expect(await screen.findByText(/aether link/)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(client.localLinkStatus).toHaveBeenCalledTimes(2)
  })

  // Every run in a workspace forks from its base branch, so creation settles
  // it once here rather than asking again per run. The environment rides the
  // shared choice component, standard by default.
  it('creates the first workspace on the standard environment by default', async () => {
    const client = fakeApi({ workspaceListFull: vi.fn(async () => []) })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toWorkspaceStep()

    const branch = await screen.findByLabelText('Base branch')
    expect(branch).toHaveProperty('value', 'main')
    // The shared environment cards render here, standard preselected.
    expect(
      screen.getByRole('radio', { name: /Standard environment/ }),
    ).toHaveProperty('checked', true)
    fireEvent.change(screen.getByLabelText(/^Name/), {
      target: { value: 'myproject' },
    })
    fireEvent.change(branch, { target: { value: 'trunk' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create workspace' }))

    await waitFor(() => {
      expect(client.workspaceAdd).toHaveBeenCalledWith({
        name: 'myproject',
        base_branch: 'trunk',
        environment: { custom_image: serverInfo.standard_image },
      })
    })
  })

  // A server predating server.info image refs cannot pin the standard
  // image, so the wizard falls back to today's behavior: the starter.
  it('defaults to the minimal starter when the server reports no standard image', async () => {
    const client = fakeApi({ workspaceListFull: vi.fn(async () => []) })
    seed({ info: { ...serverInfo, standard_image: undefined } })
    render(<OnboardingRoute params={{}} client={client} />)
    await toWorkspaceStep()

    await screen.findByLabelText('Base branch')
    expect(
      screen.queryByRole('radio', { name: /Standard environment/ }),
    ).toBeNull()
    fireEvent.change(screen.getByLabelText(/^Name/), {
      target: { value: 'myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Create workspace' }))

    await waitFor(() => {
      expect(client.workspaceAdd).toHaveBeenCalledWith({
        name: 'myproject',
        base_branch: 'main',
        environment: { neutral_image: true },
      })
    })
  })

  it('links the repo to the picked workspace and shows the push command', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))

    // The gateway wrote the remote; the push is the user's to run.
    expect(await screen.findByText('ssh://alice@host:2222/wsp_1')).toBeDefined()
    expect(client.localLinkRepo).toHaveBeenCalledWith(
      '/home/alice/code/myproject',
      workspace.id,
    )
    const cmd = screen.getByLabelText<HTMLInputElement>('Push command')
    expect(cmd.value).toContain('git push -u aether')
  })

  it('launches the first run in the chosen workspace and navigates to it', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    expect(await screen.findByRole('region', { name: 'First run' })).toBeDefined()
    // The workspace was settled two steps back, so nothing here asks for a
    // scope again.
    expect(screen.queryByLabelText('Workspace')).toBeNull()

    fireEvent.change(screen.getByLabelText('Harness'), {
      target: { value: 'claude' },
    })
    fireEvent.change(screen.getByLabelText('Task'), {
      target: { value: 'write a result file' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    expect(client.runLaunch).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      task: 'write a result file',
      harness: 'claude',
    })
    // runLaunch resolves to the fixture run; the wizard hands off to the
    // run view rather than holding a done screen.
    await waitFor(() => {
      expect(useStore.getState().route).toEqual({
        name: 'run',
        params: { runId: 'run_1' },
      })
    })
  })

  it('renders a launch refusal verbatim and lets the user retry', async () => {
    const runLaunch = vi
      .fn()
      .mockRejectedValueOnce(new Error('temporarily out of capacity'))
      .mockResolvedValue({ id: 'run_1' })
    const client = fakeApi({ runLaunch })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    fireEvent.change(await screen.findByLabelText('Harness'), {
      target: { value: 'claude' },
    })
    fireEvent.change(screen.getByLabelText('Task'), {
      target: { value: 'write a result file' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    expect(await screen.findByText('temporarily out of capacity')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() => {
      expect(client.runLaunch).toHaveBeenCalledTimes(2)
      expect(client.runLaunch).toHaveBeenLastCalledWith({
        workspace_id: workspace.id,
        task: 'write a result file',
        harness: 'claude',
      })
    })
  })

  it('places Environment between Workspace and Repository, mirror preselected', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toEnvironmentStep()

    // The breadcrumb names the step in position 3 and marks it current.
    const steps = screen.getByLabelText('Steps')
    expect(steps.textContent).toContain('3. Environment')
    const current = steps.querySelector('[aria-current="step"]')
    expect(current?.textContent).toContain('Environment')

    // Mirror is the recommended, preselected card; the picker lists only
    // the harnesses detected on this machine, by friendly name.
    expect(
      screen.getByRole('radio', { name: 'Mirror my machine' }),
    ).toHaveProperty('checked', true)
    const picker = await screen.findByLabelText('Coding agent')
    expect(picker.textContent).toContain('Claude Code')
    expect(picker.textContent).not.toContain('Codex')
    expect(client.envHarnesses).toHaveBeenCalled()
  })

  it('explains when no supported agent is installed and keeps the way on', async () => {
    const client = fakeApi({
      envHarnesses: vi.fn(async () => [
        { name: 'claude', installed: false },
        { name: 'codex', installed: false },
        { name: 'pi', installed: false },
        { name: 'amp', installed: false },
      ]),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toEnvironmentStep()

    expect(
      await screen.findByText(/No supported coding agent was found/),
    ).toBeDefined()
    expect(screen.getByText(/Claude Code, Codex, pi, or Amp/)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Start scan' })).toBeNull()

    // The keep card is the way forward.
    fireEvent.click(
      screen.getByRole('radio', { name: 'Keep the standard environment' }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(
      await screen.findByRole('region', { name: 'Repository' }),
    ).toBeDefined()
  })
})

describe('environment step scan flow', () => {
  function renderStep(client: Api) {
    seed()
    const onNext = vi.fn()
    const onReview = vi.fn()
    render(<EnvironmentStep client={client} onNext={onNext} onReview={onReview} />)
    return { onNext, onReview }
  }

  it('hands the validated pair to the review gate on success', async () => {
    const client = fakeApi()
    const { onReview, onNext } = renderStep(client)

    fireEvent.click(await screen.findByRole('button', { name: 'Start scan' }))

    await waitFor(() => {
      expect(onReview).toHaveBeenCalledWith({
        harness: 'claude',
        dockerfile: scanResult().dockerfile,
        manifest: scanResult().manifest,
      })
    })
    expect(client.openEnvScan).toHaveBeenCalledWith(
      { harness: 'claude', mode: 'inventory' },
      expect.anything(),
    )
    // Advancing is the review gate's decision, not the scan's.
    expect(onNext).not.toHaveBeenCalled()
  })

  it('streams output behind View process and cancels back to the choice', async () => {
    const close = vi.fn()
    const client = fakeApi({
      openEnvScan: vi.fn((_req: EnvScanRequest, h: EnvScanHandlers): EnvScanSession => {
        queueMicrotask(() => {
          h.onStatus('running')
          h.onOutput('inspecting go toolchain')
        })
        return { close }
      }),
    })
    renderStep(client)

    fireEvent.click(await screen.findByRole('button', { name: 'Start scan' }))
    expect(await screen.findByRole('status')).toBeDefined()

    // The raw agent output streams into the collapsed expander.
    expect(screen.getByText('View process')).toBeDefined()
    expect(await screen.findByText('inspecting go toolchain')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(close).toHaveBeenCalled()
    expect(
      screen.getByRole('radio', { name: 'Mirror my machine' }),
    ).toBeDefined()
  })

  it('offers try again and the standard fallback when the scan fails', async () => {
    const openEnvScan = vi.fn(
      (_req: EnvScanRequest, h: EnvScanHandlers): EnvScanSession => {
        queueMicrotask(() =>
          h.onError('the agent exited with status 1', 'last line'),
        )
        return { close: vi.fn() }
      },
    )
    const client = fakeApi({ openEnvScan })
    const { onNext } = renderStep(client)

    fireEvent.click(await screen.findByRole('button', { name: 'Start scan' }))
    expect(
      await screen.findByText('the agent exited with status 1'),
    ).toBeDefined()

    // Try again reopens the scan socket.
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(openEnvScan).toHaveBeenCalledTimes(2))

    // And the fallback simply advances the wizard.
    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Keep the standard environment',
      }),
    )
    expect(onNext).toHaveBeenCalled()
  })

  it('shows a non-admin only the keep path', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    const onNext = vi.fn()
    render(
      <EnvironmentStep client={fakeApi()} onNext={onNext} onReview={vi.fn()} />,
    )

    expect(screen.queryByRole('radio', { name: 'Mirror my machine' })).toBeNull()
    expect(screen.queryByLabelText('Coding agent')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onNext).toHaveBeenCalled()
  })
})

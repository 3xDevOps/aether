import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { Api, EnvScanHandlers, EnvScanSession } from '@/lib/api'
import type {
  EnvScanRequest,
  EnvScanResult,
  GatewayCapabilities,
} from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { EnvironmentStep } from '@/routes/onboarding/environment-step'
import { useStore, type RootState } from '@/store'
import {
  alice,
  bob,
  fakeApi,
  manifestItem,
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
    envBuilds: {},
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

/** A scan session that resolves with the given pair on the next tick. */
function scanWith(result: EnvScanResult) {
  return vi.fn((_req: EnvScanRequest, h: EnvScanHandlers): EnvScanSession => {
    queueMicrotask(() => h.onResult(result))
    return { close: vi.fn() }
  })
}

/** A two-item pair, so one item can go while the other's span shifts. */
function twoItemScan(): EnvScanResult {
  return {
    dockerfile:
      'FROM ubuntu:24.04\n' +
      '\n' +
      'RUN apt-get update \\\n' +
      '    && apt-get install -y --no-install-recommends jq=1.7.1-3build1 \\\n' +
      '    && rm -rf /var/lib/apt/lists/*\n' +
      'RUN install-go 1.22.1\n',
    manifest: [
      manifestItem(),
      manifestItem({
        name: 'go',
        version: '1.22.1',
        reason: 'the repository is a Go module',
        start_line: 6,
        end_line: 6,
        check_command: 'go version',
      }),
    ],
  }
}

describe('environment review gate', () => {
  async function toReviewGate(client: Api) {
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toEnvironmentStep()
    fireEvent.click(await screen.findByRole('button', { name: 'Start scan' }))
    await screen.findByRole('button', { name: 'Approve and build' })
  }

  it('lists the manifest and a removal shrinks the approved payload', async () => {
    const client = fakeApi({ openEnvScan: scanWith(twoItemScan()) })
    await toReviewGate(client)

    // The summary is a readable list: name, version, reason per row.
    expect(screen.getByText('jq')).toBeDefined()
    expect(screen.getByText('go')).toBeDefined()
    expect(screen.getByText('used by the project scripts')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Remove jq' }))
    // The last remaining item cannot be removed: the keep card is the
    // fallback, never an empty Dockerfile.
    expect(screen.getByRole('button', { name: 'Remove go' })).toHaveProperty(
      'disabled',
      true,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Approve and build' }))

    // The removed item's lines are gone and the later span has shifted.
    await waitFor(() => {
      expect(client.envSave).toHaveBeenCalledWith({
        workspace: { id: workspace.id },
        dockerfile: 'FROM ubuntu:24.04\n\nRUN install-go 1.22.1\n',
        manifest: [
          manifestItem({
            name: 'go',
            version: '1.22.1',
            reason: 'the repository is a Go module',
            start_line: 3,
            end_line: 3,
            check_command: 'go version',
          }),
        ],
        source: 'mirror',
        harness: 'claude',
      })
    })
  })

  it('approve saves then builds, primes the build slice, and advances', async () => {
    const client = fakeApi()
    await toReviewGate(client)

    fireEvent.click(screen.getByRole('button', { name: 'Approve and build' }))

    // The wizard moves on immediately; the build continues behind it.
    expect(
      await screen.findByRole('region', { name: 'Repository' }),
    ).toBeDefined()
    expect(client.envSave).toHaveBeenCalled()
    expect(client.envBuild).toHaveBeenCalledWith({ id: workspace.id }, 2)
    expect(
      vi.mocked(client.envSave).mock.invocationCallOrder[0],
    ).toBeLessThan(vi.mocked(client.envBuild).mock.invocationCallOrder[0])
    // The slice is primed before the build call, so no event frame can
    // beat the banner; it carries the pair for a later repair scan.
    expect(useStore.getState().envBuilds[workspace.id]).toMatchObject({
      version: 2,
      status: 'building',
      harness: 'claude',
      dockerfile: scanResult().dockerfile,
    })
  })

  it('request changes reopens the scan in refine mode with the note', async () => {
    const client = fakeApi()
    await toReviewGate(client)

    fireEvent.change(screen.getByLabelText('Request changes'), {
      target: { value: 'use node 20' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Send to the agent' }))

    await waitFor(() => {
      expect(client.openEnvScan).toHaveBeenLastCalledWith(
        {
          harness: 'claude',
          mode: 'refine',
          previous_dockerfile: scanResult().dockerfile,
          previous_manifest_json: JSON.stringify(scanResult().manifest),
          feedback: 'use node 20',
        },
        expect.anything(),
      )
    })
    // The refine result lands back in the same review gate.
    expect(
      await screen.findByRole('button', { name: 'Approve and build' }),
    ).toBeDefined()
  })
})

describe('environment build banner', () => {
  it('appears on the First-run step while building and clears when active', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    act(() =>
      useStore
        .getState()
        .startEnvBuild(workspace.id, { version: 2, status: 'building' }),
    )
    expect(
      await screen.findByText(/environment is still building/),
    ).toBeDefined()

    act(() =>
      useStore
        .getState()
        .applyEnvBuild(workspace.id, { version: 2, status: 'active' }),
    )
    await waitFor(() =>
      expect(screen.queryByText(/environment is still building/)).toBeNull(),
    )
  })

  it('offers repair and the standard fallback when verification fails', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    const pair = scanResult()
    act(() => {
      useStore.getState().startEnvBuild(workspace.id, {
        version: 2,
        status: 'building',
        harness: 'claude',
        dockerfile: pair.dockerfile,
        manifest: pair.manifest,
      })
      useStore.getState().applyEnvBuild(workspace.id, {
        version: 2,
        status: 'failed',
        detail: 'jq reported 1.6, the manifest claims 1.7.1',
      })
    })

    expect(await screen.findByText(/jq reported 1\.6/)).toBeDefined()
    expect(
      screen.getByRole('button', { name: 'Keep the standard environment' }),
    ).toBeDefined()

    // The repair is a refine scan seeded with the failure detail.
    fireEvent.click(
      screen.getByRole('button', { name: 'Ask the agent to fix it' }),
    )
    await waitFor(() => {
      expect(client.openEnvScan).toHaveBeenCalledWith(
        {
          harness: 'claude',
          mode: 'refine',
          previous_dockerfile: pair.dockerfile,
          previous_manifest_json: JSON.stringify(pair.manifest),
          feedback: 'jq reported 1.6, the manifest claims 1.7.1',
        },
        expect.anything(),
      )
    })
    // ...feeding back into the same review gate.
    expect(
      await screen.findByRole('button', { name: 'Approve and build' }),
    ).toBeDefined()
  })

  it('keeping the standard environment dismisses a failed build', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    act(() => {
      useStore.getState().startEnvBuild(workspace.id, {
        version: 2,
        status: 'building',
      })
      useStore.getState().applyEnvBuild(workspace.id, {
        version: 2,
        status: 'failed',
        detail: 'the build ran out of disk',
      })
    })

    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Keep the standard environment',
      }),
    )
    await waitFor(() =>
      expect(screen.queryByText(/ran out of disk/)).toBeNull(),
    )
    expect(useStore.getState().envBuilds[workspace.id]).toBeUndefined()
  })
})

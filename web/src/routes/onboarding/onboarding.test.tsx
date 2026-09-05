import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { useStore, type RootState } from '@/store'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'

// The local gateway's descriptor: full method map, event and attach sockets,
// plus the client-machine verbs the wizard rides on.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: ['link.status', 'link.repo', 'pull', 'repo.push', 'daemon.status'],
}

/** The same gateway one release older: it cannot push for the user. */
const noPushCaps: GatewayCapabilities = {
  ...localCaps,
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
    onboarded: false,
    onboardingStep: 0,
    onboardingWorkspace: '',
    ...extra,
  })
}

/** Walks the wizard from mount past the link step. */
async function toWorkspaceStep() {
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

/** Walks on to the repo step by picking the fixture workspace. */
async function toRepoStep() {
  await toWorkspaceStep()
  fireEvent.click(
    await screen.findByRole('button', { name: `Use ${workspace.name}` }),
  )
}

/** Walks on to the Agents step, through a repo link. */
async function toAgentsStep() {
  await toRepoStep()
  fireEvent.change(await screen.findByLabelText('Repository path'), {
    target: { value: '/home/alice/code/myproject' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

/** Walks all the way to the first-run step, skipping the agent setup and
 * the configuration import - both are optional. */
async function toFirstRunStep() {
  await toAgentsStep()
  fireEvent.click(await screen.findByRole('button', { name: 'Skip for now' }))
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
        server_configured: false,
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
  it('shows a configured server without a repository and opens the repo prompt', async () => {
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => ({
        server_configured: true,
        linked: false,
        addr: 'host:2222',
        user: 'alice',
        repo: '',
      })),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)

    expect(await screen.findByText('host:2222')).toBeDefined()
    expect(screen.getByText('alice')).toBeDefined()
    fireEvent.click(
      screen.getByRole('button', { name: 'Continue to repository' }),
    )

    expect(await screen.findByLabelText('Repository path')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Back to Workspace' })).toBeNull()
  })

  it('rechecks link status when the window regains focus', async () => {
    let status = {
      server_configured: false,
      linked: false,
      addr: '',
      user: '',
      repo: '',
    }
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => status),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    expect(await screen.findByText(/aether link/)).toBeDefined()

    status = {
      server_configured: true,
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
    }
    window.dispatchEvent(new Event('focus'))

    expect(await screen.findByText('/src/repo')).toBeDefined()
    expect(client.localLinkStatus).toHaveBeenCalledTimes(2)
  })

  it('creates the first workspace without an image selection', async () => {
    const client = fakeApi({ workspaceListFull: vi.fn(async () => []) })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toWorkspaceStep()

    const branch = await screen.findByLabelText('Base branch')
    expect(branch).toHaveProperty('value', 'main')
    fireEvent.change(screen.getByLabelText(/^Name/), {
      target: { value: 'myproject' },
    })
    fireEvent.change(branch, { target: { value: 'trunk' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create workspace' }))

    await waitFor(() => {
      expect(client.workspaceAdd).toHaveBeenCalledWith({
        name: 'myproject',
        base_branch: 'trunk',
        environment: {},
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

  /** Adds the remote, leaving the step on its push choices. */
  async function toPushChoice(client: Api) {
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()
    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await screen.findByLabelText('Push command')
  }

  it('pushes the base branch from the wizard and shows what git did', async () => {
    const client = fakeApi()
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))

    await waitFor(() => {
      expect(client.localRepoPush).toHaveBeenCalledWith(workspace.id)
    })
    // The confirmation names the branch that landed, and git's own words
    // stay on the page - "[new branch]" and "Everything up-to-date" are
    // both success and mean different things.
    expect(await screen.findByText(/Pushed/)).toBeDefined()
    const output = screen.getByText(/\[new branch\]/)
    expect(output.textContent).toBe(
      'To ssh://alice@host:2222/wsp_1\n * [new branch] main -> main',
    )
    // Open, not merely present: the reader who needs to tell "[new branch]"
    // from "Everything up-to-date" would not know to go looking.
    expect(output.closest('details')?.open).toBe(true)
    // Nothing invites a second push, and Continue moves on.
    expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    // The optional Agents step sits between Repository and First run.
    fireEvent.click(await screen.findByRole('button', { name: 'Skip for now' }))
    expect(
      await screen.findByRole('region', { name: 'First run' }),
    ).toBeDefined()
  })

  it('shows git verbatim when the push is refused and stays put', async () => {
    const refusal =
      'To ssh://alice@host:2222/wsp_1\n' +
      ' ! [remote rejected] main -> main (protected branch hook declined)\n' +
      "error: failed to push some refs to 'ssh://alice@host:2222/wsp_1'"
    const client = fakeApi({
      localRepoPush: vi.fn(async () => {
        throw new Error(refusal)
      }),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))

    // Every line git printed, newlines kept - the user reads the real reason.
    const output = await screen.findByText(/remote rejected/)
    expect(output.textContent).toBe(refusal)
    expect(screen.queryByRole('region', { name: 'First run' })).toBeNull()
    // Both ways out survive the failure: retry, or run the command by hand.
    expect(screen.getByRole('button', { name: 'Push now' })).toBeDefined()
    expect(
      screen.getByLabelText<HTMLInputElement>('Push command').value,
    ).toBe('git push -u aether main')
  })

  it('falls back to the copy-paste push when the gateway cannot push', async () => {
    const client = fakeApi()
    seed({ capabilities: noPushCaps })
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()
    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))

    expect(
      await screen.findByLabelText<HTMLInputElement>('Push command'),
    ).toHaveProperty('value', 'git push -u aether main')
    expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    // The optional Agents step sits between Repository and First run.
    fireEvent.click(await screen.findByRole('button', { name: 'Skip for now' }))
    expect(
      await screen.findByRole('region', { name: 'First run' }),
    ).toBeDefined()
    expect(client.localRepoPush).not.toHaveBeenCalled()
  })

  it('names the workspace base branch in the push command', async () => {
    // A workspace forked from `trunk` is seeded from `trunk`, not `main`.
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [
        { ...workspace, base_branch: 'trunk' },
      ]),
    })
    await toPushChoice(client)

    expect(
      screen.getByLabelText<HTMLInputElement>('Push command').value,
    ).toBe('git push -u aether trunk')
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
  it('finishes onboarding by going to the board', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toFirstRunStep()

    fireEvent.click(screen.getByRole('button', { name: 'Go to board' }))

    expect(useStore.getState()).toMatchObject({
      onboarded: true,
      onboardingStep: 0,
      onboardingWorkspace: '',
      route: { name: 'board', params: {} },
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
})

import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import type { Api } from '@/lib/api'
import type {
  AgentInfo,
  GatewayCapabilities,
  GitIdentity,
  RepoPushResult,
  RepoPushState,
} from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { FirstRunStep } from '@/routes/onboarding/steps'
import { useStore, type RootState } from '@/store'
import { onboardingStepIndex, onboardingSteps } from '@/store/ui'
import {
  agentInfo,
  alice,
  fakeApi,
  otherWorkspace,
  serverInfo,
  workspace,
} from '@/test/fixtures'

// The local gateway's descriptor: full method map, event and attach sockets,
// plus the client-machine verbs the wizard rides on.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: [
    'link.status',
    'link.repo',
    'git.identity',
    'pull',
    'repo.push',
    'repo.fast-forward',
    'daemon.status',
  ],
}

/**
 * The same gateway one release older: it can neither read this machine's git
 * config nor push for the user.
 */
const olderCaps: GatewayCapabilities = {
  ...localCaps,
  local: ['link.status', 'link.repo', 'pull', 'daemon.status'],
}

const localTip = '9f1c2ab3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9'
const workspaceTip = '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b'

/** A repo.push answer that compared the two tips and pushed nothing. */
function compared(
  state: RepoPushState,
  over: Partial<RepoPushResult> = {},
): RepoPushResult {
  return {
    branch: 'main',
    remote: 'aether',
    state,
    local_commit: localTip,
    workspace_commit: workspaceTip,
    ahead: 0,
    behind: 0,
    output: 'From ssh://alice@host:2222/wsp_1\n * branch main -> FETCH_HEAD',
    ...over,
  }
}

/** A Repository step that has already connected the fixture workspace. */
const connectedRepo = {
  link: 'lnk_connected',
  workspace: workspace.id,
  path: '/home/alice/code/myproject',
  remote: {
    repo: '/home/alice/code/myproject',
    remote: 'aether',
    url: `ssh://alice@host:2222/${workspace.id}`,
  },
  push: null,
  fastForward: null,
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
    onboardingStep: 'Link',
    onboardingFurthest: 'Link',
    onboardingWorkspace: '',
    onboardingRepo: null,
    onboardingFirstRun: { harness: '', task: '' },
    ...extra,
  })
}

/** Walks the wizard from mount past the link step. */
async function toGitIdentityStep() {
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

/** Walks on past the git identity step, which is optional. */
async function toWorkspaceStep() {
  await toGitIdentityStep()
  fireEvent.click(await screen.findByRole('button', { name: 'Skip' }))
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

  it('links an unlinked gateway in the app and steps to the workspace picker', async () => {
    const client = fakeApi({
      localLinkStatus: vi
        .fn()
        .mockResolvedValueOnce({
          server_configured: false,
          linked: false,
          addr: '',
          user: '',
          repo: '',
        })
        .mockResolvedValue({
          server_configured: true,
          linked: true,
          addr: 'host:2222',
          user: 'alice',
          repo: '',
        }),
      localLinkApply: vi.fn(async () => ({
        addr: 'host:2222',
        user: 'aether',
        member: { id: 'member_1', display_name: 'Alice', role: 'admin' },
        key_generated: '/home/alice/.ssh/id_ed25519',
      })),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)

    expect(await screen.findByLabelText('Server address')).toBeDefined()
    fireEvent.change(screen.getByLabelText('Server address'), {
      target: { value: 'host:2222' },
    })
    fireEvent.change(screen.getByLabelText('Invite code'), {
      target: { value: 'invite-123' },
    })
    fireEvent.change(screen.getByLabelText('Your name'), {
      target: { value: 'Alice' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Link' }))

    expect(client.localLinkApply).toHaveBeenCalledWith({
      addr: 'host:2222',
      invite: 'invite-123',
      name: 'Alice',
    })
    const summary = await screen.findByText(/^Linked to/)
    expect(summary.textContent).toBe('Linked to host:2222 as Alice (admin).')
    expect(screen.getByText(/^Created SSH key/).textContent).toBe(
      'Created SSH key /home/alice/.ssh/id_ed25519.',
    )
    expect(useStore.getState().connectionEpoch).toBe(1)
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(screen.getByRole('listitem', { current: 'step' }).textContent).toContain(
      '2. Git identity',
    )
  })
  it('shows a configured server without a repository and opens the workspace picker', async () => {
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
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Skip' }))

    expect(await screen.findByRole('region', { name: 'Workspace' })).toBeDefined()
    expect(screen.queryByLabelText('Repository path')).toBeNull()
  })
  it('returns to the workspace step when a persisted repo step has no workspace', async () => {
    seed({ onboardingStep: 'Repository', onboardingWorkspace: '' })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    expect(screen.getByRole('listitem', { current: 'step' }).textContent).toContain(
      '3. Workspace',
    )
    expect(await screen.findByRole('region', { name: 'Workspace' })).toBeDefined()
  })
  it('returns to the workspace step from every step past Repository', async () => {
    // Repository is where the workspace first becomes load-bearing, so the
    // guard covers it and everything after it, whatever their positions.
    for (const step of onboardingSteps.slice(
      onboardingStepIndex('Repository'),
    )) {
      seed({ onboardingStep: step, onboardingWorkspace: '' })
      const view = render(<OnboardingRoute params={{}} client={fakeApi()} />)
      expect(
        await view.findByRole('region', { name: 'Workspace' }),
      ).toBeDefined()
      view.unmount()
    }
  })
  it('resumes where it left off on the steps before Repository', async () => {
    // Nothing before Repository needs a workspace, so a missing one must
    // not drag the wizard forward or back.
    seed({ onboardingStep: 'Git identity', onboardingWorkspace: '' })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    expect(
      await screen.findByRole('region', { name: 'Git identity' }),
    ).toBeDefined()
    expect(screen.getByRole('listitem', { current: 'step' }).textContent).toContain(
      '2. Git identity',
    )
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
    expect(await screen.findByLabelText('Server address')).toBeDefined()

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

  it('prefills the git identity from this machine and saves it', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()

    expect(await screen.findByRole('region', { name: 'Git identity' })).toBeDefined()
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe(
        'Alice Local',
      )
    })
    expect(screen.getByLabelText<HTMLInputElement>('Email').value).toBe(
      'alice@example.invalid',
    )

    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(client.memberGit).toHaveBeenCalledWith(
        'Ada Lovelace',
        'alice@example.invalid',
      )
    })
    expect(await screen.findByRole('region', { name: 'Workspace' })).toBeDefined()
  })

  it('keeps the identity the member already saved over the machine one', async () => {
    const client = fakeApi()
    seed({
      info: { ...serverInfo, member: { ...alice, git_name: 'Ada Server' } },
    })
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()

    await screen.findByRole('region', { name: 'Git identity' })
    // The machine's address landing in the empty email is what says the
    // probe finished; the saved name is asserted after that, not before.
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Email').value).toBe(
        'alice@example.invalid',
      )
    })
    expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe('Ada Server')
  })

  it('shows the refusal verbatim and stays on the git identity step', async () => {
    const client = fakeApi({
      memberGit: vi.fn(async () => {
        throw new Error('git email must contain @')
      }),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe(
        'Alice Local',
      )
    })

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText('git email must contain @')).toBeDefined()
    expect(screen.queryByRole('region', { name: 'Workspace' })).toBeNull()
  })

  it('skips the git identity step without saving one', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()

    fireEvent.click(await screen.findByRole('button', { name: 'Skip' }))

    expect(await screen.findByRole('region', { name: 'Workspace' })).toBeDefined()
    expect(client.memberGit).not.toHaveBeenCalled()
  })

  it('keeps a concurrent info change made while the identity save is in flight', async () => {
    let land = () => {}
    const client = fakeApi({
      memberGit: vi.fn(async (name: string, email: string) => {
        await new Promise<void>((resolve) => {
          land = resolve
        })
        return { ...alice, git_name: name, git_email: email }
      }),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe(
        'Alice Local',
      )
    })

    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.memberGit).toHaveBeenCalled())

    // What a disk-usage refresh does: write the whole info back with its
    // own field changed, while the save is still on the wire.
    act(() =>
      useStore
        .getState()
        .setInfo({ ...serverInfo, tailnet_hostname: 'gateway.tailnet.ts.net' }),
    )
    land()

    await screen.findByRole('region', { name: 'Workspace' })
    expect(useStore.getState().info?.member.git_name).toBe('Ada Lovelace')
    expect(useStore.getState().info?.tailnet_hostname).toBe(
      'gateway.tailnet.ts.net',
    )
  })

  it('shows the saved identity when the user walks back into the step', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe(
        'Alice Local',
      )
    })

    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByRole('region', { name: 'Workspace' })

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    // What the server answered, not the machine's git config: the save
    // refreshed the stored member, so the re-run probe has nothing to fill.
    await screen.findByRole('region', { name: 'Git identity' })
    await waitFor(() => {
      expect(client.localGitIdentity).toHaveBeenCalledTimes(2)
    })
    expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe(
      'Ada Lovelace',
    )
    expect(screen.getByLabelText<HTMLInputElement>('Email').value).toBe(
      'alice@example.invalid',
    )
  })

  it('stays usable when this machine has no git identity to read', async () => {
    const client = fakeApi({
      localGitIdentity: vi.fn(async () => {
        throw new Error('git config: no user.name set')
      }),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()

    expect(await screen.findByText('git config: no user.name set')).toBeDefined()
    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.change(screen.getByLabelText('Email'), {
      target: { value: 'ada@example.invalid' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(client.memberGit).toHaveBeenCalledWith(
        'Ada Lovelace',
        'ada@example.invalid',
      )
    })
    expect(await screen.findByRole('region', { name: 'Workspace' })).toBeDefined()
  })

  it('asks a gateway without the git.identity verb for nothing', async () => {
    const client = fakeApi()
    seed({ capabilities: olderCaps })
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()

    expect(await screen.findByRole('region', { name: 'Git identity' })).toBeDefined()
    expect(client.localGitIdentity).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.change(screen.getByLabelText('Email'), {
      target: { value: 'ada@example.invalid' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(client.memberGit).toHaveBeenCalledWith(
        'Ada Lovelace',
        'ada@example.invalid',
      )
    })
  })

  /** A probe that stays in flight until the test resolves it. */
  function deferredIdentity() {
    let resolve: (identity: GitIdentity) => void = () => {}
    const client = fakeApi({
      localGitIdentity: vi.fn(
        () =>
          new Promise<GitIdentity>((r) => {
            resolve = r
          }),
      ),
    })
    return { client, settle: () => resolve({ name: 'Alice Local', email: 'alice@example.invalid' }) }
  }

  it('keeps what the user typed while the machine identity was still coming', async () => {
    const { client, settle } = deferredIdentity()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()
    await screen.findByRole('region', { name: 'Git identity' })

    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    settle()

    // The untouched email takes the machine's answer, which is what says the
    // probe landed; the typed name is untouched by it.
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Email').value).toBe(
        'alice@example.invalid',
      )
    })
    expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe(
      'Ada Lovelace',
    )
  })

  it('leaves a field the user cleared on purpose empty', async () => {
    const { client, settle } = deferredIdentity()
    seed({
      info: {
        ...serverInfo,
        member: { ...alice, git_email: 'ada@server.invalid' },
      },
    })
    render(<OnboardingRoute params={{}} client={client} />)
    await toGitIdentityStep()
    await screen.findByRole('region', { name: 'Git identity' })

    fireEvent.change(screen.getByLabelText('Email'), { target: { value: '' } })
    settle()

    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe(
        'Alice Local',
      )
    })
    expect(screen.getByLabelText<HTMLInputElement>('Email').value).toBe('')
  })

  it('will not save half an identity', async () => {
    seed({ capabilities: olderCaps })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toGitIdentityStep()

    const save = await screen.findByRole('button', { name: 'Save' })
    expect(save).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    expect(save).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByLabelText('Email'), {
      target: { value: 'ada@example.invalid' },
    })
    expect(save).toHaveProperty('disabled', false)
    // Whitespace is not a name; the server would refuse it anyway.
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: '  ' } })
    expect(save).toHaveProperty('disabled', true)
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
      expect(useStore.getState().activeWorkspace).toBe(workspace.id)
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
    expect(client.localLinkStatus).toHaveBeenCalledTimes(2)
    expect(useStore.getState().linkStatus?.repo).toBe('/src/repo')
    const cmd = screen.getByLabelText<HTMLInputElement>('Push command')
    expect(cmd.value).toContain('git push -u aether')
    // This clone had no pushable origin to record, so the step claims none.
    expect(screen.queryByText(/Runs push to/)).toBeNull()
  })

  it('names the upstream origin the link recorded', async () => {
    const client = fakeApi({
      localLinkRepo: vi.fn(async () => ({
        repo: '/src/repo',
        remote: 'aether',
        url: 'ssh://alice@host:2222/wsp_1',
        origin: 'https://github.com/acme/app.git',
      })),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))

    expect(await screen.findByText(/Runs push to/)).toBeDefined()
    expect(screen.getByText('https://github.com/acme/app.git')).toBeDefined()
  })

  /** Adds the remote, leaving the step on its push choices. */
  async function toPushChoice(client: Api, extra: Partial<RootState> = {}) {
    seed(extra)
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
    // The command stays copyable: the two tips agree, so it is still the
    // command that would seed the workspace.
    expect(
      screen.getByLabelText<HTMLInputElement>('Push command').value,
    ).toBe('git push -u aether main')
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

  it('reports a workspace that already has the branch', async () => {
    const client = fakeApi({
      localRepoPush: vi.fn(async () =>
        compared('up-to-date', { workspace_commit: localTip }),
      ),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))

    // Nothing was pushed, and the step is settled: the tip the workspace
    // already carries is the whole answer.
    const settled = await screen.findByText(/Workspace already has/)
    expect(settled.textContent).toContain(
      'Workspace already has main at 9f1c2ab. Nothing to push.',
    )
    expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()
    expect(
      screen.getByText(/FETCH_HEAD/).closest('details')?.open,
    ).toBe(true)
  })

  it('fast-forwards the clone when the workspace is ahead', async () => {
    const client = fakeApi({
      localRepoPush: vi.fn(async () => compared('behind', { behind: 2 })),
      localRepoFastForward: vi.fn(async () => ({
        branch: 'main',
        commit: workspaceTip,
        current: true,
        dirty: false,
        output: 'Updating 9f1c2ab..1a2b3c4\nFast-forward',
      })),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))

    expect(
      await screen.findByText('The workspace is 2 commits ahead of your clone.'),
    ).toBeDefined()
    expect(screen.getByText('9f1c2ab')).toBeDefined()
    expect(screen.getByText('1a2b3c4')).toBeDefined()
    // The push offer stays - a member who resolves by hand retries with it -
    // but `git push` is the wrong command here, so it is not the one on
    // offer to copy.
    expect(screen.getByRole('button', { name: 'Push now' })).toBeDefined()
    expect(screen.queryByLabelText('Push command')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Fast-forward my clone' }))

    await waitFor(() => {
      expect(client.localRepoFastForward).toHaveBeenCalledWith(workspace.id)
    })
    const settled = await screen.findByText(/Fast-forwarded/)
    expect(settled.textContent).toBe('Fast-forwarded main to 1a2b3c4.')
    // Both commands git ran, in the order they ran: the compare's fetch,
    // then the fast-forward.
    expect(screen.getByText(/FETCH_HEAD/).textContent).toBe(
      'From ssh://alice@host:2222/wsp_1\n * branch main -> FETCH_HEAD\n\n' +
        'Updating 9f1c2ab..1a2b3c4\nFast-forward',
    )
    expect(
      screen.queryByRole('button', { name: 'Fast-forward my clone' }),
    ).toBeNull()
    expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()

    // The answer outlives the step: walking back shows it settled rather
    // than offering the button a second time.
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByRole('region', { name: 'Agents' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(await screen.findByText(/Fast-forwarded/)).toBeDefined()
    expect(client.localRepoFastForward).toHaveBeenCalledTimes(1)
  })

  it('leaves the working tree alone when another branch is checked out', async () => {
    const client = fakeApi({
      localRepoPush: vi.fn(async () => compared('behind', { behind: 1 })),
      localRepoFastForward: vi.fn(async () => ({
        branch: 'main',
        commit: workspaceTip,
        current: false,
        dirty: true,
        output: 'From ssh://alice@host:2222/wsp_1\n * branch main -> FETCH_HEAD',
      })),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
    expect(
      await screen.findByText('The workspace is 1 commit ahead of your clone.'),
    ).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Fast-forward my clone' }))

    // The ref moved, the checkout did not, and the dirty tree is named:
    // all three are things the user has to know before the next step.
    const settled = await screen.findByText(/Updated/)
    expect(settled.textContent).toBe(
      'Updated main to 1a2b3c4. Another branch is checked out, so the ' +
        'fast-forward left your working tree alone. The uncommitted changes ' +
        'you already had are still there.',
    )
  })

  it('hands a diverged clone the commands to resolve it', async () => {
    const client = fakeApi({
      localRepoPush: vi.fn(async () =>
        compared('diverged', { ahead: 1, behind: 2 }),
      ),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))

    const explained = await screen.findByText(/both moved on/)
    expect(explained.textContent).toBe(
      'Your clone and the workspace have both moved on: 1 commit here, 2 ' +
        'there. Aether never force-pushes.',
    )
    expect(screen.getByText('9f1c2ab')).toBeDefined()
    expect(screen.getByText('1a2b3c4')).toBeDefined()
    // A fast-forward would lose the local commits, so it is not offered.
    expect(
      screen.queryByRole('button', { name: 'Fast-forward my clone' }),
    ).toBeNull()
    const commands =
      'git fetch aether main\n' +
      'git log --oneline --left-right main...aether/main\n' +
      'git rebase aether/main\n' +
      'git push aether main'
    expect(screen.getByText(/git rebase/).textContent).toBe(commands)
    // The commands that resolve this are the copyable ones. A bare
    // `git push` is what the workspace just rejected, so it is not offered.
    expect(screen.queryByLabelText('Push command')).toBeNull()
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    fireEvent.click(
      screen.getByRole('button', { name: 'Copy resolve commands' }),
    )
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(commands))
    vi.unstubAllGlobals()

    // Push now stays: it re-compares, which is what a member does after
    // resolving by hand.
    expect(screen.getByRole('button', { name: 'Push now' })).toBeDefined()
  })

  it('remembers the connected repository when the user walks back to it', async () => {
    const client = fakeApi()
    await toPushChoice(client)
    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
    await screen.findByText(/Pushed/)

    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByRole('region', { name: 'Agents' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    // The connected state, not the blank form: the remote is already
    // written and the push already happened.
    expect(await screen.findByRole('region', { name: 'Repository' })).toBeDefined()
    expect(screen.queryByLabelText('Repository path')).toBeNull()
    expect(screen.getByText('/home/alice/code/myproject')).toBeDefined()
    expect(screen.getByText(/Pushed/)).toBeDefined()
    expect(client.localLinkRepo).toHaveBeenCalledTimes(1)

    // Re-pointing is the way back to the form, with the old path to edit.
    fireEvent.click(
      screen.getByRole('button', { name: 'Use a different repository' }),
    )
    expect(
      screen.getByLabelText<HTMLInputElement>('Repository path').value,
    ).toBe('/home/alice/code/myproject')
  })

  /** Re-points the step at a second clone, leaving it connected. */
  async function toOtherClone() {
    fireEvent.click(
      screen.getByRole('button', { name: 'Use a different repository' }),
    )
    fireEvent.change(screen.getByLabelText('Repository path'), {
      target: { value: '/home/alice/code/other' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await screen.findByText('/home/alice/code/other')
  }

  it('drops a push answer that lands after the clone was re-pointed', async () => {
    let land: (result: RepoPushResult) => void = () => {}
    const client = fakeApi({
      localRepoPush: vi.fn(
        async () =>
          new Promise<RepoPushResult>((resolve) => {
            land = resolve
          }),
      ),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
    await toOtherClone()

    // The new clone offers its own push rather than the old one's spinner.
    expect(
      screen.getByRole('button', { name: 'Push now' }),
    ).toHaveProperty('disabled', false)

    await act(async () => {
      land(compared('pushed', { workspace_commit: localTip }))
    })

    // The answer belongs to the clone that was pushed, not the one on screen.
    expect(screen.queryByText(/Pushed/)).toBeNull()
    expect(useStore.getState().onboardingRepo?.path).toBe(
      '/home/alice/code/other',
    )
    expect(useStore.getState().onboardingRepo?.push).toBeNull()
    expect(screen.getByRole('button', { name: 'Push now' })).toBeDefined()
  })

  it('drops a push answer that lands after the same clone was reconnected', async () => {
    // Re-pointing at the same folder and the same workspace makes a new
    // connection, not the old one: the gateway wrote the remote again. An
    // answer from the previous connection has to be dropped just the same,
    // and path and workspace cannot tell the two apart.
    let land: (result: RepoPushResult) => void = () => {}
    const client = fakeApi({
      localRepoPush: vi.fn(
        async () =>
          new Promise<RepoPushResult>((resolve) => {
            land = resolve
          }),
      ),
    })
    await toPushChoice(client)
    const first = useStore.getState().onboardingRepo?.link

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
    fireEvent.click(
      screen.getByRole('button', { name: 'Use a different repository' }),
    )
    // The form comes back prefilled with the same path; the member changes
    // their mind and reconnects it.
    fireEvent.click(await screen.findByRole('button', { name: 'Add remote' }))
    await screen.findByLabelText('Push command')

    const second = useStore.getState().onboardingRepo
    expect(second?.path).toBe('/home/alice/code/myproject')
    expect(second?.link).not.toBe(first)

    await act(async () => {
      land(compared('pushed', { workspace_commit: localTip }))
    })

    expect(screen.queryByText(/Pushed/)).toBeNull()
    expect(useStore.getState().onboardingRepo?.push).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Push now' }),
    ).toHaveProperty('disabled', false)
  })

  it('drops a push failure that lands after the clone was re-pointed', async () => {
    let refuse: (err: Error) => void = () => {}
    const client = fakeApi({
      localRepoPush: vi.fn(
        async () =>
          new Promise<RepoPushResult>((_, reject) => {
            refuse = reject
          }),
      ),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
    await toOtherClone()

    await act(async () => {
      refuse(new Error('! [remote rejected] main -> main'))
    })

    expect(screen.queryByText(/The push failed/)).toBeNull()
    expect(screen.queryByText(/remote rejected/)).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Push now' }),
    ).toHaveProperty('disabled', false)
  })

  it('resumes on Repository with its clone connected and walks back to Workspace', async () => {
    // What an older release left behind: its resume point was an index and
    // its repository answer already carried the comparison states. Migrated,
    // the two have to fit together - the wizard opens on Repository with the
    // clone still connected, and Back reaches Workspace rather than the step
    // inserted in front of it.
    const client = fakeApi()
    seed({
      onboardingStep: 'Repository',
      onboardingWorkspace: workspace.id,
      onboardingRepo: connectedRepo,
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(
      await screen.findByRole('region', { name: 'Repository' }),
    ).toBeDefined()
    expect(screen.getByText('/home/alice/code/myproject')).toBeDefined()
    expect(screen.queryByLabelText('Repository path')).toBeNull()
    expect(client.localLinkRepo).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(await screen.findByRole('region', { name: 'Workspace' })).toBeDefined()

    fireEvent.click(
      screen.getByRole('button', { name: `Use ${workspace.name}` }),
    )
    expect(
      await screen.findByRole('region', { name: 'Repository' }),
    ).toBeDefined()
    expect(screen.getByText('/home/alice/code/myproject')).toBeDefined()
  })

  it('keeps the connected clone while the identity step is walked through', async () => {
    // Forward from the start: the Git identity step sits between Link and
    // Workspace, and passing it must not disturb the answer Repository
    // already holds.
    const client = fakeApi()
    seed({ onboardingWorkspace: workspace.id, onboardingRepo: connectedRepo })
    render(<OnboardingRoute params={{}} client={client} />)

    await toRepoStep()

    expect(
      await screen.findByRole('region', { name: 'Repository' }),
    ).toBeDefined()
    expect(screen.getByText('/home/alice/code/myproject')).toBeDefined()
    expect(client.memberGit).not.toHaveBeenCalled()
  })

  it('forgets the connected repository when the workspace changes', async () => {
    // A remote points at one workspace. Changing the workspace after
    // connecting leaves that answer stale, and the new one is unseeded.
    const client = fakeApi()
    await toPushChoice(client)
    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
    await screen.findByText(/Pushed/)

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    fireEvent.click(
      await screen.findByRole('button', { name: `Use ${otherWorkspace.name}` }),
    )

    expect(await screen.findByRole('region', { name: 'Repository' })).toBeDefined()
    expect(screen.getByLabelText<HTMLInputElement>('Repository path').value).toBe('')
    expect(screen.queryByText('ssh://alice@host:2222/wsp_1')).toBeNull()
    expect(screen.queryByText(/Pushed/)).toBeNull()
    expect(client.localLinkRepo).toHaveBeenCalledTimes(1)
  })

  it('falls back to the copy-paste push when the gateway cannot push', async () => {
    const client = fakeApi()
    seed({ capabilities: olderCaps })
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

    fireEvent.change(screen.getByLabelText('Agent'), {
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
      onboardingStep: 'Link',
      onboardingWorkspace: '',
      route: { name: 'board', params: {} },
    })
  })

  it('offers only the agents installed in this account', async () => {
    // agent.list always carries every shipped name; only the installed ones
    // can actually launch, so only they are offered.
    const client = fakeApi({
      agentList: vi.fn(async () => [
        agentInfo(),
        agentInfo({ name: 'codex', installed: false }),
      ]),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    const picker = await screen.findByLabelText<HTMLSelectElement>('Agent')
    const options = [...picker.options].map((o) => o.value)
    expect(options).toEqual(['', 'claude'])
  })

  it('sends the reader back to Agents when nothing is installed', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ installed: false })]),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    expect(
      await screen.findByText(/no agent is installed in your environment yet/i),
    ).toBeDefined()
    expect(screen.queryByLabelText('Agent')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Launch' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Set up an agent' }))

    expect(
      await screen.findByRole('region', { name: 'Agents' }),
    ).toBeDefined()
    expect(
      screen.getByRole('listitem', { current: 'step' }).textContent,
    ).toContain('5. Agents')
  })

  it('keeps a failed agent.list on screen rather than calling it empty', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => {
        throw new Error('agent.list: environment home unreadable')
      }),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    expect(
      await screen.findByText('agent.list: environment home unreadable'),
    ).toBeDefined()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeDefined()
  })

  it('waits rather than claiming an empty account while a retry is in flight', async () => {
    let answer: (agents: AgentInfo[]) => void = () => {}
    // The Agents step asks first, so the failure is the standing answer until
    // the retry, which is left hanging.
    let hang = false
    const client = fakeApi({
      agentList: vi.fn(() =>
        hang
          ? new Promise<AgentInfo[]>((resolve) => {
              answer = resolve
            })
          : Promise.reject(new Error('agent.list: environment home unreadable')),
      ),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    hang = true
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))

    // The gateway has said nothing yet, so neither does the step.
    expect(screen.queryByText(/no agent is installed/i)).toBeNull()
    expect(
      screen.queryByText('agent.list: environment home unreadable'),
    ).toBeNull()

    await act(async () => answer([agentInfo()]))
    expect(screen.getByLabelText('Agent')).toBeDefined()
  })

  it('jumps between the steps it has reached from the header', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toFirstRunStep()

    const steps = screen.getByLabelText('Steps')
    const chip = (name: string) =>
      within(steps).queryByRole('button', { name: `${name}, done - go to this step` })

    fireEvent.click(chip('3. Workspace')!)

    expect(await screen.findByRole('region', { name: 'Workspace' })).toBeDefined()
    expect(useStore.getState().onboardingStep).toBe('Workspace')
    // The step it landed on is the current one, so it is not a jump target.
    expect(chip('3. Workspace')).toBeNull()
    // Everything already reached stays reachable, or a jump backwards would
    // strand the member on a step whose own Back is gone.
    expect(useStore.getState().onboardingFurthest).toBe('First run')
    fireEvent.click(chip('6. First run')!)
    expect(await screen.findByRole('region', { name: 'First run' })).toBeDefined()
  })

  it('leaves a step it has never reached inert in the header', () => {
    seed({ onboardingStep: 'Git identity', onboardingFurthest: 'Workspace' })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    const steps = screen.getByLabelText('Steps')
    const names = within(steps)
      .getAllByRole('button')
      .map((chip) => chip.getAttribute('aria-label'))

    expect(names).toEqual([
      '1. Link, done - go to this step',
      '3. Workspace, done - go to this step',
    ])
  })

  it('drops a draft agent this account no longer has installed', async () => {
    // The draft is persisted, so it outlives the account that could run it.
    seed({ onboardingFirstRun: { harness: 'claude', task: 'write a result file' } })
    const client = fakeApi({
      agentList: vi.fn(async () => [
        agentInfo({ installed: false }),
        agentInfo({ name: 'codex', installed: true }),
      ]),
    })
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    await waitFor(() => {
      expect(screen.getByLabelText<HTMLSelectElement>('Agent').value).toBe('')
    })
    expect(useStore.getState().onboardingFirstRun.harness).toBe('')
    expect(
      screen.getByRole('button', { name: 'Launch' }),
    ).toHaveProperty('disabled', true)
  })

  it('keeps launch blocked until the agent list has answered', async () => {
    // The draft is on screen at once; the list that decides whether its agent
    // can run is a round trip behind it.
    seed({ onboardingFirstRun: { harness: 'claude', task: 'write a result file' } })
    const client = fakeApi({
      agentList: vi.fn(() => new Promise<AgentInfo[]>(() => {})),
    })
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    expect(
      await screen.findByRole('button', { name: 'Launch' }),
    ).toHaveProperty('disabled', true)
  })

  it('keeps the agent the member picked over the one Agents set up', async () => {
    seed({ onboardingFirstRun: { harness: 'myagent', task: 'write a file' } })
    render(
      <FirstRunStep
        client={fakeApi()}
        workspace={workspace}
        defaultHarness="claude"
        onBackToAgents={() => {}}
      />,
    )

    await waitFor(() => {
      expect(screen.getByLabelText<HTMLSelectElement>('Agent').value).toBe('myagent')
    })
  })

  it('keeps the first run draft when the header jumps away and back', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toFirstRunStep()

    fireEvent.change(await screen.findByLabelText('Task'), {
      target: { value: 'add a health check endpoint' },
    })
    const steps = screen.getByLabelText('Steps')
    fireEvent.click(
      within(steps).getByRole('button', {
        name: '3. Workspace, done - go to this step',
      }),
    )
    await screen.findByRole('region', { name: 'Workspace' })
    fireEvent.click(
      within(steps).getByRole('button', {
        name: '6. First run, done - go to this step',
      }),
    )

    expect(
      (await screen.findByLabelText<HTMLTextAreaElement>('Task')).value,
    ).toBe('add a health check endpoint')
  })

  it('renders Back inside the step it belongs to', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toAgentsStep()

    const agents = await screen.findByRole('region', { name: 'Agents' })
    expect(within(agents).getByRole('button', { name: 'Back' })).toBeDefined()
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

    fireEvent.change(await screen.findByLabelText('Agent'), {
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

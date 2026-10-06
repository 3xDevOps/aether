import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import type { AetherDesktop } from '@/components/shell/window-bar'
import type { Api } from '@/lib/api'
import type {
  AgentInfo,
  GatewayCapabilities,
  GitIdentity,
  RepoPushResult,
  RepoPushState,
} from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { FirstRunStep } from '@/routes/onboarding/first-run-step'
import { useStore, type RootState } from '@/store'
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
const checkoutOrigin = 'https://github.com/acme/app.git'

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
    repo: '/src/repo',
    remote: 'aether',
    url: `ssh://alice@host:2222/${workspace.id}`,
  },
  push: null,
  fastForward: null,
}
const settledRepo = {
  ...connectedRepo,
  remote: { ...connectedRepo.remote, origin: checkoutOrigin },
  push: compared('pushed', { workspace_commit: localTip }),
}

// The Electron preload injects the bridge onto the real window, so a test
// installs and removes it the same way.
const shellWindow = window as Window & { aetherDesktop?: AetherDesktop }

afterEach(() => {
  delete shellWindow.aetherDesktop
})

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    capabilities: localCaps,
    linkStatus: { server_configured: true, linked: true, addr: 'host:2222', user: 'alice', repo: '/src/repo' },
    hydrated: true,
    hydrationError: null,
    route: { name: 'onboarding', params: {} },
    onboarded: false,
    onboardingStep: 'Connect',
    onboardingFurthest: 'Connect',
    onboardingWorkspace: '',
    onboardingSource: 'local',
    onboardingRepo: null,
    onboardingFirstRun: { harness: '', task: '' },
    ...extra,
  })
}

/** Waits for the Connect step's identity form to take this machine's name. */
async function machineIdentity() {
  await waitFor(() => {
    expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe('Alice Local')
  })
}

/** Walks the wizard from mount past Connect to the Repository step. */
async function toWorkspaceStep() {
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

/** Walks on to the local clone form by picking the fixture workspace. */
async function toRepoStep() {
  await toWorkspaceStep()
  fireEvent.click(
    await screen.findByRole('button', { name: `Use ${workspace.name}` }),
  )
  const local = screen.queryByRole('button', { name: 'Link local repository' })
  if (local) fireEvent.click(local)
}

/** Walks on to the Agent step, through a repo link. */
async function toAgentsStep() {
  await toRepoStep()
  fireEvent.change(await screen.findByLabelText('Repository path'), {
    target: { value: '/home/alice/code/myproject' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

/** Walks all the way to the first-run step past the Agent step. */
async function toFirstRunStep() {
  await toAgentsStep()
  await screen.findByRole('region', { name: 'Agent' })
  fireEvent.click(await screen.findByRole('button', { name: /^(Continue|Skip for now)$/ }))
}

function currentStep() {
  return screen.getByRole('listitem', { current: 'step' }).textContent
}

describe('onboarding wizard', () => {
  it('shows four steps, one header, and no step counter', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    const steps = within(screen.getByRole('list', { name: 'Steps' })).getAllByRole('listitem')
    expect(steps.map((step) => step.textContent)).toEqual(['1Connect', '2Repository', '3Agent', '4First run'])
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(screen.queryByText(/Step \d of \d/)).toBeNull()
    expect(await screen.findByRole('region', { name: 'Connect' })).toBeDefined()
  })

  it('starts a hosted member at Repository with the git identity on top, without probing the local machine', async () => {
    const client = fakeApi()
    seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] } })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(currentStep()).toContain('Repository')
    expect(screen.queryByText('Connect')).toBeNull()
    const repository = await screen.findByRole('region', { name: 'Repository' })
    const identity = within(repository).getByRole('form', { name: 'Git identity' })
    expect(identity.compareDocumentPosition(within(repository).getByRole('button', { name: 'Import repository' })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Hosted member' } })
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'member@example.test' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save identity' }))
    expect(await screen.findByText('Saved')).toBeDefined()
    fireEvent.click(await screen.findByRole('button', { name: 'Import repository' }))
    expect(await screen.findByRole('dialog')).toBeDefined()
    expect(client.localLinkStatus).not.toHaveBeenCalled()
    expect(client.localGitIdentity).not.toHaveBeenCalled()
    expect(useStore.getState().info?.member.git_email).toBe('member@example.test')
  })

  it('checks link status on mount and steps to the repository choice', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)

    expect(await screen.findByText('host:2222')).toBeDefined()
    expect(screen.getByText(/as alice/)).toBeDefined()
    expect(client.localLinkStatus).toHaveBeenCalledTimes(1)
    expect(useStore.getState().linkStatus?.linked).toBe(true)

    await toWorkspaceStep()
    const repository = await screen.findByRole('region', { name: 'Repository' })
    expect(repository.textContent).toContain('A workspace is one repository and base branch, and the runs started from it.')
    expect(client.workspaceListFull).toHaveBeenCalled()
  })

  it('links an unlinked gateway in the app and steps to Repository', async () => {
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

    expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: /^Link by address/ }))
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
    expect(summary.textContent).toBe('Linked to host:2222 as Alice (admin). Created SSH key /home/alice/.ssh/id_ed25519.')
    expect(useStore.getState().connectionEpoch).toBe(1)
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(currentStep()).toContain('Repository')
  })

  it('shows a configured server without a repository and opens the repository choice', async () => {
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
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))

    expect(await screen.findByRole('region', { name: 'Repository' })).toBeDefined()
    expect(screen.queryByLabelText('Repository path')).toBeNull()
  })

  it('opens Repository on the workspace choice when the persisted step has no workspace', async () => {
    seed({ onboardingStep: 'Repository', onboardingWorkspace: '' })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    expect(currentStep()).toContain('Repository')
    expect(await screen.findByRole('button', { name: `Use ${workspace.name}` })).toBeDefined()
  })

  it('lets a member reach Agent without a workspace, but not launch', async () => {
    seed({ onboardingStep: 'Agent', onboardingFurthest: 'Agent', onboardingWorkspace: '' })
    const view = render(<OnboardingRoute params={{}} client={fakeApi()} />)
    expect(await view.findByRole('region', { name: 'Agent' })).toBeDefined()
    view.unmount()

    seed({ onboardingStep: 'First run', onboardingFurthest: 'First run', onboardingWorkspace: '' })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    const firstRun = await screen.findByRole('region', { name: 'First run' })
    expect(firstRun.textContent).toContain('choose a workspace first')
    fireEvent.click(within(firstRun).getByRole('button', { name: 'Choose a repository' }))
    expect(currentStep()).toContain('Repository')
  })

  it('gives a member who cannot add a workspace an Ask an admin state and a way on to Agent', async () => {
    seed({ info: { ...serverInfo, member: { ...alice, role: 'collaborator' } }, members: { [alice.id]: { ...alice, role: 'collaborator' } } })
    render(<OnboardingRoute params={{}} client={fakeApi({ workspaceListFull: vi.fn(async () => []) })} />)
    await toWorkspaceStep()

    expect(await screen.findByText('Ask an admin to add a workspace')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Create from local clone' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Continue to Agent' }))
    expect(currentStep()).toContain('Agent')
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
    expect(await screen.findByRole('button', { name: /^Link by address/ })).toBeDefined()

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

  it('prefills the git identity at the bottom of Connect from this machine and saves it', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)

    const connect = await screen.findByRole('region', { name: 'Connect' })
    expect(await within(connect).findByRole('region', { name: 'Git identity' })).toBeDefined()
    await machineIdentity()
    expect(screen.getByLabelText<HTMLInputElement>('Email').value).toBe('alice@example.invalid')

    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save identity' }))

    await waitFor(() => {
      expect(client.memberGit).toHaveBeenCalledWith('Ada Lovelace', 'alice@example.invalid')
    })
    expect(await screen.findByText('Saved')).toBeDefined()
    expect(currentStep()).toContain('Connect')
  })

  it('keeps the identity the member already saved over the machine one', async () => {
    const client = fakeApi()
    seed({
      info: { ...serverInfo, member: { ...alice, git_name: 'Ada Server' } },
    })
    render(<OnboardingRoute params={{}} client={client} />)

    // The machine's address landing in the empty email is what says the
    // probe finished; the saved name is asserted after that, not before.
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLInputElement>('Email').value).toBe(
        'alice@example.invalid',
      )
    })
    expect(screen.getByLabelText<HTMLInputElement>('Name').value).toBe('Ada Server')
  })

  it('shows the refusal verbatim', async () => {
    const client = fakeApi({
      memberGit: vi.fn(async () => {
        throw new Error('git email must contain @')
      }),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await machineIdentity()

    fireEvent.click(screen.getByRole('button', { name: 'Save identity' }))

    expect(await screen.findByText('git email must contain @')).toBeDefined()
    expect(screen.queryByText('Saved')).toBeNull()
  })

  it('moves on without saving an identity', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toWorkspaceStep()

    expect(await screen.findByRole('region', { name: 'Repository' })).toBeDefined()
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
    await machineIdentity()

    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save identity' }))
    await waitFor(() => expect(client.memberGit).toHaveBeenCalled())

    // What a disk-usage refresh does: write the whole info back with its
    // own field changed, while the save is still on the wire.
    act(() =>
      useStore
        .getState()
        .setInfo({ ...serverInfo, tailnet_hostname: 'gateway.tailnet.ts.net' }),
    )
    land()

    await screen.findByText('Saved')
    expect(useStore.getState().info?.member.git_name).toBe('Ada Lovelace')
    expect(useStore.getState().info?.tailnet_hostname).toBe(
      'gateway.tailnet.ts.net',
    )
  })

  it('shows the saved identity when the user walks back into Connect', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await machineIdentity()

    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save identity' }))
    await screen.findByText('Saved')
    await toWorkspaceStep()
    await screen.findByRole('region', { name: 'Repository' })

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    // What the server answered, not the machine's git config: the save
    // refreshed the stored member, so the re-run probe has nothing to fill.
    await screen.findByRole('region', { name: 'Connect' })
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

    expect(await screen.findByText('git config: no user.name set')).toBeDefined()
    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.change(screen.getByLabelText('Email'), {
      target: { value: 'ada@example.invalid' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save identity' }))

    await waitFor(() => {
      expect(client.memberGit).toHaveBeenCalledWith(
        'Ada Lovelace',
        'ada@example.invalid',
      )
    })
  })

  it('asks a gateway without the git.identity verb for nothing', async () => {
    const client = fakeApi()
    seed({ capabilities: olderCaps })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(await screen.findByRole('region', { name: 'Git identity' })).toBeDefined()
    expect(client.localGitIdentity).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('Name'), {
      target: { value: 'Ada Lovelace' },
    })
    fireEvent.change(screen.getByLabelText('Email'), {
      target: { value: 'ada@example.invalid' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save identity' }))

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

    const save = await screen.findByRole('button', { name: 'Save identity' })
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

  it('creates another local workspace and continues against its branch', async () => {
    const created = { ...otherWorkspace, base_branch: 'trunk' }
    const client = fakeApi({ workspaceAdd: vi.fn(async () => created) })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toWorkspaceStep()
    fireEvent.click(await screen.findByRole('button', { name: 'Create from local clone' }))
    fireEvent.change(screen.getByLabelText('Workspace name'), { target: { value: created.name } })
    fireEvent.change(screen.getByLabelText('Base branch'), { target: { value: 'trunk' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create workspace' }))
    fireEvent.change(await screen.findByLabelText('Repository path'), { target: { value: '/home/alice/code/myproject' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    expect((await screen.findByLabelText<HTMLInputElement>('Push command')).value).toBe('git push -u aether trunk')
    fireEvent.click(await screen.findByRole('button', { name: 'Push now' }))
    await waitFor(() => expect(client.localRepoPush).toHaveBeenCalledWith(created.id))
    expect(client.localLinkRepo).toHaveBeenCalledWith('/home/alice/code/myproject', created.id)
    expect(useStore.getState().activeWorkspace).toBe(created.id)
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
    // This clone had no pushable origin to record, so the step claims none.
    expect(screen.queryByText(/Runs push to/)).toBeNull()
  })

  it('offers the folders the gateway already knows as suggestions', async () => {
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => ({
        server_configured: true,
        linked: true,
        addr: 'host:2222',
        user: 'alice',
        repo: '/src/repo',
        // The default link's folder repeats here; a suggestion list must not
        // show it twice, and a profile without a folder adds nothing.
        links: [
          { name: 'prod', addr: 'host:2222', repo: '/src/repo' },
          { name: 'staging', addr: 'staging:2222', repo: '/src/other' },
          { name: 'lab', addr: 'lab:2222' },
        ],
      })),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    // Through the input's own list linkage: a suggestion list the field
    // does not point at is one the user never sees.
    const path =
      await screen.findByLabelText<HTMLInputElement>('Repository path')
    const list = document.getElementById(path.getAttribute('list') ?? '')
    expect(list).not.toBeNull()
    const options = within(list as HTMLElement).getAllByRole('option', {
      hidden: true,
    })
    expect(options.map((o) => (o as HTMLOptionElement).value)).toEqual([
      '/src/repo',
      '/src/other',
    ])
  })

  it('fills the path from the desktop shell folder dialog', async () => {
    const chooseFolder = vi.fn(async () => '/home/alice/code/myproject')
    shellWindow.aetherDesktop = { platform: 'linux', chooseFolder }
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    fireEvent.click(
      await screen.findByRole('button', { name: 'Choose folder' }),
    )

    const path =
      await screen.findByLabelText<HTMLInputElement>('Repository path')
    await waitFor(() => expect(path.value).toBe('/home/alice/code/myproject'))

    // Cancelling answers with an empty string, which must leave the path put.
    chooseFolder.mockResolvedValueOnce('')
    fireEvent.click(screen.getByRole('button', { name: 'Choose folder' }))
    await waitFor(() => expect(chooseFolder).toHaveBeenCalledTimes(2))
    expect(path.value).toBe('/home/alice/code/myproject')

    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await waitFor(() => {
      expect(client.localLinkRepo).toHaveBeenCalledWith(
        '/home/alice/code/myproject',
        workspace.id,
      )
    })
  })

  it('shows the dialog error when the shell could not open a folder', async () => {
    shellWindow.aetherDesktop = {
      platform: 'linux',
      chooseFolder: vi.fn(async () => {
        throw new Error('no dialog available')
      }),
    }
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toRepoStep()

    fireEvent.click(
      await screen.findByRole('button', { name: 'Choose folder' }),
    )

    expect(await screen.findByText('no dialog available')).toBeDefined()
  })

  it('keeps the error that sent the user to the dialog when they cancel', async () => {
    const chooseFolder = vi.fn(async () => '')
    shellWindow.aetherDesktop = { platform: 'linux', chooseFolder }
    seed()
    render(
      <OnboardingRoute
        params={{}}
        client={fakeApi({
          localLinkRepo: vi.fn(async () => {
            throw new Error('/home/alice/typo is not a git repository')
          }),
        })}
      />,
    )
    await toRepoStep()

    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/typo' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    const failure = await screen.findByText(
      '/home/alice/typo is not a git repository',
    )

    // Opening the dialog to correct the path and backing out must not clear
    // the only thing on screen saying why Add remote refused.
    fireEvent.click(screen.getByRole('button', { name: 'Choose folder' }))
    await waitFor(() => expect(chooseFolder).toHaveBeenCalled())
    expect(failure.isConnected).toBe(true)
  })

  it('takes the Windows path the shell dialog answers with', async () => {
    shellWindow.aetherDesktop = {
      platform: 'win32',
      chooseFolder: vi.fn(async () => 'C:\\Users\\alice\\code\\myproject'),
    }
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    fireEvent.click(
      await screen.findByRole('button', { name: 'Choose folder' }),
    )

    // A drive-letter path is absolute, so Add remote must not sit disabled
    // telling the user the folder they just picked is not absolute.
    const add = await screen.findByRole('button', { name: 'Add remote' })
    await waitFor(() => expect(add.hasAttribute('disabled')).toBe(false))
    expect(screen.queryByText('The path must be absolute.')).toBeNull()

    fireEvent.click(add)
    await waitFor(() => {
      expect(client.localLinkRepo).toHaveBeenCalledWith(
        'C:\\Users\\alice\\code\\myproject',
        workspace.id,
      )
    })
  })

  it('freezes the path form while a folder dialog is open', async () => {
    let answer!: (path: string) => void
    const chooseFolder = vi.fn(
      () =>
        new Promise<string>((resolve) => {
          answer = resolve
        }),
    )
    shellWindow.aetherDesktop = { platform: 'linux', chooseFolder }
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toRepoStep()

    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/first' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Choose folder' }))

    // Where the chooser is not modal to the window the rest of the form is
    // still clickable, and a dialog answering late would land on top of
    // whatever was done in the meantime.
    const path = screen.getByLabelText<HTMLInputElement>('Repository path')
    await waitFor(() => expect(path.disabled).toBe(true))
    expect(
      screen.getByRole('button', { name: 'Add remote' }).hasAttribute('disabled'),
    ).toBe(true)

    answer('/home/alice/code/picked')

    await waitFor(() => expect(path.value).toBe('/home/alice/code/picked'))
    expect(path.disabled).toBe(false)
    expect(
      screen.getByRole('button', { name: 'Add remote' }).hasAttribute('disabled'),
    ).toBe(false)
  })

  it('has no folder picker in a browser tab', async () => {
    // Last of the bridge tests on purpose: it is also the guard that the
    // cleanup above really removes the bridge the two before it installed.
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    expect(screen.queryByRole('button', { name: 'Choose folder' })).toBeNull()
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
    const client = fakeApi({
      localLinkRepo: vi.fn(async () => ({
        repo: '/src/repo',
        remote: 'aether',
        url: 'ssh://alice@host:2222/wsp_1',
        origin: checkoutOrigin,
      })),
    })
    await toPushChoice(client)

    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))

    await waitFor(() => {
      expect(client.localRepoPush).toHaveBeenCalledWith(workspace.id)
    })
    // The confirmation names the branch that landed, and git's own words
    // stay on the page - "[new branch]" and "Everything up-to-date" are
    // both success and mean different things.
    expect(await screen.findByText(/Pushed/)).toBeDefined()
    const mirrorStatus = await screen.findByRole('status', {
      name: 'Source mirror status',
    })
    expect(mirrorStatus.textContent).toContain('Local-only workspace.')
    fireEvent.click(
      screen.getByRole('button', { name: 'Set up source mirror' }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toBeDefined()
    expect(
      within(dialog).getByLabelText<HTMLInputElement>('Source URL').value,
    ).toBe(checkoutOrigin)
    fireEvent.click(
      within(dialog).getAllByRole('button', { name: 'Close' })[0],
    )
    const output = screen.getByText(/\[new branch\]/)
    expect(output.textContent).toBe(
      'To ssh://alice@host:2222/wsp_1\n * [new branch] main -> main',
    )
    // Open, not merely present: the reader who needs to tell "[new branch]"
    // from "Everything up-to-date" would not know to go looking.
    expect(
      screen.getByRole('button', { name: 'What git did' }).getAttribute('aria-expanded'),
    ).toBe('true')
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
      localLinkRepo: vi.fn(async () => ({
        repo: '/src/repo',
        remote: 'aether',
        url: 'ssh://alice@host:2222/wsp_1',
        origin: checkoutOrigin,
      })),
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
    expect(
      await screen.findByRole('status', { name: 'Source mirror status' }),
    ).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()
    // Git's own answer, and the panel holding it open so the reader meets it.
    expect(screen.getByText(/FETCH_HEAD/)).toBeDefined()
    expect(
      screen.getByRole('button', { name: 'What git did' }).getAttribute('aria-expanded'),
    ).toBe('true')
  })

  it('lets a collaborator seed a confirmed local-only workspace without mirror management', async () => {
    const client = fakeApi()
    seed({
      onboardingStep: 'Repository',
      onboardingWorkspace: workspace.id,
      onboardingRepo: connectedRepo,
      info: { ...serverInfo, member: { ...alice, role: 'collaborator' } },
    })
    render(<OnboardingRoute params={{}} client={client} />)

    await screen.findByText('Local-only workspace.')
    expect(client.workspaceMirrorStatus).toHaveBeenCalledWith(workspace.id)
    expect(screen.queryByRole('button', { name: /source mirror/ })).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByLabelText<HTMLInputElement>('Push command').value).toBe('git push -u aether main')
    fireEvent.click(await screen.findByRole('button', { name: 'Push now' }))
    await waitFor(() => expect(client.localRepoPush).toHaveBeenCalledWith(workspace.id))
    expect(await screen.findByText(/Pushed/)).toBeDefined()
    expect(client.workspaceMirrorConfigure).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByRole('region', { name: 'Agent' })).toBeDefined()
  })

  it('lets a collaborator link and continue on a mirrored workspace without offering a base push', async () => {
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => ({ enabled: true, status: 'pending' as const, source_url: checkoutOrigin, branch: 'main' })),
    })
    seed({
      onboardingStep: 'Repository',
      onboardingWorkspace: workspace.id,
      info: { ...serverInfo, member: { ...alice, role: 'collaborator' } },
    })
    render(<OnboardingRoute params={{}} client={client} />)

    await screen.findByText('Source mirror configured.')
    fireEvent.change(await screen.findByLabelText('Repository path'), { target: { value: '/src/repo' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await screen.findByRole('button', { name: 'Use a different repository' })
    expect(client.localLinkRepo).toHaveBeenCalledWith('/src/repo', workspace.id)
    expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()
    expect(screen.queryByLabelText('Push command')).toBeNull()
    expect(screen.queryByRole('button', { name: /source mirror/ })).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(client.localRepoPush).not.toHaveBeenCalled()
    expect(client.workspaceMirrorConfigure).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByRole('region', { name: 'Agent' })).toBeDefined()
  })

  it('keeps source mirror setup behind its capability', async () => {
    const client = fakeApi()
    seed({
      capabilities: { ...localCaps, methods: ['server.info'] },
      onboardingStep: 'Repository',
      onboardingWorkspace: workspace.id,
      onboardingRepo: settledRepo,
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(
      screen.queryByRole('status', { name: 'Source mirror status' }),
    ).toBeNull()
    expect(client.workspaceMirrorStatus).not.toHaveBeenCalled()
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
    expect(
      await screen.findByRole('status', { name: 'Source mirror status' }),
    ).toBeDefined()
    expect(
      await screen.findByRole('button', { name: 'Set up source mirror' }),
    ).toBeDefined()

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
    expect(await screen.findByRole('region', { name: 'Agent' })).toBeDefined()
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

    await screen.findByText(/both moved on/)
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
    expect(await screen.findByRole('region', { name: 'Agent' })).toBeDefined()
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

  it('keeps a newer clone connection when a closed link form completes late', async () => {
    const pending = Promise.withResolvers<typeof connectedRepo.remote>()
    const currentStatus = { server_configured: true, linked: true, addr: 'host:2222', user: 'alice', repo: '/clone-b' }
    const client = fakeApi({
      localLinkRepo: vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue({
        repo: '/clone-b', remote: 'aether', url: `ssh://alice@host:2222/${otherWorkspace.id}`,
      }),
      localLinkStatus: vi.fn(async () => currentStatus),
    })
    seed({ onboardingStep: 'Repository', onboardingWorkspace: workspace.id })
    const first = render(<OnboardingRoute params={{}} client={client} />)
    fireEvent.change(await screen.findByLabelText('Repository path'), { target: { value: '/clone-a' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await waitFor(() => expect(client.localLinkRepo).toHaveBeenCalledTimes(1))
    first.unmount()
    seed({ workspaces: { [otherWorkspace.id]: otherWorkspace }, onboardingStep: 'Repository', onboardingWorkspace: otherWorkspace.id })
    const second = render(<OnboardingRoute params={{}} client={client} />)
    fireEvent.change(await screen.findByLabelText('Repository path'), { target: { value: '/clone-b' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await screen.findByText('/clone-b')
    const current = useStore.getState().onboardingRepo
    await act(async () => pending.resolve({ repo: '/clone-a', remote: 'aether', url: `ssh://alice@host:2222/${workspace.id}` }))
    expect(useStore.getState().onboardingRepo).toEqual(current)
    expect(useStore.getState().linkStatus).toEqual(currentStatus)
    expect(screen.getByText('/clone-b')).toBeDefined()
    second.unmount()
  })

  it.each(['identity', 'connection'] as const)('does not merge a late linked clone into a new %s', async (transition) => {
    const pending = Promise.withResolvers<typeof connectedRepo.remote>()
    const client = fakeApi({ localLinkRepo: vi.fn(() => pending.promise) })
    seed({ identityKey: 'server:alice', connectionEpoch: 0, onboardingStep: 'Repository', onboardingWorkspace: workspace.id })
    render(<OnboardingRoute params={{}} client={client} />)
    fireEvent.change(await screen.findByLabelText('Repository path'), { target: { value: '/clone-a' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await waitFor(() => expect(client.localLinkRepo).toHaveBeenCalledTimes(1))
    const statusReads = vi.mocked(client.localLinkStatus).mock.calls.length
    const currentStatus = { server_configured: true, linked: true, addr: 'another:2222', user: 'bob', repo: '/other-owner' }
    act(() => useStore.setState({
      ...(transition === 'identity' ? { identityKey: 'server:bob' } : { connectionEpoch: 1 }),
      linkStatus: currentStatus,
    }))
    await act(async () => pending.resolve({ repo: '/clone-a', remote: 'aether', url: `ssh://alice@host:2222/${workspace.id}` }))
    expect(useStore.getState().onboardingRepo).toBeNull()
    expect(useStore.getState().linkStatus).toEqual(currentStatus)
    expect(client.localLinkStatus).toHaveBeenCalledTimes(statusReads)
    expect(screen.queryByText('/clone-a')).toBeNull()
  })

  it('refuses to push a remembered connection after the gateway switches clones', async () => {
    const client = fakeApi()
    await toPushChoice(client)
    vi.mocked(client.localLinkStatus).mockResolvedValueOnce({
      server_configured: true, linked: true, addr: 'host:2222', user: 'alice', repo: '/home/alice/code/another',
    })
    fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
    await screen.findByText(/The gateway now uses \/home\/alice\/code\/another/)
    expect(client.localRepoPush).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Repository path')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()
  })

  it.each(['admin', 'collaborator'] as const)('keeps %s first-run launch disabled until an unaccepted source is repaired', async (role) => {
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn()
        .mockResolvedValueOnce({ enabled: true, status: 'pending', last_error: 'source key is not installed' })
        .mockResolvedValue({ enabled: true, status: 'ready', accepted_commit: workspaceTip }),
    })
    const review = vi.fn()
    seed({ info: { ...serverInfo, member: { ...alice, role } }, onboardingFirstRun: { harness: 'claude', task: 'Inspect the repository' } })
    render(<FirstRunStep client={client} workspace={workspace} onBackToAgent={vi.fn()} onBackToRepository={review} />)
    await screen.findByText('source key is not installed')
    expect(screen.getByRole('button', { name: 'Launch' })).toHaveProperty('disabled', true)
    expect(client.workspaceMirrorStatus).toHaveBeenCalledWith(workspace.id)
    expect(client.runLaunch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    expect(review).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Launch' })).toHaveProperty('disabled', false))
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))
    await waitFor(() => expect(useStore.getState().route.params.runId).toBe('run_1'))
  })

  it('keeps collaborator launch disabled while source status is pending or fails, then allows a confirmed local-only source', async () => {
    const pending = Promise.withResolvers<{ enabled: boolean }>()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue({ enabled: false }),
    })
    seed({
      info: { ...serverInfo, member: { ...alice, role: 'collaborator' } },
      onboardingFirstRun: { harness: 'claude', task: 'Inspect the repository' },
    })
    render(<FirstRunStep client={client} workspace={workspace} onBackToAgent={vi.fn()} onBackToRepository={vi.fn()} />)

    await waitFor(() => expect(client.workspaceMirrorStatus).toHaveBeenCalledWith(workspace.id))
    expect(screen.getByRole('button', { name: 'Launch' })).toHaveProperty('disabled', true)
    await act(async () => pending.reject(new Error('source status unavailable')))
    await screen.findByText('source status unavailable')
    expect(screen.getByRole('button', { name: 'Launch' })).toHaveProperty('disabled', true)
    expect(client.runLaunch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Launch' })).toHaveProperty('disabled', false))
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))
    await waitFor(() => expect(client.runLaunch).toHaveBeenCalledWith({
      workspace_id: workspace.id, task: 'Inspect the repository', harness: 'claude',
    }))
  })

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
    await waitFor(() => expect(client.localRepoPush).toHaveBeenCalledTimes(1))
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
    await waitFor(() => expect(client.localRepoPush).toHaveBeenCalledTimes(1))
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
    await waitFor(() => expect(client.localRepoPush).toHaveBeenCalledTimes(1))
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

  it('resumes on Repository with its clone connected and goes back to the workspace choice', async () => {
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

    fireEvent.click(screen.getByRole('button', { name: 'Choose another workspace' }))
    fireEvent.click(
      await screen.findByRole('button', { name: `Use ${workspace.name}` }),
    )
    expect(
      await screen.findByRole('region', { name: 'Repository' }),
    ).toBeDefined()
    expect(screen.getByText('/home/alice/code/myproject')).toBeDefined()
  })

  it('keeps the connected clone while Connect is walked through', async () => {
    const client = fakeApi()
    seed({ onboardingWorkspace: workspace.id, onboardingRepo: connectedRepo })
    render(<OnboardingRoute params={{}} client={client} />)

    await toWorkspaceStep()

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

    fireEvent.click(screen.getByRole('button', { name: 'Choose another workspace' }))
    fireEvent.click(
      await screen.findByRole('button', { name: `Use ${otherWorkspace.name}` }),
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Link local repository' }))

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

  it('launches the first run with the launch form in the chosen workspace and navigates to it', async () => {
    const client = fakeApi()
    seed({ runs: {} })
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    const step = await screen.findByRole('region', { name: 'First run' })
    expect(step.textContent).toContain('A run is one agent working on its own branch in its own container')
    // The launch dialog's own fields, not a copy.
    expect(within(step).getByRole('radiogroup', { name: 'Agent' })).toBeDefined()
    fireEvent.click(within(step).getByRole('radio', { name: /^Claude Code/ }))
    expect(within(step).getByRole('radiogroup', { name: 'Mode' })).toBeDefined()
    expect(step.textContent).toContain('about 5 s in Enhanced')
    fireEvent.change(screen.getByLabelText('Task'), {
      target: { value: 'write a result file' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() => {
      expect(client.runLaunch).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        task: 'write a result file',
        harness: 'claude',
      })
    })
    // The wizard hands off to the run view rather than holding a done
    // screen, with the run already in the store so the terminal tab does not
    // call it deleted.
    await waitFor(() => {
      expect(useStore.getState().route).toEqual({
        name: 'terminal',
        params: { runId: 'run_1' },
      })
      expect(useStore.getState().runs.run_1).toBeDefined()
    })
    expect(useStore.getState().launchDefaults.claude?.mode).toBe('tui')
  })

  it('launches Enhanced when the setup chose it', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ display_name: 'Claude Code', enhanced: 'adapter', enhanced_installed: true })]),
    })
    seed({ runs: {}, launchDefaults: { claude: { mode: 'acp', at: 1 } } })
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    await screen.findByRole('radiogroup', { name: 'Mode' })
    expect(screen.getByRole('radio', { name: 'Enhanced' }).getAttribute('aria-checked')).toBe('true')
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))
    await waitFor(() => {
      expect(client.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp' })
    })
  })

  it('finishes onboarding by going to the board', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toFirstRunStep()

    fireEvent.click(await screen.findByRole('button', { name: 'Go to board' }))

    expect(useStore.getState()).toMatchObject({
      onboarded: true,
      onboardingStep: 'Connect',
      onboardingWorkspace: '',
      route: { name: 'board', params: {} },
    })
  })

  it('offers only the agents installed in this account', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [
        agentInfo({ display_name: 'Claude Code' }),
        agentInfo({ name: 'codex', display_name: 'Codex', installed: false }),
      ]),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    await screen.findByRole('radiogroup', { name: 'Agent' })
    expect(screen.getByRole('radio', { name: /^Claude Code/ })).toHaveProperty('disabled', false)
    expect(screen.getByRole('radio', { name: /^Codex/ })).toHaveProperty('disabled', true)
  })

  it('sends the reader back to Agent when nothing is installed', async () => {
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ installed: false })]),
    })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toFirstRunStep()

    expect(await screen.findByText('No agent is installed yet')).toBeDefined()
    expect(screen.queryByRole('radiogroup', { name: 'Agent' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Launch' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Set up an agent' }))

    expect(await screen.findByRole('region', { name: 'Agent' })).toBeDefined()
    expect(currentStep()).toContain('Agent')
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
      await within(await screen.findByRole('region', { name: 'First run' })).findByText('agent.list: environment home unreadable'),
    ).toBeDefined()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeDefined()
    expect(screen.queryByText('No agent is installed yet')).toBeNull()
  })

  it('jumps between the steps it has reached from the header', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toFirstRunStep()

    const steps = screen.getByLabelText('Steps')
    fireEvent.click(within(steps).getByRole('button', { name: /Repository, visited/ }))

    expect(await screen.findByRole('region', { name: 'Repository' })).toBeDefined()
    expect(useStore.getState().onboardingStep).toBe('Repository')
    // Everything already reached stays reachable, or a jump backwards would
    // strand the member on a step whose own Back is gone.
    expect(useStore.getState().onboardingFurthest).toBe('First run')
    fireEvent.click(within(steps).getByRole('button', { name: /First run/ }))
    expect(await screen.findByRole('region', { name: 'First run' })).toBeDefined()
  })

  it('leaves a step it has never reached inert in the header', async () => {
    seed({ onboardingStep: 'Connect', onboardingFurthest: 'Repository' })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    const steps = screen.getByLabelText('Steps')
    expect(within(steps).queryByRole('button', { name: /First run/ })).toBeNull()
    expect(within(steps).getByRole('button', { name: /Repository, visited/ })).toBeDefined()
    fireEvent.click(within(steps).getByText('First run'))
    expect(useStore.getState().onboardingStep).toBe('Connect')
    expect(await screen.findByRole('region', { name: 'Connect' })).toBeDefined()
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
      expect(screen.getByRole('radio', { name: /^codex/ }).getAttribute('aria-checked')).toBe('true')
    })
    expect(useStore.getState().onboardingFirstRun.harness).toBe('codex')
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

  it('keeps the agent the member picked over the one the Agent step set up', async () => {
    seed({
      onboardingFirstRun: { harness: 'myagent', task: 'write a file' },
      launchDefaults: { claude: { mode: 'tui', at: 2 } },
    })
    render(
      <FirstRunStep
        client={fakeApi()}
        workspace={workspace}
        onBackToAgent={() => {}}
        onBackToRepository={() => {}}
      />,
    )

    await waitFor(() => {
      expect(screen.getByRole('radio', { name: /^myagent/ }).getAttribute('aria-checked')).toBe('true')
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
        name: /Repository, .*go to this step/,
      }),
    )
    await screen.findByRole('region', { name: 'Repository' })
    fireEvent.click(
      within(steps).getByRole('button', {
        name: /First run, .*go to this step/,
      }),
    )

    expect(
      (await screen.findByLabelText<HTMLTextAreaElement>('Task')).value,
    ).toBe('add a health check endpoint')
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

    fireEvent.click(await screen.findByRole('radio', { name: /^Claude Code/ }))
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

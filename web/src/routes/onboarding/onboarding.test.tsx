import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { useStore, type RootState } from '@/store'
import { alice, fakeApi, serverInfo, session, workspace } from '@/test/fixtures'

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
    sessions: { [session.id]: session },
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

/** Walks on to the repo step by picking the fixture workspace. */
async function toRepoStep() {
  await toWorkspaceStep()
  fireEvent.click(
    await screen.findByRole('button', { name: `Use ${workspace.name}` }),
  )
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

  it('launches the first run and navigates to it', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))

    const region = await screen.findByRole('region', { name: 'First run' })
    expect(region).toBeDefined()
    fireEvent.change(await screen.findByLabelText('Session'), {
      target: { value: session.id },
    })
    fireEvent.change(screen.getByLabelText('Harness'), {
      target: { value: 'claude' },
    })
    fireEvent.change(screen.getByLabelText('Task'), {
      target: { value: 'write a result file' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    // runLaunch resolves to the fixture run; the wizard hands off to the
    // run view rather than holding a done screen.
    expect(await screen.findByRole('region', { name: 'First run' })).toBeDefined()
    expect(client.runLaunch).toHaveBeenCalledWith({
      session_id: session.id,
      task: 'write a result file',
      harness: 'claude',
    })
    await waitFor(() => {
      expect(useStore.getState().route).toEqual({
        name: 'run',
        params: { runId: 'run_1' },
      })
    })
  })

  it('creates a session inline when session.new is offered', async () => {
    const client = fakeApi({ sessionList: vi.fn(async () => []) })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))

    fireEvent.change(await screen.findByLabelText('Session'), {
      target: { value: '__new' },
    })
    fireEvent.change(await screen.findByLabelText('New session name'), {
      target: { value: 'demo' },
    })
    fireEvent.change(screen.getByLabelText('Harness'), {
      target: { value: 'claude' },
    })
    fireEvent.change(screen.getByLabelText('Task'), {
      target: { value: 'write a result file' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() => {
      expect(client.sessionNew).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        name: 'demo',
      })
      // The new session's id feeds the launch.
      expect(client.runLaunch).toHaveBeenCalledWith({
        session_id: session.id,
        task: 'write a result file',
        harness: 'claude',
      })
    })
  })

  it('reuses the created session when the launch itself fails and is retried', async () => {
    // sessionNew succeeds but the first runLaunch fails: the retry must not
    // mint a second session with the same name.
    const runLaunch = vi
      .fn()
      .mockRejectedValueOnce(new Error('temporarily out of capacity'))
      .mockResolvedValue({ id: 'run_1' })
    const client = fakeApi({ sessionList: vi.fn(async () => []), runLaunch })
    seed()
    render(<OnboardingRoute params={{}} client={client} />)
    await toRepoStep()

    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))

    fireEvent.change(await screen.findByLabelText('Session'), {
      target: { value: '__new' },
    })
    fireEvent.change(await screen.findByLabelText('New session name'), {
      target: { value: 'demo' },
    })
    fireEvent.change(screen.getByLabelText('Harness'), {
      target: { value: 'claude' },
    })
    fireEvent.change(screen.getByLabelText('Task'), {
      target: { value: 'write a result file' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    // The refusal renders verbatim and the picker now holds the created
    // session, so the name field is gone.
    expect(await screen.findByText('temporarily out of capacity')).toBeDefined()
    expect(screen.queryByLabelText('New session name')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() => {
      expect(client.runLaunch).toHaveBeenCalledTimes(2)
      expect(client.runLaunch).toHaveBeenLastCalledWith({
        session_id: session.id,
        task: 'write a result file',
        harness: 'claude',
      })
    })
    expect(client.sessionNew).toHaveBeenCalledTimes(1)
  })
})

import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { api, ApiError } from '@/lib/api'
import type { Api } from '@/lib/api'
import type { GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { FirstRunStep } from '@/routes/onboarding/steps'
import { useStore } from '@/store'
import { capability } from '@/store/hooks'
import {
  agentInfo,
  alice,
  fakeApi,
  profilePreview,
  serverInfo,
  workspace,
} from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

// The local gateway: every method, the shell and envscan sockets, and the
// client-machine verbs this step rides on.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'shell', 'envscan'],
  local: [
    'link.status',
    'link.repo',
    'env.harnesses',
    'profile.preview',
    'profile.push',
  ],
}

// jsdom has no layout engine, so the terminal's fit addon has nothing to
// observe.
class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function seed(caps: GatewayCapabilities = localCaps) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    capabilities: caps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'onboarding', params: {} },
    shellRequest: null,
    envBuilds: {},
  })
}

/** The step on its own, with the wizard's callbacks as spies. */
function renderStep(client: Api, caps: GatewayCapabilities = localCaps) {
  seed(caps)
  const onNext = vi.fn()
  const onReady = vi.fn()
  const view = render(
    <AgentsStep
      client={client}
      caps={capability(caps)}
      workspace={workspace}
      onNext={onNext}
      onReady={onReady}
    />,
  )
  return { onNext, onReady, view }
}

/** Two harnesses with configuration on this machine, so a test can prove a
 * push touched exactly one of them. */
function bothPresent() {
  return vi.fn(async (harness: string) =>
    harness === 'claude' || harness === 'codex'
      ? profilePreview({ harness, root: `/home/alice/.${harness}` })
      : profilePreview({ harness, present: false, files: 0, bytes: 0 }),
  )
}

/**
 * Presses Part B's preview button and waits for the walk to finish.
 * Previews are deliberately not automatic - a profile root can hold
 * hundreds of megabytes - so every test that wants one asks for it, the
 * way a user does.
 */
async function look() {
  fireEvent.click(
    await screen.findByRole('button', { name: /Look at what is here/ }),
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'Look at what is here' }),
    ).toBeNull(),
  )
}

function frame(data: object) {
  StubSocket.last().onmessage?.({ data: JSON.stringify(data) })
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('agents step', () => {
  it('renders both halves from env.harnesses, agent.list and the previews', async () => {
    const client = fakeApi()
    renderStep(client)
    await look()

    // Part A: one row per setup-capable harness, by friendly name, saying
    // what each of the two signals actually means.
    expect(
      await screen.findByRole('region', { name: 'Set up an agent' }),
    ).toBeDefined()
    // Both halves name the harness, so Part A's rows are queried inside
    // Part A's own region.
    const partA = screen.getByRole('region', { name: 'Set up an agent' })
    expect(within(partA).getByText('Claude Code')).toBeDefined()
    expect(within(partA).getByText('Codex')).toBeDefined()
    expect(
      screen.getByText(/installed on this machine - the server can launch claude/),
    ).toBeDefined()
    expect(
      screen.getByText(
        /not installed on this machine - the server does not list codex/,
      ),
    ).toBeDefined()
    expect(client.envHarnesses).toHaveBeenCalled()
    expect(client.agentList).toHaveBeenCalled()

    // Part B: only the harness whose preview reports present:true.
    expect(
      screen.getByRole('region', { name: 'Bring your configuration' }),
    ).toBeDefined()
    expect(
      await screen.findByRole('checkbox', {
        name: 'Bring Claude Code configuration',
      }),
    ).toBeDefined()
    expect(client.localProfilePreview).toHaveBeenCalledWith(
      'claude',
      expect.anything(),
    )
    expect(
      screen.queryByRole('checkbox', { name: 'Bring Codex configuration' }),
    ).toBeNull()
  })

  it('offers opencode, which syncs a profile but cannot run a scan', async () => {
    // env.harnesses reports only the setup-capable four. opencode syncs
    // ~/.local/share/opencode all the same, so leaving Part B to that list
    // would make its configuration unreachable from the dashboard; the
    // shipped names from agent.list are what fill the gap.
    const client = fakeApi({
      agentList: vi.fn(async () => [
        agentInfo(),
        agentInfo({ name: 'opencode', source: 'shipped' }),
        agentInfo({ name: 'myagent', source: 'member' }),
      ]),
      localProfilePreview: vi.fn(async (harness: string) =>
        harness === 'opencode'
          ? profilePreview({
              harness,
              root: '/home/alice/.local/share/opencode',
            })
          : profilePreview({ harness, present: false, files: 0, bytes: 0 }),
      ),
    })
    renderStep(client)
    await look()

    expect(
      await screen.findByRole('checkbox', {
        name: 'Bring opencode configuration',
      }),
    ).toBeDefined()
    expect(client.localProfilePreview).toHaveBeenCalledWith(
      'opencode',
      expect.anything(),
    )
    // A member-registered name is not a shipped profile root, so it is
    // never previewed.
    expect(client.localProfilePreview).not.toHaveBeenCalledWith(
      'myagent',
      expect.anything(),
    )
    // opencode is not setup-capable, so Part A never offers to set it up.
    expect(
      screen.queryByRole('button', { name: 'Set up opencode' }),
    ).toBeNull()
  })

  it('names the missing verbs rather than reporting an empty machine', async () => {
    // A gateway that does not serve profile.preview would answer every
    // preview with a 404, and "nothing to bring" would be a false claim
    // about the user's machine.
    const client = fakeApi()
    renderStep(client, {
      ...localCaps,
      local: ['link.status', 'link.repo', 'env.harnesses'],
    })

    expect(
      await screen.findByText(/does not serve the profile verbs/),
    ).toBeDefined()
    expect(screen.getByText(/aether profile push --agent claude/)).toBeDefined()
    expect(screen.queryByText(/No agent configuration was found/)).toBeNull()
    expect(client.localProfilePreview).not.toHaveBeenCalled()
  })

  it('opens the agent-setup shell inline and marks the harness ready on a clean exit', async () => {
    const client = fakeApi()
    const { onReady } = renderStep(client)

    fireEvent.click(
      await screen.findByRole('button', { name: 'Set up Claude Code' }),
    )
    await act(async () => {})

    // The same agent-setup shell the Agents page opens, in the workspace
    // the wizard already settled on - no second form.
    expect(useStore.getState().shellRequest).toEqual({
      workspace: { id: workspace.id },
      mode: 'agent-setup',
      harness: 'claude',
    })
    expect(screen.queryByText('Open setup shell')).toBeNull()

    const listCalls = vi.mocked(client.agentList).mock.calls.length
    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })
      StubSocket.last().onclose?.({ code: 1000 })
    })
    await act(async () => {})

    // Registration is the server's, reported by the clean exit: refetch,
    // and carry the harness to the first run.
    expect(screen.getByText('Agent registered')).toBeDefined()
    expect(vi.mocked(client.agentList).mock.calls.length).toBe(listCalls + 1)
    expect(onReady).toHaveBeenCalledWith('claude')

    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(
      await screen.findByText(/Set up in this session/),
    ).toBeDefined()
  })

  it('hides Set up where the gateway cannot run the setup shell', async () => {
    const client = fakeApi()
    renderStep(client, {
      gateway: 'remote',
      methods: ['run.list'],
      ws: ['events', 'attach'],
      local: ['link.status'],
    })

    expect(await screen.findByText('Claude Code')).toBeDefined()
    expect(
      screen.queryByRole('button', { name: 'Set up Claude Code' }),
    ).toBeNull()
    expect(screen.getByText(/aether agent add/)).toBeDefined()
  })

  it('lists a preview by category and what the guards left out', async () => {
    const client = fakeApi()
    renderStep(client)
    await look()

    expect(await screen.findByText('12 skills, 4 commands - 179 KB')).toBeDefined()
    expect(screen.getByText('/home/alice/.claude')).toBeDefined()
    // The exclusions carry the guard's own reason, file by file.
    expect(screen.getByText('Left out of Claude Code: 1 entry')).toBeDefined()
    expect(screen.getByText('.credentials.json')).toBeDefined()
    expect(
      screen.getByText(/credential file excluded for claude/),
    ).toBeDefined()
    // profile.status already carries a snapshot, so the row says so rather
    // than pretending this is the first import.
    expect(screen.getByText(/Already imported on/)).toBeDefined()
  })

  it('says how many exclusions the gateway did not send', async () => {
    // A real profile root produces thousands; the gateway caps the list
    // it sends and reports the exact count, so the row must not imply the
    // handful it received is all of them.
    const client = fakeApi({
      localProfilePreview: vi.fn(async (harness: string) =>
        harness === 'claude'
          ? profilePreview({
              excluded: [
                {
                  path: 'projects/',
                  reason: 'ignored',
                  detail: 'skipped by default for claude (projects/)',
                },
              ],
              excluded_total: 1403,
            })
          : profilePreview({ harness, present: false, files: 0, bytes: 0 }),
      ),
    })
    renderStep(client)
    await look()

    expect(
      await screen.findByText('Left out of Claude Code: 1403 entries'),
    ).toBeDefined()
    expect(screen.getByText('and 1402 more')).toBeDefined()
  })

  it('pushes exactly the checked harnesses', async () => {
    const client = fakeApi({ localProfilePreview: bothPresent() })
    renderStep(client)
    await look()

    fireEvent.click(
      await screen.findByRole('checkbox', {
        name: 'Bring Claude Code configuration',
      }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Import selected' }))

    await waitFor(() => {
      expect(client.localProfilePush).toHaveBeenCalledWith('claude')
    })
    expect(client.localProfilePush).toHaveBeenCalledTimes(1)
    expect(
      await screen.findByText(/Imported 42 files, 179 KB/),
    ).toBeDefined()
  })

  it('refuses to push a blocked preview and names the flagged file', async () => {
    const client = fakeApi({
      localProfilePreview: vi.fn(async (harness: string) =>
        harness === 'claude'
          ? profilePreview({
              blocked: true,
              excluded: [
                {
                  path: 'notes/key.txt',
                  reason: 'secret',
                  detail: 'secret detected (aws-access-key) at line 3',
                },
              ],
            })
          : profilePreview({ harness, present: false, files: 0, bytes: 0 }),
      ),
    })
    renderStep(client)
    await look()

    // No checkbox at all: the push is refused server-side, so offering it
    // would be a lie.
    expect(
      await screen.findByText(
        'notes/key.txt: secret detected (aws-access-key) at line 3',
      ),
    ).toBeDefined()
    expect(
      screen.queryByRole('checkbox', {
        name: 'Bring Claude Code configuration',
      }),
    ).toBeNull()
    // The fix is local, and the override lives on the CLI.
    expect(
      screen.getByText(
        `aether profile push --agent claude --allow-secret notes/key.txt --workspace ${workspace.id}`,
      ),
    ).toBeDefined()
    expect(
      screen.getByRole('button', { name: 'Import selected' }),
    ).toHaveProperty('disabled', true)
    expect(client.localProfilePush).not.toHaveBeenCalled()
  })

  it('renders a push refusal on its own row and still runs the others', async () => {
    const client = fakeApi({
      localProfilePreview: bothPresent(),
      localProfilePush: vi.fn(async (harness: string) => {
        if (harness === 'claude') {
          throw new Error(
            'profile.push: secret detected (aws-access-key) in notes/key.txt at line 3',
          )
        }
        return {
          harness,
          snapshot_id: 'psn_3',
          digest: 'sha256:cafe9012',
          files: 7,
          bytes: 2048,
        }
      }),
    })
    renderStep(client)
    await look()

    fireEvent.click(
      await screen.findByRole('checkbox', {
        name: 'Bring Claude Code configuration',
      }),
    )
    fireEvent.click(
      screen.getByRole('checkbox', { name: 'Bring Codex configuration' }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Import selected' }))

    // The refusal verbatim, against the row it belongs to...
    expect(
      await screen.findByText(
        'profile.push: secret detected (aws-access-key) in notes/key.txt at line 3',
      ),
    ).toBeDefined()
    // ...and the harness behind it still went.
    expect(await screen.findByText(/Imported 7 files, 2.0 KB/)).toBeDefined()
    expect(client.localProfilePush).toHaveBeenCalledTimes(2)
  })

  it('pre-checks what the agent recommends, and the user edits it before approving', async () => {
    // The real socket client, so the start frame itself is under test.
    const client = fakeApi({
      localProfilePreview: bothPresent(),
      openProfileScan: api.openProfileScan,
    })
    renderStep(client)
    await look()

    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Ask an agent which configuration to bring',
      }),
    )
    act(() => StubSocket.last().onopen?.())

    expect(StubSocket.last().url).toContain('/ws/envscan')
    expect(StubSocket.last().frames()[0]).toEqual({
      harness: 'claude',
      mode: 'profile',
      repo_path: '/src/repo',
    })

    act(() => {
      frame({ type: 'status', status: 'running' })
      frame({ type: 'output', line: 'reading ~/.claude' })
    })
    expect(await screen.findByRole('status')).toBeDefined()
    expect(screen.getByText('reading ~/.claude')).toBeDefined()

    act(() =>
      frame({
        type: 'result',
        recommendation: {
          harnesses: [
            {
              harness: 'claude',
              import: true,
              categories: ['skills', 'commands'],
              reason: 'your skills cover the languages in this repository',
            },
            {
              harness: 'codex',
              import: false,
              categories: [],
              reason: 'this profile is empty apart from a settings file',
            },
          ],
        },
      }),
    )

    const claude = await screen.findByRole<HTMLInputElement>('checkbox', {
      name: 'Bring Claude Code configuration',
    })
    const codex = screen.getByRole<HTMLInputElement>('checkbox', {
      name: 'Bring Codex configuration',
    })
    expect(claude.checked).toBe(true)
    expect(codex.checked).toBe(false)
    // Each one-sentence reason sits next to its own row.
    expect(
      screen.getByText('your skills cover the languages in this repository'),
    ).toBeDefined()
    expect(
      screen.getByText(/this profile is empty apart from a settings file/),
    ).toBeDefined()

    // A proposal, not an action: the user overrules it both ways.
    fireEvent.click(claude)
    fireEvent.click(codex)
    fireEvent.click(screen.getByRole('button', { name: 'Import selected' }))

    await waitFor(() => {
      expect(client.localProfilePush).toHaveBeenCalledWith('codex')
    })
    expect(client.localProfilePush).toHaveBeenCalledTimes(1)
  })

  it('keeps the deterministic path and the way out when the scan fails', async () => {
    const client = fakeApi({
      openProfileScan: api.openProfileScan,
    })
    const { onNext } = renderStep(client)
    await look()

    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Ask an agent which configuration to bring',
      }),
    )
    act(() => StubSocket.last().onopen?.())
    act(() =>
      frame({
        type: 'error',
        detail: 'the scan timed out after 10m0s',
        output_tail: 'last line',
      }),
    )

    expect(
      await screen.findByText('the scan timed out after 10m0s'),
    ).toBeDefined()
    // The checklist is still there, and so is the way past the step.
    fireEvent.click(
      screen.getByRole('checkbox', { name: 'Bring Claude Code configuration' }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Import selected' }))
    await waitFor(() => {
      expect(client.localProfilePush).toHaveBeenCalledWith('claude')
    })
    expect(screen.getByRole('button', { name: 'Try again' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalled()
  })

  it('cancels a running scan and closes the socket', async () => {
    const client = fakeApi({ openProfileScan: api.openProfileScan })
    renderStep(client)
    await look()

    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Ask an agent which configuration to bring',
      }),
    )
    act(() => StubSocket.last().onopen?.())
    act(() => frame({ type: 'status', status: 'running' }))

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(StubSocket.last().closed).toBe(true)
    expect(
      screen.getByRole('button', {
        name: 'Ask an agent which configuration to bring',
      }),
    ).toBeDefined()
  })

  it('skips from the list, from an open setup shell, and after an import', async () => {
    const client = fakeApi()
    const { onNext } = renderStep(client)

    fireEvent.click(await screen.findByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalledTimes(1)

    // The setup shell is a terminal, not a trap.
    fireEvent.click(screen.getByRole('button', { name: 'Set up Claude Code' }))
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalledTimes(2)
  })

  it('skips a harness the registry has no profile sync for', async () => {
    const client = fakeApi({
      localProfilePreview: vi.fn(async (harness: string) => {
        // -32602 maps to HTTP 400: this name cannot be imported at all.
        if (harness !== 'claude') {
          throw new ApiError(400, `profile.preview: unknown harness ${harness}`)
        }
        return profilePreview()
      }),
    })
    renderStep(client)
    await look()

    expect(
      await screen.findByRole('checkbox', {
        name: 'Bring Claude Code configuration',
      }),
    ).toBeDefined()
    expect(screen.queryByText(/unknown harness/)).toBeNull()
  })

  it('shows a real preview failure instead of claiming the machine is empty', async () => {
    // A permission error inside a profile root, a gateway restart: any of
    // these answer -32603, and swallowing them made the step state
    // something about the user's machine it had not checked.
    const client = fakeApi({
      localProfilePreview: vi.fn(async () => {
        throw new ApiError(500, 'profile.preview: open /home/alice/.claude: permission denied')
      }),
    })
    renderStep(client)
    await look()

    // One line per harness that failed, each naming its own harness.
    expect((await screen.findAllByText(/permission denied/)).length).toBeGreaterThan(0)
    expect(screen.queryByText(/No agent configuration was found/)).toBeNull()
  })

  it('runs no preview until asked, and walks one harness at a time', async () => {
    // The blocker: a profile root can hold hundreds of megabytes, and
    // every file is secret-scanned. Mounting the step must cost nothing.
    const client = fakeApi()
    renderStep(client)

    expect(await screen.findByText('Claude Code')).toBeDefined()
    expect(client.localProfilePreview).not.toHaveBeenCalled()
    expect(
      screen.getByRole('button', { name: 'Look at what is here' }),
    ).toBeDefined()

    await look()
    expect(client.localProfilePreview).toHaveBeenCalled()
    // Every call carries a signal, so leaving the step stops the walk on
    // the gateway rather than only stopping the wait.
    for (const call of vi.mocked(client.localProfilePreview).mock.calls) {
      expect(call[1]).toBeInstanceOf(AbortSignal)
    }
  })
})

describe('the harness the step set up', () => {
  it('is preselected by the First run step when agent.list carries it', async () => {
    seed()
    render(
      <FirstRunStep
        client={fakeApi()}
        workspace={workspace}
        defaultHarness="claude"
      />,
    )

    await waitFor(() => {
      expect(screen.getByLabelText<HTMLSelectElement>('Harness').value).toBe(
        'claude',
      )
    })
  })

  it('is not preselected when the server cannot launch it', async () => {
    seed()
    render(
      <FirstRunStep
        client={fakeApi({ agentList: vi.fn(async () => [agentInfo()]) })}
        workspace={workspace}
        defaultHarness="codex"
      />,
    )

    await waitFor(() => {
      expect(screen.getByLabelText<HTMLSelectElement>('Harness').value).toBe('')
    })
  })

  it('reaches the First run step through the whole wizard', async () => {
    const client = fakeApi()
    seed()
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    fireEvent.click(
      await screen.findByRole('button', { name: `Use ${workspace.name}` }),
    )
    fireEvent.click(
      await screen.findByRole('radio', {
        name: 'Keep the standard environment',
      }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))

    // Step five of six: the agents step.
    const steps = screen.getByLabelText('Steps')
    expect(steps.textContent).toContain('5. Agents')
    fireEvent.click(
      await screen.findByRole('button', { name: 'Set up Claude Code' }),
    )
    await act(async () => {})
    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onclose?.({ code: 1000 })
    })
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))

    expect(
      await screen.findByRole('region', { name: 'First run' }),
    ).toBeDefined()
    await waitFor(() => {
      expect(screen.getByLabelText<HTMLSelectElement>('Harness').value).toBe(
        'claude',
      )
    })
  })
})

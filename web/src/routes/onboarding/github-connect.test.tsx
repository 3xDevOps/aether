import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { ApiError } from '@/lib/api'
import type { Api } from '@/lib/api'
import { githubLoginCommand } from '@/lib/github'
import type { GatewayCapabilities, GitHubProbeResult } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { useStore } from '@/store'
import {
  registerEnvTerminalSocket,
  setEnvTerminalSocketReady,
  type EnvTerminalSocket,
} from '@/store/env-terminal'
import { capability } from '@/store/hooks'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

// The local gateway: every method, the terminal socket the dock rides on,
// and the client-machine verbs the wizard needs.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal', 'envscan'],
  local: [
    'link.status',
    'link.repo',
    'env.harnesses',
    'profile.preview',
    'profile.push',
  ],
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
    onboardingStep: 'Link',
    onboardingWorkspace: '',
    onboardingRepo: null,
  })
}

/**
 * A client whose environment terminal is already up. The gh probe runs
 * inside that container and waits for the dock to report one, and the dock
 * learns it from `terminal.status` or its own attach.
 */
function runningApi(overrides: Partial<Api> = {}) {
  return fakeApi({
    terminalStatus: vi.fn(async () => ({ running: true, tabs: ['main'] })),
    ...overrides,
  })
}

/** The step on its own, with the wizard's sub-screen slot as local state. */
function renderStep(client: Api, caps: GatewayCapabilities = localCaps) {
  seed(caps)
  const onNext = vi.fn()
  function Host() {
    const [setup, onSetup] = useState('')
    return (
      <AgentsStep
        client={client}
        caps={capability(caps)}
        workspace={workspace}
        setup={setup}
        onSetup={onSetup}
        onNext={onNext}
        onReady={vi.fn()}
      />
    )
  }
  render(<Host />)
  return { onNext }
}

/**
 * An attached main tab, so a line the dock types is sent rather than queued.
 * The real socket only reaches this state after the environment container
 * has started.
 */
function attachMainTab(): EnvTerminalSocket {
  const socket = {
    send: vi.fn(),
    resize: vi.fn(),
    reopen: vi.fn(),
    close: vi.fn(),
  }
  useStore.getState().resetEnvTerminal()
  registerEnvTerminalSocket('main', socket)
  // The gh probe runs inside the container, so it waits for the dock to
  // report one. The real dock sets this when its socket attaches.
  useStore.getState().setEnvTerminalStatus({ running: true, tabs: ['main'] })
  return socket
}

/** Opens the Connect GitHub sub-screen from the step's own screen, once the
 * harness list the step loads on mount has settled. */
async function open() {
  await screen.findByText('Claude Code')
  fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
  await act(async () => {})
  // Mounting the dock marks its connection unattached until the server
  // acks; the real one acks, and a line typed before that is queued until
  // it does.
  act(() => setEnvTerminalSocketReady('main', true))
}

/** Walks the whole wizard from Link to the Agents step. */
async function toAgentsStep() {
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Skip' }))
  fireEvent.click(
    await screen.findByRole('button', { name: `Use ${workspace.name}` }),
  )
  fireEvent.change(await screen.findByLabelText('Repository path'), {
    target: { value: '/home/alice/code/myproject' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
}

beforeEach(() => {
  StubSocket.install()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// Every case here mounts the whole Agents step and a terminal dock, which
// is several seconds of real awaits in jsdom even idle. The 5s default is
// a coin flip on a loaded CI runner, so the block carries its own budget.
describe('connect GitHub', { timeout: 20_000 }, () => {
  it('types the login command into the environment terminal', async () => {
    const socket = attachMainTab()
    renderStep(runningApi())

    // The closed section is part of the step, not a screen of its own.
    expect(
      await screen.findByRole('region', { name: 'Connect GitHub' }),
    ).toBeDefined()
    await open()

    expect(screen.getByRole('region', { name: 'Terminal dock' })).toBeDefined()
    // Ctrl-U, the kill-line every shell the terminal opens honors, so the
    // command cannot land on top of whatever the member typed at the
    // prompt while the check was out.
    await waitFor(() => {
      expect(socket.send).toHaveBeenCalledWith(`\u0015${githubLoginCommand}\n`)
    })
    // The command is on screen too: a terminal that is still starting has
    // not shown it yet, and it is what a member retypes by hand.
    expect(screen.getByText(githubLoginCommand)).toBeDefined()
  })

  it('names the account and the key it registered, then offers Continue', async () => {
    attachMainTab()
    const client = runningApi()
    renderStep(client)
    await open()

    fireEvent.click(screen.getByRole('button', { name: "I've logged in" }))
    await act(async () => {})

    expect(client.githubConnect).toHaveBeenCalled()
    expect(
      screen.getByText(
        /Connected to GitHub as octocat\. Signing key SHA256:9wPnHRtG0DPQNo8VYbC2mSczRRRUYY7NoLgTHTAlYFA is registered on your account\./,
      ),
    ).toBeDefined()

    // Closing returns to the step, which now says who it connected as, and
    // a connection counts the way a set-up agent does for Continue.
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(
      await screen.findByText('Connected in this session as octocat'),
    ).toBeDefined()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeDefined()
  })

  it('renders the server refusal verbatim and keeps the way out', async () => {
    attachMainTab()
    const { onNext } = renderStep(
      runningApi({
        githubConnect: vi.fn(async () => {
          throw new ApiError(
            400,
            'github.connect: not logged in to github.com in the environment terminal; run gh auth login there first',
          )
        }),
      }),
    )
    await open()

    fireEvent.click(screen.getByRole('button', { name: "I've logged in" }))

    expect(
      await screen.findByText(
        'github.connect: not logged in to github.com in the environment terminal; run gh auth login there first',
      ),
    ).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalled()
  })

  it('shows no login command until the probe has answered', async () => {
    const socket = attachMainTab()
    // A probe that has not settled: this is the whole window the check
    // exists for, and it is as long as the container takes to answer.
    renderStep(
      runningApi({
        githubProbe: vi.fn(() => new Promise<GitHubProbeResult>(() => {})),
      }),
    )
    await open()

    // The status line is what a screen reader is told; the dock renders
    // one of its own, so this asks for the screen's.
    const status = screen
      .getAllByRole('status')
      .map((node) => node.textContent)
    expect(status).toContain('Checking your environment terminal for gh...')
    expect(screen.queryByText(githubLoginCommand)).toBeNull()
    expect(socket.send).not.toHaveBeenCalled()
  })

  it('says the check failed, offers it again, and still gives the command', async () => {
    attachMainTab()
    const probe = vi.fn(async (): Promise<GitHubProbeResult> => {
      throw new ApiError(
        400,
        'github.probe: terminal: environment terminal is not running; open it first',
      )
    })
    renderStep(runningApi({ githubProbe: probe }))
    await open()

    // Failing open, not failing silent: the command comes back, and so
    // does what went wrong.
    expect(
      screen.getByText(
        'github.probe: terminal: environment terminal is not running; open it first',
      ),
    ).toBeDefined()
    expect(screen.getByText(githubLoginCommand)).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await act(async () => {})
    expect(probe).toHaveBeenCalledTimes(2)
  })

  it('replaces the login command when the environment has no gh', async () => {
    const socket = attachMainTab()
    renderStep(
      runningApi({
        githubProbe: vi.fn(async () => ({
          status: 'missing' as const,
          minimum: '2.81.0',
          detail:
            'OCI runtime exec failed: exec failed: unable to start container process: exec: "gh": executable file not found in $PATH',
          image: 'ghcr.io/3xdevops/aether-standard:latest',
          remedy: 'aether terminal stop',
          admin_remedy: 'docker pull ghcr.io/3xdevops/aether-standard:latest',
        })),
      }),
    )
    await open()

    // Typing a login into a container with no gh is what sent the member in
    // circles, so the command is never sent and never shown.
    expect(socket.send).not.toHaveBeenCalled()
    expect(screen.queryByText(githubLoginCommand)).toBeNull()
    // Both halves: the admin's, and the reopen without which a container
    // that is already up keeps the image it started from.
    expect(
      screen.getByText('docker pull ghcr.io/3xdevops/aether-standard:latest'),
    ).toBeDefined()
    expect(screen.getByText('aether terminal stop')).toBeDefined()
    // The real error stays on screen.
    expect(screen.getByText(/executable file not found in \$PATH/)).toBeDefined()
  })

  it('offers the way out that keeps a saved environment first', async () => {
    attachMainTab()
    renderStep(
      runningApi({
        githubProbe: vi.fn(async () => ({
          status: 'missing' as const,
          minimum: '2.81.0',
          detail: 'sh: 1: gh: not found',
          image: 'aether/member-mbr_1:1788827030',
          saved_image: 'aether/member-mbr_1:1788827030',
          remedy: 'aether env reset',
        })),
      }),
    )
    await open()

    // env reset deletes the image the member built up, so the screen says
    // so rather than handing over a one-line command that throws it away.
    expect(
      screen.getByText(
        /Install a current gh in the terminal below and press Save environment, or press Reset to standard - which removes your saved aether\/member-mbr_1:1788827030\./,
      ),
    ).toBeDefined()
    expect(screen.getByText('aether env reset')).toBeDefined()
  })

  it('names the gh it found and the oldest one that works', async () => {
    const socket = attachMainTab()
    renderStep(
      runningApi({
        githubProbe: vi.fn(async () => ({
          status: 'outdated' as const,
          version: '2.45.0',
          minimum: '2.81.0',
          detail: 'gh version 2.45.0 (2025-07-18 Ubuntu 2.45.0-1ubuntu0.3)',
          image: 'ghcr.io/3xdevops/aether-standard:v0.2.0-alpha.5',
          remedy: 'aether terminal stop',
          admin_remedy: 'aether server update',
        })),
      }),
    )
    await open()

    expect(socket.send).not.toHaveBeenCalled()
    expect(
      screen.getByText(
        /gh 2\.45\.0 in your environment terminal cannot answer the login check; 2\.81\.0 is the oldest that can\./,
      ),
    ).toBeDefined()
    // A release-tagged standard image never moves in the registry, so
    // repulling it is not the answer; a newer server is.
    expect(screen.getByText('aether server update')).toBeDefined()
  })

  it('offers the check again from the remedy, not only from a failure', async () => {
    attachMainTab()
    const probe = vi.fn(async () => ({
      status: 'missing' as const,
      minimum: '2.81.0',
      detail: 'sh: 1: gh: not found',
      image: 'aether/member-mbr_1:1788827030',
      saved_image: 'aether/member-mbr_1:1788827030',
      remedy: 'aether env reset',
    }))
    renderStep(runningApi({ githubProbe: probe }))
    await open()

    // Installing gh and saving the environment restarts nothing, so
    // without this button the screen can never confirm the remedy worked.
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await act(async () => {})
    expect(probe).toHaveBeenCalledTimes(2)
  })

  it('names the member\'s own gh instead of an image no remedy can reach', async () => {
    attachMainTab()
    renderStep(
      runningApi({
        githubProbe: vi.fn(async () => ({
          status: 'outdated' as const,
          version: '2.45.0',
          minimum: '2.81.0',
          detail: 'gh version 2.45.0 (2025-07-18 Ubuntu 2.45.0-1ubuntu0.3)',
          image: 'ghcr.io/3xdevops/aether-standard:latest',
          path: '/root/.local/bin/gh',
          remedy: 'rm /root/.local/bin/gh',
        })),
      }),
    )
    await open()

    expect(screen.getByText(/gh 2\.45\.0 at \/root\/\.local\/bin\/gh/)).toBeDefined()
    expect(screen.getByText('rm /root/.local/bin/gh')).toBeDefined()
    // No image remedy: that file survives every one of them.
    expect(screen.queryByText(/docker pull|aether server update|aether terminal stop/)).toBeNull()
  })

  it('says a gh that will not run is there, not missing', async () => {
    attachMainTab()
    renderStep(
      runningApi({
        githubProbe: vi.fn(async () => ({
          status: 'broken' as const,
          minimum: '2.81.0',
          detail: 'gh --version exited 126: permission denied',
          image: 'ghcr.io/3xdevops/aether-standard:latest',
          remedy: 'aether terminal stop',
          admin_remedy: 'docker pull ghcr.io/3xdevops/aether-standard:latest',
        })),
      }),
    )
    await open()

    expect(
      screen.getByText('gh is in your environment terminal but would not run.'),
    ).toBeDefined()
    expect(screen.getByText(/permission denied/)).toBeDefined()
    // Connecting would only collect the matching refusal.
    expect(
      screen.getByRole('button', { name: "I've logged in" }).hasAttribute('disabled'),
    ).toBe(true)
  })

  it('keeps the command back when the dock refuses a write mid-probe', async () => {
    const socket = attachMainTab()
    renderStep(
      runningApi({
        githubProbe: vi.fn(() => new Promise<GitHubProbeResult>(() => {})),
      }),
    )
    await open()

    // statusError is the dock's general error channel, written while the
    // container is running perfectly well. It is not the check answering.
    act(() => {
      useStore.getState().setEnvTerminalStatus(
        { running: true, tabs: ['main'] },
        'Terminal input was denied',
      )
    })
    expect(screen.queryByText(githubLoginCommand)).toBeNull()
    expect(socket.send).not.toHaveBeenCalled()

    // A dock that never got a terminal at all is the same dead end as a
    // failed probe: the command comes back, unrun.
    act(() => {
      useStore.getState().setEnvTerminalStatus(null, 'server unreachable')
    })
    expect(screen.getByText(githubLoginCommand)).toBeDefined()
    expect(socket.send).not.toHaveBeenCalled()
  })

  it('drops a previous container\'s answer when the terminal cycles', async () => {
    attachMainTab()
    renderStep(runningApi())
    await open()
    expect(
      await screen.findByText('The login command is ready in your environment terminal:'),
    ).toBeDefined()

    // Stopping the environment makes the standing answer describe a
    // container that no longer exists.
    act(() => {
      useStore.getState().setEnvTerminalStatus({ running: false, tabs: [] })
    })
    expect(screen.queryByText(githubLoginCommand)).toBeNull()
    expect(
      screen.getByText('Waiting for your environment terminal to start...'),
    ).toBeDefined()
  })

  it('gives the CLI path on a gateway without the terminal socket', async () => {
    renderStep(runningApi(), { ...localCaps, ws: ['events', 'attach', 'envscan'] })
    await open()

    expect(screen.queryByRole('region', { name: 'Terminal dock' })).toBeNull()
    expect(screen.getByText('aether terminal')).toBeDefined()
    expect(screen.getByText(githubLoginCommand)).toBeDefined()
    expect(screen.getByText('aether github connect')).toBeDefined()
    // The whole flow is the CLI's here: there is no terminal to log in
    // through, so the screen never offers to finish it from the browser.
    expect(
      screen.queryByRole('button', { name: "I've logged in" }),
    ).toBeNull()
  })

  it('walks Back out of the connect screen before it leaves the Agents step', async () => {
    attachMainTab()
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    await toAgentsStep()
    await open()

    expect(screen.getByRole('region', { name: 'Terminal dock' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    await waitFor(() => {
      expect(
        screen.getByRole('button', { name: 'Connect GitHub' }),
      ).toBeDefined()
    })
    expect(
      screen.getByRole('listitem', { current: 'step' }).textContent,
    ).toContain('Agents')

    // Only now does Back leave the step.
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(screen.getByRole('region', { name: 'Repository' })).toBeDefined()
  }, 20_000)
})

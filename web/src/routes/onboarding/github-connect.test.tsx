import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { ApiError } from '@/lib/api'
import type { Api } from '@/lib/api'
import { githubLoginCommand } from '@/lib/github'
import type { GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { useStore } from '@/store'
import {
  registerEnvTerminalSocket,
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
    onboardingStep: 'Link',
    onboardingWorkspace: '',
    onboardingRepo: null,
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
  return socket
}

/** Opens the Connect GitHub sub-screen from the step's own screen, once the
 * harness list the step loads on mount has settled. */
async function open() {
  await screen.findByText('Claude Code')
  fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
  await act(async () => {})
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
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('connect GitHub', () => {
  it('types the login command into the environment terminal', async () => {
    const socket = attachMainTab()
    renderStep(fakeApi())

    // The closed section is part of the step, not a screen of its own.
    expect(
      await screen.findByRole('region', { name: 'Connect GitHub' }),
    ).toBeDefined()
    await open()

    expect(screen.getByRole('region', { name: 'Terminal dock' })).toBeDefined()
    expect(socket.send).toHaveBeenCalledWith(`${githubLoginCommand}\n`)
    // The command is on screen too: a terminal that is still starting has
    // not shown it yet, and it is what a member retypes by hand.
    expect(screen.getByText(githubLoginCommand)).toBeDefined()
  })

  it('names the account and the key it registered, then offers Continue', async () => {
    attachMainTab()
    const client = fakeApi()
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
      fakeApi({
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

  it('gives the CLI path on a gateway without the terminal socket', async () => {
    renderStep(fakeApi(), { ...localCaps, ws: ['events', 'attach', 'envscan'] })
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

  // A whole wizard walk, several seconds of real awaits even idle, so it
  // carries its own budget rather than sitting just under the default.
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
    ).toContain('5. Agents')

    // Only now does Back leave the step.
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(
      screen.getByRole('listitem', { current: 'step' }).textContent,
    ).toContain('4. Repository')
  }, 20_000)
})

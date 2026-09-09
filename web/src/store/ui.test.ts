import { beforeEach, describe, expect, it } from 'vitest'
import {
  defaultTerminalFontSize,
  maxTerminalFontSize,
  minTerminalFontSize,
} from '@/lib/term-font'
import { createRootStore, useStore } from '@/store'

// Terminal zoom is one preference behind every terminal, and the shortcut
// steps it without checking bounds first, so the store is where the range
// has to hold.
describe('terminal zoom', () => {
  beforeEach(() => {
    useStore.setState({ terminalFontSize: defaultTerminalFontSize })
  })

  /** Rehydrates a fresh store from a payload this build's own version wrote. */
  function rehydrate(state: Record<string, unknown>) {
    window.localStorage.setItem('aether.ui', JSON.stringify({ state, version: 3 }))
    const hydrated = createRootStore().getState()
    window.localStorage.removeItem('aether.ui')
    return hydrated
  }

  it('keeps a requested size inside the supported range', () => {
    const { setTerminalFontSize } = useStore.getState()

    setTerminalFontSize(18)
    expect(useStore.getState().terminalFontSize).toBe(18)

    setTerminalFontSize(minTerminalFontSize - 5)
    expect(useStore.getState().terminalFontSize).toBe(minTerminalFontSize)

    setTerminalFontSize(maxTerminalFontSize + 5)
    expect(useStore.getState().terminalFontSize).toBe(maxTerminalFontSize)

    setTerminalFontSize(Number.NaN)
    expect(useStore.getState().terminalFontSize).toBe(defaultTerminalFontSize)
  })

  // Stored state is a file on disk, and a same-version reload never reaches
  // migrate. xterm does not validate fontSize either, so anything that got
  // in there would render a terminal nobody can read.
  it('clamps a stored size on the way back in', () => {
    expect(rehydrate({ terminalFontSize: 900 }).terminalFontSize).toBe(maxTerminalFontSize)
    expect(rehydrate({ terminalFontSize: -5 }).terminalFontSize).toBe(minTerminalFontSize)
    expect(rehydrate({ terminalFontSize: null }).terminalFontSize).toBe(defaultTerminalFontSize)
    expect(rehydrate({ terminalFontSize: 'huge' }).terminalFontSize).toBe(defaultTerminalFontSize)
    expect(rehydrate({ terminalFontSize: 18 }).terminalFontSize).toBe(18)
    expect(rehydrate({}).terminalFontSize).toBe(defaultTerminalFontSize)
  })

  it('takes only a stored true as control having been taken', () => {
    expect(rehydrate({ terminalControlTaken: true }).terminalControlTaken).toBe(true)
    expect(rehydrate({ terminalControlTaken: 'yes' }).terminalControlTaken).toBe(false)
    expect(rehydrate({}).terminalControlTaken).toBe(false)
  })

  it('leaves the other stored preferences alone', () => {
    const hydrated = rehydrate({ theme: 'dark', sidebarWidth: 320, terminalFontSize: 20 })

    expect(hydrated.theme).toBe('dark')
    expect(hydrated.sidebarWidth).toBe(320)
    expect(hydrated.terminalFontSize).toBe(20)
  })
})

// The active workspace and the workspace route are two views of one thing:
// which workspace the app is acting on. If they drift, the sidebar names one
// workspace while the open page and its dialogs act on another.
describe('workspace scope and route stay in sync', () => {
  beforeEach(() => {
    useStore.setState({
      activeWorkspace: 'wsp_1',
      route: { name: 'board', params: {} },
    })
  })

  it('carries the workspace route along when the scope switches', () => {
    useStore.getState().navigate('workspace', { workspaceId: 'wsp_1' })
    useStore.getState().setActiveWorkspace('wsp_2')

    const { activeWorkspace, route } = useStore.getState()
    expect(activeWorkspace).toBe('wsp_2')
    expect(route).toEqual({ name: 'workspace', params: { workspaceId: 'wsp_2' } })
  })

  it('leaves other routes alone when the scope switches', () => {
    useStore.getState().navigate('run', { runId: 'run_1' })
    useStore.getState().setActiveWorkspace('wsp_2')

    expect(useStore.getState().route).toEqual({
      name: 'run',
      params: { runId: 'run_1' },
    })
  })

  it('makes an opened workspace the active scope', () => {
    useStore.getState().navigate('workspace', { workspaceId: 'wsp_2' })
    expect(useStore.getState().activeWorkspace).toBe('wsp_2')
  })
  it('marks onboarding complete and clears its state when navigating away', () => {
    useStore.setState({
      route: { name: 'onboarding', params: {} },
      onboarded: false,
      onboardingStep: 'Agents',
      onboardingWorkspace: 'wsp_1',
      onboardingRepo: {
        link: 'lnk_1',
        workspace: 'wsp_1',
        path: '/home/alice/code/myproject',
        remote: { repo: '/home/alice/code/myproject', remote: 'aether', url: 'ssh://host/wsp_1' },
        push: null,
        fastForward: null,
      },
    })

    useStore.getState().navigate('board')

    expect(useStore.getState()).toMatchObject({
      route: { name: 'board', params: {} },
      onboarded: true,
      onboardingStep: 'Link',
      onboardingWorkspace: '',
      onboardingRepo: null,
    })
  })
  it('keeps onboarding state when navigating to onboarding again', () => {
    useStore.setState({
      route: { name: 'onboarding', params: {} },
      onboarded: false,
      onboardingStep: 'Repository',
      onboardingWorkspace: 'wsp_1',
    })

    useStore.getState().navigate('onboarding')

    expect(useStore.getState()).toMatchObject({
      route: { name: 'onboarding', params: {} },
      onboarded: false,
      onboardingStep: 'Repository',
      onboardingWorkspace: 'wsp_1',
    })
  })
})

// A dismissal is a version, not a flag. Storing a boolean would silence
// every future release the moment someone closed one banner.
describe('update dismissals are per version', () => {
  beforeEach(() => {
    useStore.setState({ dismissedUpdates: { cli: '', server: '', shell: '' } })
  })

  it('records the version dismissed, per kind', () => {
    useStore.getState().dismissUpdate('cli', 'v1.3.0')
    expect(useStore.getState().dismissedUpdates).toEqual({
      cli: 'v1.3.0',
      server: '',
      shell: '',
    })

    useStore.getState().dismissUpdate('server', 'v1.3.0')
    expect(useStore.getState().dismissedUpdates.server).toBe('v1.3.0')
  })

  it('clears every kind, which is what the status bar badge does', () => {
    useStore.getState().dismissUpdate('cli', 'v1.3.0')
    useStore.getState().dismissUpdate('server', 'v1.3.0')
    useStore.getState().dismissUpdate('shell', 'v1.3.0')
    useStore.getState().clearDismissedUpdates()
    expect(useStore.getState().dismissedUpdates).toEqual({
      cli: '',
      server: '',
      shell: '',
    })
  })

  it('survives a reload, unlike the check answer itself', () => {
    useStore.getState().dismissUpdate('cli', 'v1.3.0')
    const stored = JSON.parse(
      window.localStorage.getItem('aether.ui') ?? '{}',
    ) as { state?: Record<string, unknown> }
    expect(stored.state?.dismissedUpdates).toEqual({
      cli: 'v1.3.0',
      server: '',
      shell: '',
    })
    expect(stored.state).not.toHaveProperty('update')
  })
})

describe('terminal dock heights', () => {
  it('uses the defaults, clamps updates, and persists both preferences', () => {
    const initial = useStore.getState()
    expect(initial.terminalDockHeight).toBe(280)
    expect(initial.runDockHeight).toBe(240)

    initial.setTerminalDockHeight(0)
    initial.setRunDockHeight(window.innerHeight)

    expect(useStore.getState().terminalDockHeight).toBe(120)
    expect(useStore.getState().runDockHeight).toBe(
      Math.max(120, window.innerHeight - 200),
    )

    const stored = JSON.parse(
      window.localStorage.getItem('aether.ui') ?? '{}',
    ) as { state?: Record<string, unknown> }
    expect(stored.state).toMatchObject({
      terminalDockHeight: 120,
      runDockHeight: Math.max(120, window.innerHeight - 200),
    })
    useStore.setState({ terminalDockHeight: 280, runDockHeight: 240 })
  })
})

// The resume point is persisted by name because a wizard grows steps.
// Versions 0 and 1 wrote an index into a step list without "Git identity" in
// it, so every one of those numbers has to keep meaning the step it named
// then. Version 0 also wrote a Repository answer from before the comparison
// states, which matches none of them and is dropped.
describe('a persisted store from an older release', () => {
  /** Rehydrates a fresh store from a payload written at version. */
  function resumeFrom(version: number, onboardingStep: unknown): string {
    window.localStorage.setItem(
      'aether.ui',
      JSON.stringify({ state: { onboardingStep }, version }),
    )
    return createRootStore().getState().onboardingStep
  }

  it('drops the stale repository answer and keeps the other preferences', () => {
    // Version 0 stored a push result with no `state`: the Repository step
    // matches it against none of its states and renders a blank panel.
    window.localStorage.setItem(
      'aether.ui',
      JSON.stringify({
        version: 0,
        state: {
          theme: 'dark',
          activeWorkspace: 'wsp_2',
          onboardingStep: 2,
          onboardingWorkspace: 'wsp_2',
          onboardingRepo: {
            workspace: 'wsp_2',
            path: '/home/alice/code/myproject',
            remote: { remote: 'aether', url: 'ssh://alice@host:2222/wsp_2' },
            push: { branch: 'main', remote: 'aether', output: '' },
          },
        },
      }),
    )

    const migrated = createRootStore().getState()

    expect(migrated.onboardingRepo).toBeNull()
    expect(migrated.theme).toBe('dark')
    expect(migrated.activeWorkspace).toBe('wsp_2')
    expect(migrated.onboardingStep).toBe('Repository')
    expect(migrated.onboardingWorkspace).toBe('wsp_2')
    window.localStorage.removeItem('aether.ui')
  })

  it('drops a version 1 repository answer, which carries no link id', () => {
    // Version 1 already carried the comparison states but nothing to tell
    // one connection from the next, so the step has to ask again.
    window.localStorage.setItem(
      'aether.ui',
      JSON.stringify({
        version: 1,
        state: {
          onboardingStep: 3,
          onboardingRepo: {
            workspace: 'wsp_2',
            path: '/home/alice/code/myproject',
            remote: { remote: 'aether', url: 'ssh://alice@host:2222/wsp_2' },
            push: null,
            fastForward: null,
          },
        },
      }),
    )

    const migrated = createRootStore().getState()

    expect(migrated.onboardingStep).toBe('Agents')
    expect(migrated.onboardingRepo).toBeNull()
    window.localStorage.removeItem('aether.ui')
  })

  it('keeps a version 2 repository answer while renaming its step', () => {
    // Version 2 is the first shape this build can use as it stands, so only
    // the step needs reading back.
    const repo = {
      link: 'lnk_1',
      workspace: 'wsp_2',
      path: '/home/alice/code/myproject',
      remote: { remote: 'aether', url: 'ssh://alice@host:2222/wsp_2' },
      push: null,
      fastForward: null,
    }
    window.localStorage.setItem(
      'aether.ui',
      JSON.stringify({ version: 2, state: { onboardingStep: 3, onboardingRepo: repo } }),
    )

    const migrated = createRootStore().getState()

    expect(migrated.onboardingStep).toBe('Agents')
    expect(migrated.onboardingRepo).toEqual(repo)
    window.localStorage.removeItem('aether.ui')
  })

  it('maps every old index to the step it named', () => {
    for (const version of [0, 1, 2]) {
      expect(resumeFrom(version, 0)).toBe('Link')
      expect(resumeFrom(version, 1)).toBe('Workspace')
      expect(resumeFrom(version, 2)).toBe('Repository')
      expect(resumeFrom(version, 3)).toBe('Agents')
      expect(resumeFrom(version, 4)).toBe('First run')
    }
  })

  it('starts over on a value the wizard cannot place', () => {
    expect(resumeFrom(0, 9)).toBe('Link')
    expect(resumeFrom(0, -1)).toBe('Link')
    expect(resumeFrom(0, 'Repository')).toBe('Link')
    expect(resumeFrom(0, undefined)).toBe('Link')
  })

  it('runs the migrate on every version behind this one', () => {
    // Zustand calls migrate only when the stored version is older than the
    // configured one, so a payload seeded at the current version proves
    // nothing about it. All three older versions stored an index, and each
    // has to come back as the step it named.
    expect(resumeFrom(0, 3)).toBe('Agents')
    expect(resumeFrom(1, 3)).toBe('Agents')
    expect(resumeFrom(2, 3)).toBe('Agents')
    window.localStorage.removeItem('aether.ui')
  })

  it('reads the furthest step from the resume point it was stored without', () => {
    // Version 3 has no furthest step. Left at the slice default it would be
    // Link, and the first backward jump would turn every later step inert.
    window.localStorage.setItem(
      'aether.ui',
      JSON.stringify({ state: { onboardingStep: 'Agents' }, version: 3 }),
    )

    const migrated = createRootStore().getState()

    expect(migrated.onboardingStep).toBe('Agents')
    expect(migrated.onboardingFurthest).toBe('Agents')
    window.localStorage.removeItem('aether.ui')
  })

  it('leaves a payload at this version untouched', () => {
    window.localStorage.setItem(
      'aether.ui',
      JSON.stringify({
        state: { onboardingStep: 'Agents', onboardingFurthest: 'First run' },
        version: 4,
      }),
    )

    const migrated = createRootStore().getState()

    expect(migrated.onboardingStep).toBe('Agents')
    expect(migrated.onboardingFurthest).toBe('First run')
    window.localStorage.removeItem('aether.ui')
  })
})

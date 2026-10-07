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
  function rehydrate(state: Record<string, unknown>, version = 8) {
    window.localStorage.setItem('aether.ui', JSON.stringify({ state, version }))
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

  it('leaves the other stored preferences alone', () => {
    const hydrated = rehydrate({ theme: 'dark', sidebarWidth: 320, terminalFontSize: 20 })

    expect(hydrated.theme).toBe('dark')
    expect(hydrated.sidebarWidth).toBe(320)
    expect(hydrated.terminalFontSize).toBe(20)
  })

  it('drops the old run pane width from before the sidebar', () => {
    expect(rehydrate({ sidebarWidth: 320 }, 5).sidebarWidth).toBe(createRootStore().getState().sidebarWidth)
  })

  it('drops the per-account agent memory from a version 5 payload', () => {
    const hydrated = rehydrate({ lastHarnessByAccount: { mem_1: 'claude' }, theme: 'dark' }, 5)
    expect(hydrated).not.toHaveProperty('lastHarnessByAccount')
    expect(hydrated.theme).toBe('dark')
  })

  it('drops the run dock height from a version 7 payload', () => {
    const hydrated = rehydrate({ runDockHeight: 240, theme: 'dark' }, 7)
    expect(hydrated).not.toHaveProperty('runDockHeight')
    expect(hydrated.theme).toBe('dark')
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
  it('marks onboarding complete and keeps its progress when navigating away', () => {
    useStore.setState({
      route: { name: 'onboarding', params: {} },
      onboarded: false,
      onboardingStep: 'Repository',
      onboardingFurthest: 'Agent',
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
      onboardingStep: 'Agent',
      onboardingWorkspace: 'wsp_1',
      onboardingRepo: { workspace: 'wsp_1' },
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

  it('clears every kind at once', () => {
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

describe('terminal dock height', () => {
  it('uses the default, clamps updates, and persists the preference', () => {
    const initial = useStore.getState()
    expect(initial.terminalDockHeight).toBe(280)

    initial.setTerminalDockHeight(0)

    expect(useStore.getState().terminalDockHeight).toBe(120)
    const stored = JSON.parse(
      window.localStorage.getItem('aether.ui') ?? '{}',
    ) as { state?: Record<string, unknown> }
    expect(stored.state).toMatchObject({ terminalDockHeight: 120 })
    useStore.setState({ terminalDockHeight: 280 })
  })
})

describe('run view memory', () => {
  it('remembers the view a run was opened in for the rest of the visit', () => {
    useStore.getState().navigate('run', { runId: 'run_1', view: 'changes' })
    useStore.getState().navigate('board')
    useStore.getState().navigate('run', { runId: 'run_1' })

    expect(useStore.getState().runViewMemory).toEqual({ run_1: 'changes' })
    const stored = JSON.parse(window.localStorage.getItem('aether.ui') ?? '{}') as { state?: Record<string, unknown> }
    expect(stored.state).not.toHaveProperty('runViewMemory')
    useStore.setState({ runViewMemory: {} })
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

    expect(migrated.onboardingStep).toBe('Agent')
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

    expect(migrated.onboardingStep).toBe('Agent')
    expect(migrated.onboardingRepo).toEqual(repo)
    window.localStorage.removeItem('aether.ui')
  })

  it('maps every old index to the step that absorbed the one it named', () => {
    for (const version of [0, 1, 2]) {
      expect(resumeFrom(version, 0)).toBe('Connect')
      expect(resumeFrom(version, 1)).toBe('Repository')
      expect(resumeFrom(version, 2)).toBe('Repository')
      expect(resumeFrom(version, 3)).toBe('Agent')
      expect(resumeFrom(version, 4)).toBe('First run')
    }
  })

  it('starts over on a value the wizard cannot place', () => {
    expect(resumeFrom(0, 9)).toBe('Connect')
    expect(resumeFrom(0, -1)).toBe('Connect')
    expect(resumeFrom(0, 'Repository')).toBe('Connect')
    expect(resumeFrom(0, undefined)).toBe('Connect')
    expect(resumeFrom(7, 'Somewhere')).toBe('Connect')
  })

  it('maps each of the six old step names to the step that absorbed it', () => {
    expect(resumeFrom(7, 'Link')).toBe('Connect')
    expect(resumeFrom(7, 'Git identity')).toBe('Connect')
    expect(resumeFrom(7, 'Workspace')).toBe('Repository')
    expect(resumeFrom(7, 'Repository')).toBe('Repository')
    expect(resumeFrom(7, 'Agents')).toBe('Agent')
    expect(resumeFrom(7, 'First run')).toBe('First run')
  })

  it('runs the migrate on every version behind this one', () => {
    // Zustand calls migrate only when the stored version is older than the
    // configured one, so a payload seeded at the current version proves
    // nothing about it. All three older versions stored an index, and each
    // has to come back as the step it named.
    expect(resumeFrom(0, 3)).toBe('Agent')
    expect(resumeFrom(1, 3)).toBe('Agent')
    expect(resumeFrom(2, 3)).toBe('Agent')
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

    expect(migrated.onboardingStep).toBe('Agent')
    expect(migrated.onboardingFurthest).toBe('Agent')
    window.localStorage.removeItem('aether.ui')
  })

  it('leaves a payload at this version untouched', () => {
    window.localStorage.setItem(
      'aether.ui',
      JSON.stringify({
        state: { onboardingStep: 'Agent', onboardingFurthest: 'First run' },
        version: 8,
      }),
    )

    const migrated = createRootStore().getState()

    expect(migrated.onboardingStep).toBe('Agent')
    expect(migrated.onboardingFurthest).toBe('First run')
    window.localStorage.removeItem('aether.ui')
  })
})

describe('scoped Board preferences', () => {
  it('restores each workspace view and exact map cameras after a reload', () => {
    const store = createRootStore().getState()
    store.setBoardView('first', 'map')
    store.setBoardView('second', 'board')
    store.setBoardMapAllWorkspaces(true)
    store.setBoardMapViewport('["",false,false]', { x: -3100.25, y: 87.5, zoom: 0.73 })
    store.setBoardMapViewport('["first",true,true]', { x: 1, y: 2, zoom: 1.4 })
    const restored = createRootStore().getState()
    expect(restored.boardViews).toEqual({ first: 'map', second: 'board' })
    expect(restored.boardMapAllWorkspaces).toBe(true)
    expect(restored.boardMapViewports).toEqual({
      '["",false,false]': { x: -3100.25, y: 87.5, zoom: 0.73 },
      '["first",true,true]': { x: 1, y: 2, zoom: 1.4 },
    })
  })

  it('rejects corrupt cameras and unknown views instead of losing the canvas on reload', () => {
    window.localStorage.setItem('aether.ui', JSON.stringify({
      version: 8,
      state: {
        boardViews: { good: 'map', bad: 'unknown' },
        boardMapViewports: {
          valid: { x: 10, y: 20, zoom: 100 },
          invalid: { x: null, y: 20, zoom: 1 },
          zero: { x: 0, y: 0, zoom: 0 },
        },
      },
    }))
    const restored = createRootStore().getState()
    expect(restored.boardViews).toEqual({ good: 'map' })
    expect(restored.boardMapViewports).toEqual({ valid: { x: 10, y: 20, zoom: 2 } })
    restored.setBoardMapViewport('valid', { x: Infinity, y: 0, zoom: 1 })
    expect(createRootStore().getState().boardMapViewports.valid).toEqual({ x: 10, y: 20, zoom: 2 })
  })
})

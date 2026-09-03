import { beforeEach, describe, expect, it } from 'vitest'
import { useStore } from '@/store'

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
})

// A dismissal is a version, not a flag. Storing a boolean would silence
// every future release the moment someone closed one banner.
describe('update dismissals are per version', () => {
  beforeEach(() => {
    useStore.setState({ dismissedUpdates: { cli: '', server: '' } })
  })

  it('records the version dismissed, per kind', () => {
    useStore.getState().dismissUpdate('cli', 'v1.3.0')
    expect(useStore.getState().dismissedUpdates).toEqual({
      cli: 'v1.3.0',
      server: '',
    })

    useStore.getState().dismissUpdate('server', 'v1.3.0')
    expect(useStore.getState().dismissedUpdates.server).toBe('v1.3.0')
  })

  it('clears both kinds, which is what the status bar badge does', () => {
    useStore.getState().dismissUpdate('cli', 'v1.3.0')
    useStore.getState().dismissUpdate('server', 'v1.3.0')
    useStore.getState().clearDismissedUpdates()
    expect(useStore.getState().dismissedUpdates).toEqual({ cli: '', server: '' })
  })

  it('survives a reload, unlike the check answer itself', () => {
    useStore.getState().dismissUpdate('cli', 'v1.3.0')
    const stored = JSON.parse(
      window.localStorage.getItem('aether.ui') ?? '{}',
    ) as { state?: Record<string, unknown> }
    expect(stored.state?.dismissedUpdates).toEqual({ cli: 'v1.3.0', server: '' })
    expect(stored.state).not.toHaveProperty('update')
  })
})

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

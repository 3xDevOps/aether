import { createRootStore } from '@/store'
import type { WorkspaceShellReq } from '@/store/shell'

const req: WorkspaceShellReq = {
  workspace: { name: 'main-repo' },
  mode: 'agent-setup',
  harness: 'myagent',
  tui_args: ['--tui'],
  headless_args: ['--headless'],
}

describe('shell slice', () => {
  it('opens a shell request and forgets it on close', () => {
    const store = createRootStore()
    expect(store.getState().shellRequest).toBeNull()

    store.getState().openShell(req)
    expect(store.getState().shellRequest).toEqual(req)

    store.getState().closeShell()
    expect(store.getState().shellRequest).toBeNull()
  })

  it('replaces an open request wholesale: one shell at a time', () => {
    const store = createRootStore()
    store.getState().openShell(req)

    store.getState().openShell({
      workspace: { id: 'wsp_1' },
      mode: 'bootstrap-tools',
    })

    expect(store.getState().shellRequest).toEqual({
      workspace: { id: 'wsp_1' },
      mode: 'bootstrap-tools',
    })
  })
})

describe('local slice', () => {
  it('indexes sync sessions by run id', () => {
    const store = createRootStore()
    expect(store.getState().syncSessions).toEqual({})

    store.getState().setSyncSessions([
      { run_id: 'run_1', state: 'running', conflict: null },
      { run_id: 'run_2', state: 'conflict', conflict: 'src/checkout.ts' },
    ])

    expect(store.getState().syncSessions).toEqual({
      run_1: { state: 'running', conflict: null },
      run_2: { state: 'conflict', conflict: 'src/checkout.ts' },
    })
  })

  it('replaces the sync map wholesale, so a stopped session disappears', () => {
    const store = createRootStore()
    store.getState().setSyncSessions([
      { run_id: 'run_1', state: 'running', conflict: null },
    ])

    store.getState().setSyncSessions([])

    expect(store.getState().syncSessions).toEqual({})
  })

  it('stores the last link.status answer', () => {
    const store = createRootStore()
    expect(store.getState().linkStatus).toBeNull()

    store.getState().setLinkStatus({
      server_configured: true,
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
    })

    expect(store.getState().linkStatus?.linked).toBe(true)
    expect(store.getState().linkStatus?.repo).toBe('/src/repo')
  })
})

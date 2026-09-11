import { act, render, screen, waitFor } from '@testing-library/react'
import { FilesRoute } from '@/routes/files'
import { useStore } from '@/store'
import { fakeApi, run, workspace } from '@/test/fixtures'

describe('Files cache request boundaries', () => {
  beforeEach(() => {
    useStore.getState().resetFiles()
    useStore.setState({
      capabilities: { gateway: 'remote', methods: ['config.roots'], ws: [] },
      workspaces: {},
      runs: {},
      identityKey: null,
    })
  })

  it('does not strand a pending config tree when another run invalidates', async () => {
    const tree = Promise.withResolvers<{ entries: Array<{ name: string; kind: 'file' | 'dir'; size: number }> }>()
    const client = fakeApi({
      configRoots: vi.fn(async () => ({ roots: [{ harness: 'claude', path: '~/.claude' }] })),
      configTree: vi.fn(() => tree.promise),
    })
    render(<FilesRoute params={{}} client={client} />)

    await waitFor(() => {
      expect(client.configTree).toHaveBeenCalledWith({ harness: 'claude', path: '' })
    })
    act(() => useStore.getState().invalidateRun('unrelated-run'))

    tree.resolve({ entries: [{ name: 'settings.json', kind: 'file', size: 2 }] })
    expect(await screen.findByRole('button', { name: 'settings.json' })).toBeDefined()
  })

  it('does not restore an invalidated run tree after its view unmounts', async () => {
    const pending = Promise.withResolvers<{ entries: Array<{ name: string; kind: 'file'; size: number }> }>()
    const runTree = vi.fn()
      .mockReturnValueOnce(pending.promise)
      .mockResolvedValue({ entries: [{ name: 'current.txt', kind: 'file', size: 1 }] })
    const currentRun = run()
    useStore.setState({
      capabilities: { gateway: 'server', methods: ['files.tree'], ws: [] },
    })
    useStore.getState().setWorkspaces([workspace])
    useStore.getState().setRuns([currentRun])
    const client = fakeApi({
      filesTree: vi.fn((params) => params.run_id ? runTree() : Promise.resolve({ entries: [] })),
    })
    const view = render(<FilesRoute params={{}} client={client} />)
    await waitFor(() => expect(runTree).toHaveBeenCalledTimes(1))
    view.unmount()
    act(() => useStore.getState().invalidateRun(currentRun.id))
    await act(async () => {
      pending.resolve({ entries: [{ name: 'stale.txt', kind: 'file', size: 1 }] })
      await pending.promise
    })
    render(<FilesRoute params={{}} client={client} />)
    expect(await screen.findByRole('button', { name: 'current.txt' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'stale.txt' })).toBeNull()
  })
})

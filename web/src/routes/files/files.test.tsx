import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { FilesRoute } from '@/routes/files'
import { useStore } from '@/store'
import { filesKey } from '@/store/files'
import { fakeApi, run, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

type Entries = { entries: Array<{ name: string; kind: 'file' | 'dir'; size: number }> }

const file = (name: string): Entries['entries'][number] => ({ name, kind: 'file', size: 1 })

function seed(methods = ['files.tree', 'config.roots']) {
  useStore.getState().resetFiles()
  useStore.setState({
    capabilities: { gateway: 'server', methods, ws: [] },
    workspaces: {},
    runs: {},
    identityKey: null,
  })
  useStore.getState().setWorkspaces([workspace])
  useStore.getState().setRuns([run()])
}

function tree() {
  return within(screen.getByRole('complementary', { name: 'Files' }))
}

describe('Files tree', () => {
  beforeEach(() => seed())

  it('opens on the base branch with run checkouts and agent config folded away', async () => {
    const client = fakeApi({
      filesTree: vi.fn(async (params) => (params.run_id ? { entries: [file('run.txt')] } : { entries: [file('README.md')] })),
    })
    render(<FilesRoute params={{}} client={client} />)

    expect(await tree().findByRole('button', { name: 'README.md' })).toBeDefined()
    const checkouts = tree().getByRole('button', { name: /^Run checkouts/ })
    const config = tree().getByRole('button', { name: /^Agent config/ })
    expect(checkouts.getAttribute('aria-expanded')).toBe('false')
    expect(config.getAttribute('aria-expanded')).toBe('false')
    expect(client.filesTree).not.toHaveBeenCalledWith(expect.objectContaining({ run_id: 'run_1' }))
    expect(client.configTree).not.toHaveBeenCalled()

    fireEvent.click(checkouts)
    fireEvent.click(tree().getByRole('button', { name: 'rewrite the checkout flow' }))
    expect(await tree().findByRole('button', { name: 'run.txt' })).toBeDefined()

    fireEvent.click(config)
    expect(tree().getByRole('button', { name: /^claude/ })).toBeDefined()
    expect(tree().getByRole('button', { name: 'New file in claude' })).toBeDefined()
  })

  it('does not strand a pending config tree when another run invalidates', async () => {
    seed(['config.roots'])
    const pending = Promise.withResolvers<Entries>()
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [{ harness: 'claude', path: '~/.claude', runtime_ignores: ['projects/'], credential_names: ['auth.json'] }],
      })),
      configTree: vi.fn(() => pending.promise),
    })
    render(<FilesRoute params={{}} client={client} />)
    fireEvent.click(await tree().findByRole('button', { name: /^Agent config/ }))
    fireEvent.click(tree().getByRole('button', { name: /^claude/ }))

    await waitFor(() => expect(client.configTree).toHaveBeenCalledWith({ harness: 'claude', path: '' }))
    act(() => useStore.getState().invalidateRun('unrelated-run'))

    pending.resolve({ entries: [file('settings.json')] })
    expect(await tree().findByRole('button', { name: 'settings.json' })).toBeDefined()
  })

  it('does not restore an invalidated run tree after its view unmounts', async () => {
    seed(['files.tree'])
    const pending = Promise.withResolvers<Entries>()
    const runTree = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue({ entries: [file('current.txt')] })
    const client = fakeApi({
      filesTree: vi.fn((params) => (params.run_id ? runTree() : Promise.resolve({ entries: [] }))),
    })
    const openCheckout = async () => {
      fireEvent.click(await tree().findByRole('button', { name: /^Run checkouts/ }))
      fireEvent.click(tree().getByRole('button', { name: 'rewrite the checkout flow' }))
    }
    const view = render(<FilesRoute params={{}} client={client} />)
    await openCheckout()
    await waitFor(() => expect(runTree).toHaveBeenCalledTimes(1))
    view.unmount()
    act(() => useStore.getState().invalidateRun('run_1'))
    await act(async () => {
      pending.resolve({ entries: [file('stale.txt')] })
      await pending.promise
    })
    render(<FilesRoute params={{}} client={client} />)
    await openCheckout()
    expect(await tree().findByRole('button', { name: 'current.txt' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'stale.txt' })).toBeNull()
  })
})

describe('Files editor', () => {
  beforeEach(() => seed(['files.tree']))

  function readmeApi() {
    return fakeApi({
      filesTree: vi.fn(async () => ({ entries: [file('README.md')] })),
      filesRead: vi.fn(async () => ({ content: '# project\n', truncated: false, binary: false, size: 10, revision: 'r1', writable: true })),
    })
  }

  async function openAndEdit(client: ReturnType<typeof readmeApi>, runID = '') {
    render(<FilesRoute params={{}} client={client} />)
    if (runID) {
      fireEvent.click(await tree().findByRole('button', { name: /^Run checkouts/ }))
      fireEvent.click(tree().getByRole('button', { name: 'rewrite the checkout flow' }))
      await waitFor(() => expect(tree().getAllByRole('button', { name: 'README.md' })).toHaveLength(2))
      fireEvent.click(tree().getAllByRole('button', { name: 'README.md' })[1])
    } else {
      fireEvent.click(await tree().findByRole('button', { name: 'README.md' }))
    }
    const key = filesKey(workspace.id, runID, 'README.md')
    await waitFor(() => expect(useStore.getState().documents[key]?.loading).toBe(false))
    act(() => useStore.getState().updateDraft(key, '# project\nedited\n'))
  }

  it('commits a base-branch file only after the dialog that says what a commit does', async () => {
    const client = readmeApi()
    await openAndEdit(client)

    fireEvent.click(screen.getByRole('button', { name: 'Commit to main…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Commit to main' }))
    expect(dialog.getByText(/it does not push upstream/)).toBeDefined()
    expect(client.filesWrite).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Commit' }))
    await waitFor(() =>
      expect(client.filesWrite).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        path: 'README.md',
        content: '# project\nedited\n',
        revision: 'r1',
      }),
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('closes an open file from its tab with Delete, and the tab list holds only tabs', async () => {
    const client = readmeApi()
    render(<FilesRoute params={{}} client={client} />)
    fireEvent.click(await tree().findByRole('button', { name: 'README.md' }))
    const tabs = await screen.findByRole('tablist', { name: 'Open files' })
    expect(within(tabs).queryAllByRole('button')).toHaveLength(0)

    fireEvent.keyDown(within(tabs).getByRole('tab', { name: /README\.md/ }), { key: 'Delete' })
    await waitFor(() => expect(screen.queryByRole('tablist', { name: 'Open files' })).toBeNull())
  })

  it('saves a run checkout file directly', async () => {
    const client = readmeApi()
    await openAndEdit(client, 'run_1')

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(client.filesWrite).toHaveBeenCalledWith(expect.objectContaining({ run_id: 'run_1', path: 'README.md' })),
    )
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByRole('tab', { name: 'Diff vs base' })).toBeDefined()
  })

  it('offers reload and discard when the server refuses a stale revision', async () => {
    const client = readmeApi()
    vi.mocked(client.filesWrite).mockRejectedValue(new Error('files.write: stale revision'))
    await openAndEdit(client, 'run_1')

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    const alert = within(await screen.findByRole('alert'))
    expect(alert.getByText('files.write: stale revision')).toBeDefined()
    expect(alert.getByRole('button', { name: 'Reload from server' })).toBeDefined()
    expect(alert.getByRole('button', { name: 'Discard edits' })).toBeDefined()
  })
})

describe('Files on a phone', () => {
  beforeEach(() => seed(['files.tree']))

  it('shows the tree until a file opens, then keeps it in a side sheet', async () => {
    atViewport(412, { pointer: 'coarse' })
    const client = fakeApi({ filesTree: vi.fn(async () => ({ entries: [file('README.md'), file('agent.sh')] })) })
    render(<FilesRoute params={{}} client={client} />)

    fireEvent.click(await tree().findByRole('button', { name: 'README.md' }))
    const viewer = await screen.findByRole('article')
    expect(within(viewer).getByText('README.md', { selector: 'p' })).toBeDefined()
    expect(screen.queryByRole('complementary', { name: 'Files' })).toBeNull()

    fireEvent.click(within(viewer).getByRole('button', { name: 'Browse' }))
    const sheet = within(await screen.findByRole('dialog', { name: 'Files' }))
    fireEvent.click(sheet.getByRole('button', { name: 'agent.sh' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Files' })).toBeNull())
    expect(within(screen.getByRole('article')).getByText('agent.sh', { selector: 'p' })).toBeDefined()
  })
})

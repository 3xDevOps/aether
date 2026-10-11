import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Markdown } from '@/components/ui/markdown'
import { OutputTail } from '@/components/ui/session-blocks'
import { api } from '@/lib/api'
import type { FileTreeEntry } from '@/lib/types'
import { openInFiles } from '@/routes/files/open'
import { RunFileLinks } from '@/routes/files/path-link'
import { RunFiles } from '@/routes/files/run-files'
import { useStore } from '@/store'
import { filesKey } from '@/store/files'
import { fakeApi, run, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

const file = (name: string): FileTreeEntry => ({ name, kind: 'file', size: 1 })
const dir = (name: string): FileTreeEntry => ({ name, kind: 'dir', size: 0 })

const listings: Record<string, FileTreeEntry[]> = {
  '': [dir('src'), file('README.md')],
  src: [file('app.ts')],
}
const filesTree = async ({ path }: { path: string }) => ({ entries: listings[path] ?? [] })
const source = 'one\ntwo\nthree\nfour\n'

function checkout(writable = true) {
  return fakeApi({
    filesTree: vi.fn(filesTree),
    filesRead: vi.fn(async () => ({ content: source, truncated: false, binary: false, size: source.length, revision: 'r1', writable })),
  })
}

beforeEach(() => {
  useStore.getState().resetFiles()
  useStore.setState({
    capabilities: { gateway: 'server', methods: ['files.tree'], ws: [] },
    workspaces: { [workspace.id]: workspace },
    runs: {},
    route: { name: 'run', params: { runId: 'run_1', view: 'files' } },
  })
  useStore.getState().setRuns([run()])
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('a run’s Files view', () => {
  it('lists the run’s checkout under its branch and opens a file beside the tree', async () => {
    const client = checkout()
    render(<RunFiles run={run()} shown client={client} />)
    const tree = within(screen.getByRole('complementary', { name: 'Files' }))
    expect(tree.getByRole('button', { name: 'aether/run-1-checkout' }).getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('No file open')).toBeDefined()

    fireEvent.click(await tree.findByRole('button', { name: 'README.md' }))
    expect(await screen.findByRole('textbox', { name: 'Edit README.md' })).toBeDefined()
    expect(client.filesRead).toHaveBeenCalledWith({ workspace_id: workspace.id, run_id: 'run_1', path: 'README.md' })
    expect(useStore.getState().route.name).toBe('run')
  })

  it('opens the file a link names, shows it in the tree and marks its line, again when the link is followed again', async () => {
    const client = checkout()
    act(() => openInFiles('run_1', { path: 'src/app.ts', line: 3 }))
    render(<RunFiles run={run()} shown client={client} />)
    await screen.findByRole('textbox', { name: 'Edit src/app.ts' })
    await waitFor(() => expect(document.querySelector('.cm-marked-line')?.textContent).toBe('three'))
    const row = await within(screen.getByRole('complementary', { name: 'Files' })).findByRole('button', { name: 'app.ts' })
    expect(row.getAttribute('aria-current')).toBe('true')

    act(() => openInFiles('run_1', { path: 'src/app.ts', line: 99 }))
    await waitFor(() => expect(document.querySelector('.cm-marked-line')?.textContent).toBe(''))
    expect(document.querySelectorAll('.cm-marked-line')).toHaveLength(1)
  })

  it('opens a file named at column zero on the first line', async () => {
    act(() => openInFiles('run_1', { path: 'src/app.ts', line: 1, column: 0 }))
    render(<RunFiles run={run()} shown client={checkout()} />)
    await waitFor(() => expect(document.querySelector('.cm-marked-line')?.textContent).toBe('one'))
  })

  it('returns to the file a run had selected after another run opened one', async () => {
    const client = checkout()
    act(() => openInFiles('run_1', { path: 'README.md' }))
    const { unmount } = render(<RunFiles run={run()} shown client={client} />)
    await screen.findByRole('textbox', { name: 'Edit README.md' })
    act(() => openInFiles('run_1', { path: 'src/app.ts' }))
    await screen.findByRole('textbox', { name: 'Edit src/app.ts' })
    fireEvent.click(await within(screen.getByRole('complementary', { name: 'Files' })).findByRole('button', { name: 'README.md' }))
    await screen.findByRole('textbox', { name: 'Edit README.md' })
    unmount()

    const other = run({ id: 'run_2' })
    act(() => useStore.getState().setRuns([run(), other]))
    act(() => openInFiles('run_2', { path: 'src/app.ts' }))
    render(<RunFiles run={other} shown client={client} />)
    await screen.findByRole('textbox', { name: 'Edit src/app.ts' })
    cleanup()

    render(<RunFiles run={run()} shown client={client} />)
    expect(await screen.findByRole('textbox', { name: 'Edit README.md' })).toBeDefined()
  })

  it('says a file the member may not change is read-only', async () => {
    act(() => openInFiles('run_1', { path: 'README.md' }))
    render(<RunFiles run={run()} shown client={checkout(false)} />)
    expect(await screen.findByText('Read-only')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
    expect(screen.getByRole('textbox', { name: 'Edit README.md' }).getAttribute('aria-readonly')).toBe('true')
  })

  it('keeps the editor and the tree up while a change to the checkout rereads them', async () => {
    const client = checkout()
    act(() => openInFiles('run_1', { path: 'README.md', line: 2 }))
    render(<RunFiles run={run()} shown client={client} />)
    const editor = await screen.findByRole('textbox', { name: 'Edit README.md' })
    const tree = within(screen.getByRole('complementary', { name: 'Files' }))
    await tree.findByRole('button', { name: 'README.md' })

    vi.mocked(client.filesRead).mockResolvedValue({ content: `zero\n${source}`, truncated: false, binary: false, size: 24, revision: 'r2', writable: true })
    act(() => useStore.getState().invalidateRun('run_1'))
    expect(screen.queryByText('Loading file…')).toBeNull()
    expect(screen.queryByText('Loading files…')).toBeNull()
    expect(tree.getByRole('button', { name: 'README.md' })).toBeDefined()

    const key = filesKey(workspace.id, 'run_1', 'README.md')
    await waitFor(() => expect(useStore.getState().documents[key]?.revision).toBe('r2'))
    expect(screen.getByRole('textbox', { name: 'Edit README.md' })).toBe(editor)
    await waitFor(() => expect(editor.textContent).toContain('zero'))
    expect(document.querySelector('.cm-marked-line')?.textContent).toBe('two')
  })

  it.each([
    ['on a phone', () => atViewport(390, { pointer: 'coarse' })],
    ['in a pane too narrow for both', () => vi.stubGlobal('ResizeObserver', class {
      constructor(private readonly report: ResizeObserverCallback) {}
      observe(target: Element) {
        this.report([{ target, contentRect: { width: 420, height: 700 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
      }
      disconnect() {}
    })],
  ])('shows the tree until a file is open %s, then the editor with Browse', async (_, narrow) => {
    narrow()
    render(<RunFiles run={run()} shown client={checkout()} />)
    fireEvent.click(await within(screen.getByRole('complementary', { name: 'Files' })).findByRole('button', { name: 'README.md' }))
    const viewer = await screen.findByRole('article')
    expect(screen.queryByRole('complementary', { name: 'Files' })).toBeNull()
    fireEvent.click(within(viewer).getByRole('button', { name: 'Browse' }))
    expect(await screen.findByRole('dialog', { name: 'Files' })).toBeDefined()
  })
})

describe('paths in a run’s Session', () => {
  function session(text: string) {
    return render(<RunFileLinks run={run()}><Markdown text={text} /></RunFileLinks>)
  }

  it('asks the server nothing until a path is pointed at, then links the one that is a file', async () => {
    const asked = vi.spyOn(api, 'filesTree').mockImplementation(filesTree)
    session('Fixed in `src/app.ts:3`; and/or see src/gone.ts.')
    expect(asked).not.toHaveBeenCalled()
    expect(screen.queryByRole('link')).toBeNull()

    fireEvent.pointerEnter(screen.getByText('src/app.ts:3'))
    const link = await screen.findByRole('link', { name: 'src/app.ts:3' })
    fireEvent.pointerEnter(screen.getByText('src/gone.ts'))
    fireEvent.pointerEnter(screen.getByText('and/or'))
    await waitFor(() => expect(asked).toHaveBeenCalledTimes(2))
    expect(asked).toHaveBeenCalledWith({ workspace_id: workspace.id, run_id: 'run_1', path: '' })
    expect(asked).toHaveBeenCalledWith({ workspace_id: workspace.id, run_id: 'run_1', path: 'src' })
    expect(screen.getAllByRole('link')).toEqual([link])
    // The agent's next edit drops the listing; the link the reader is on stays.
    act(() => useStore.getState().invalidateRun('run_1'))
    expect(screen.getAllByRole('link')).toEqual([link])

    fireEvent.click(link)
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1', view: 'files', file: 'src/app.ts', line: '3' } })
  })

  it('opens a path that is tapped before any listing was read, and not one that is no file', async () => {
    vi.spyOn(api, 'filesTree').mockImplementation(filesTree)
    session('See src/gone.ts and /workspace/src/app.ts:2:4.')
    const before = useStore.getState().route

    fireEvent.click(screen.getByText('src/gone.ts'))
    fireEvent.click(screen.getByText('/workspace/src/app.ts:2:4'))
    await waitFor(() => expect(useStore.getState().route.params).toMatchObject({ file: 'src/app.ts', line: '2', column: '4' }))
    expect(useStore.getState().route).not.toBe(before)
    expect(screen.getAllByRole('link').map((link) => link.textContent)).toEqual(['/workspace/src/app.ts:2:4'])
  })

  it('opens a Markdown link to a repository path in Files, and leaves a web link alone', () => {
    const asked = vi.spyOn(api, 'filesTree').mockImplementation(filesTree)
    session('[app](src/app.ts#L4) and [docs](https://example.com/docs)')
    expect(screen.getByRole('link', { name: 'docs' }).getAttribute('href')).toBe('https://example.com/docs')
    fireEvent.click(screen.getByRole('link', { name: 'app' }))
    expect(useStore.getState().route.params).toMatchObject({ view: 'files', file: 'src/app.ts', line: '4' })
    expect(asked).not.toHaveBeenCalled()
  })

  it('links paths in tool output from the listing the Files tree already read', () => {
    useStore.getState().setTree(filesKey(workspace.id, 'run_1', 'src'), { entries: listings.src! })
    render(<RunFileLinks run={run()}><OutputTail output={'src/app.ts:1:1: error\nsrc/other.ts:2: ok\n'} /></RunFileLinks>)
    expect(screen.getAllByRole('link').map((link) => link.textContent)).toEqual(['src/app.ts:1:1'])
  })

  it('is plain text for a run whose checkout is gone', () => {
    const asked = vi.spyOn(api, 'filesTree').mockImplementation(filesTree)
    render(<RunFileLinks run={run({ status: 'merged' })}><Markdown text="See `src/app.ts:3`." /></RunFileLinks>)
    fireEvent.pointerEnter(screen.getByText('src/app.ts:3'))
    fireEvent.click(screen.getByText('src/app.ts:3'))
    expect(asked).not.toHaveBeenCalled()
  })
})

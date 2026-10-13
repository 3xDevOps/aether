import { act, createEvent, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { FileRead, FileTreeEntry } from '@/lib/types'
import { FilesRoute } from '@/routes/files'
import { useStore } from '@/store'
import { homeKey } from '@/store/files'
import { fakeApi } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

const dir = (name: string): FileTreeEntry => ({ name, kind: 'dir', size: 0 })
const file = (name: string, locked = false): FileTreeEntry => ({ name, kind: 'file', size: 1, locked })

const listings: Record<string, FileTreeEntry[]> = {
  '': [dir('notes'), file('.netrc', true), { name: 'shortcut', kind: 'link', size: 9 }, file('todo.md')],
  notes: [file('plan.md')],
}

function homeApi(overrides: Parameters<typeof fakeApi>[0] = {}) {
  return fakeApi({
    homeTree: vi.fn(async (path: string) => ({ entries: listings[path] ?? [] })),
    homeRead: vi.fn(async () => ({ content: 'text\n', truncated: false, binary: false, size: 5, revision: 'r1', writable: true })),
    ...overrides,
  })
}

function tree() {
  return within(screen.getByRole('complementary', { name: 'Files' }))
}

const row = (name: string | RegExp) => tree().getByRole('button', { name })

async function openHome(client: ReturnType<typeof homeApi>) {
  render(<FilesRoute params={{}} client={client} />)
  fireEvent.click(await tree().findByRole('button', { name: /^Home ?~$/ }))
  await tree().findByRole('button', { name: 'todo.md' })
}

async function menu(label: string): Promise<string[]> {
  await userEvent.click(tree().getByRole('button', { name: label }))
  return (await screen.findAllByRole('menuitem')).map((item) => item.textContent ?? '')
}

const choose = (item: string) => userEvent.click(screen.getByRole('menuitem', { name: item }))

beforeEach(() => {
  useStore.getState().resetFiles()
  useStore.setState({ capabilities: { gateway: 'server', methods: ['home.tree'], ws: [] }, workspaces: {}, runs: {}, identityKey: null })
})

describe('Home tree', () => {
  it('lists the whole home under one root and gives each kind of row its own actions', async () => {
    const client = homeApi()
    await openHome(client)

    expect(client.homeTree).toHaveBeenCalledWith('')
    expect(row(/^\.netrc ?Credential file$/)).toBeDefined()
    expect(row(/^Symlink ?shortcut$/)).toBeDefined()

    expect(await menu('Home actions')).toEqual(['New file', 'New folder', 'Upload files…', 'Refresh', 'Copy path'])
    await userEvent.keyboard('{Escape}')
    expect(await menu('Actions for notes')).toEqual(['New file', 'New folder', 'Upload files…', 'Rename', 'Copy path', 'Delete'])
    await userEvent.keyboard('{Escape}')
    expect(await menu('Actions for todo.md')).toEqual(['Download', 'Rename', 'Copy path', 'Delete'])
    await userEvent.keyboard('{Escape}')
    expect(await menu('Actions for .netrc')).toEqual(['Copy path', 'Delete'])
    await userEvent.keyboard('{Escape}')
    expect(await menu('Actions for shortcut')).toEqual(['Rename', 'Copy path', 'Delete'])
    await userEvent.keyboard('{Escape}')

    fireEvent.contextMenu(row('todo.md'))
    expect(await screen.findByRole('menuitem', { name: 'Download' })).toBeDefined()
    await userEvent.keyboard('{Escape}')

    fireEvent.click(row(/^Symlink ?shortcut$/))
    expect(client.homeRead).not.toHaveBeenCalled()
    expect(screen.queryByRole('article')).toBeNull()
  })

  it('rereads every open folder on Refresh', async () => {
    const client = homeApi()
    await openHome(client)
    fireEvent.click(row('notes'))
    await tree().findByRole('button', { name: 'plan.md' })
    vi.mocked(client.homeTree).mockClear()

    fireEvent.click(tree().getByRole('button', { name: 'Refresh Home' }))
    await waitFor(() => expect(vi.mocked(client.homeTree).mock.calls.map(([path]) => path).sort()).toEqual(['', 'notes']))
  })

  it('downloads a file through the browser, saying so while it is fetched', async () => {
    const fetched = Promise.withResolvers<{ content_base64: string; size: number }>()
    const client = homeApi({ homeDownload: vi.fn(() => fetched.promise) })
    const saved: Array<{ name: string; bytes: number }> = []
    let blob = new Blob()
    Object.assign(URL, { createObjectURL: (made: Blob) => ((blob = made), 'blob:home'), revokeObjectURL: () => {} })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      saved.push({ name: this.download, bytes: blob.size })
    })
    onTestFinished(() => click.mockRestore())
    await openHome(client)

    await menu('Actions for todo.md')
    await choose('Download')
    expect((await tree().findByRole('status')).textContent).toBe('Downloading todo.md')
    fetched.resolve({ content_base64: btoa('hi\n'), size: 3 })
    await waitFor(() => expect(saved).toEqual([{ name: 'todo.md', bytes: 3 }]))
    expect(client.homeDownload).toHaveBeenCalledWith('todo.md')
    await waitFor(() => expect(tree().queryByRole('status')).toBeNull())
  })

  it('shows the server refusal when a credential file is opened', async () => {
    const refusal = 'home.read: .netrc is a credential file; read it in the Environment terminal'
    const client = homeApi({ homeRead: vi.fn(async () => Promise.reject(new ApiError(403, refusal))) })
    await openHome(client)

    fireEvent.click(row(/^\.netrc ?Credential file$/))
    expect((await screen.findByRole('alert')).textContent).toBe(refusal)
  })
})

describe('Home naming', () => {
  it('creates a file where it will sit and opens it', async () => {
    const client = homeApi()
    await openHome(client)

    await menu('Actions for notes')
    await choose('New file')
    const field = await tree().findByRole('textbox', { name: 'New file name' })
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(row('notes').getAttribute('aria-expanded')).toBe('true')

    await userEvent.type(field, 'a/b{Enter}')
    expect((await tree().findByRole('alert')).textContent).toBe('A name cannot contain /.')
    expect(client.homeWrite).not.toHaveBeenCalled()

    await userEvent.clear(field)
    await userEvent.type(field, 'ideas.md{Enter}')
    await waitFor(() => expect(client.homeWrite).toHaveBeenCalledWith({ path: 'notes/ideas.md', content: '', revision: '' }))
    expect(within(await screen.findByRole('article')).getByText('~/notes/ideas.md', { selector: 'p' })).toBeDefined()
    expect(tree().queryByRole('textbox')).toBeNull()
  })

  it('keeps the field open with the server refusal, and Escape gives the folder the keyboard back', async () => {
    const client = homeApi({ homeMkdir: vi.fn(async () => Promise.reject(new ApiError(409, 'home.mkdir: plans already exists'))) })
    await openHome(client)

    await menu('Home actions')
    await choose('New folder')
    const field = await tree().findByRole('textbox', { name: 'New folder name' })
    await userEvent.type(field, 'plans{Enter}')
    expect((await tree().findByRole('alert')).textContent).toBe('plans already exists')
    expect(client.homeMkdir).toHaveBeenCalledWith('plans')
    expect(tree().getByRole('textbox', { name: 'New folder name' })).toBeDefined()

    await userEvent.keyboard('{Escape}')
    expect(tree().queryByRole('textbox')).toBeNull()
    await waitFor(() => expect(document.activeElement).toBe(row(/^Home ?~$/)))
  })

  it('renames with F2, the stem selected, and the open tab follows with its draft', async () => {
    const client = homeApi()
    await openHome(client)
    fireEvent.click(row('todo.md'))
    await waitFor(() => expect(useStore.getState().documents[homeKey('todo.md')]?.loading).toBe(false))
    act(() => useStore.getState().updateDraft(homeKey('todo.md'), 'edited\n'))

    row('todo.md').focus()
    await userEvent.keyboard('{F2}')
    const field = (await tree().findByRole('textbox', { name: 'New name for todo.md' })) as HTMLInputElement
    expect([field.value, field.selectionStart, field.selectionEnd]).toEqual(['todo.md', 0, 4])

    listings[''] = [dir('notes'), file('done.md')]
    onTestFinished(() => {
      listings[''] = [dir('notes'), file('.netrc', true), { name: 'shortcut', kind: 'link', size: 9 }, file('todo.md')]
    })
    await userEvent.keyboard('done{Enter}')
    await waitFor(() => expect(client.homeRename).toHaveBeenCalledWith('todo.md', 'done.md'))
    await waitFor(() => expect(document.activeElement).toBe(row('done.md')))

    const state = useStore.getState()
    expect(state.fileTabs.map((tab) => tab.path)).toEqual(['done.md'])
    expect(state.activeFileKey).toBe(homeKey('done.md'))
    expect(state.drafts[homeKey('done.md')]?.content).toBe('edited\n')
    expect(state.documents[homeKey('todo.md')]).toBeUndefined()
    expect(client.homeRead).toHaveBeenCalledTimes(1)
  })

  it('settles a save on the file it was renamed to while the save was in flight', async () => {
    const written = Promise.withResolvers<FileRead>()
    const client = homeApi({ homeWrite: vi.fn(() => written.promise) })
    await openHome(client)
    fireEvent.click(row('todo.md'))
    await waitFor(() => expect(useStore.getState().documents[homeKey('todo.md')]?.loading).toBe(false))
    act(() => useStore.getState().updateDraft(homeKey('todo.md'), 'edited\n'))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.homeWrite).toHaveBeenCalledWith({ path: 'todo.md', content: 'edited\n', revision: 'r1' }))

    act(() => useStore.getState().moveHomePath('todo.md', 'done.md'))
    expect(useStore.getState().drafts[homeKey('done.md')]?.saving).toBe(true)
    written.resolve({ content: 'edited\n', truncated: false, binary: false, size: 7, revision: 'r2', writable: true })

    await waitFor(() => expect(useStore.getState().drafts).toEqual({}))
    const state = useStore.getState()
    expect(state.documents[homeKey('done.md')]).toMatchObject({ content: 'edited\n', revision: 'r2' })
    expect(state.documents[homeKey('todo.md')]).toBeUndefined()
  })
})

describe('Home on a phone', () => {
  it('names a row from the menu in the tree sheet, and Escape leaves the sheet open', async () => {
    atViewport(412, { pointer: 'coarse' })
    const client = homeApi()
    await openHome(client)
    fireEvent.click(row('todo.md'))
    fireEvent.click(within(await screen.findByRole('article')).getByRole('button', { name: 'Browse' }))
    const sheet = within(await screen.findByRole('dialog', { name: 'Files' }))

    await userEvent.click(sheet.getByRole('button', { name: 'Actions for notes' }))
    await choose('Rename')
    const field = await sheet.findByRole('textbox', { name: 'New name for notes' })
    await waitFor(() => expect(document.activeElement).toBe(field))

    await userEvent.keyboard('{Escape}')
    expect(sheet.queryByRole('textbox')).toBeNull()
    expect(screen.getByRole('dialog', { name: 'Files' })).toBeDefined()
  })
})

describe('Home delete', () => {
  it('asks first, says what is lost, then closes the tabs under the folder', async () => {
    const client = homeApi()
    vi.mocked(client.homeRemove).mockRejectedValueOnce(new ApiError(503, 'home.remove: permission denied'))
    await openHome(client)
    fireEvent.click(row('notes'))
    fireEvent.click(await tree().findByRole('button', { name: 'plan.md' }))
    await waitFor(() => expect(useStore.getState().documents[homeKey('notes/plan.md')]?.loading).toBe(false))
    act(() => useStore.getState().updateDraft(homeKey('notes/plan.md'), 'edited\n'))

    row('notes').focus()
    await userEvent.keyboard('{Delete}')
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Delete notes?' }))
    expect(dialog.getByText('~/notes')).toBeDefined()
    expect(dialog.getByText(/and everything in it will be deleted\./)).toBeDefined()
    expect(dialog.getByText('An open file in it has unsaved edits, which will be discarded.')).toBeDefined()
    expect(client.homeRemove).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Delete' }))
    expect((await dialog.findByRole('alert')).textContent).toBe('permission denied')
    expect(useStore.getState().fileTabs).toHaveLength(1)

    fireEvent.click(dialog.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(client.homeRemove).toHaveBeenLastCalledWith('notes')
    const state = useStore.getState()
    expect(state.fileTabs).toEqual([])
    expect(state.drafts[homeKey('notes/plan.md')]).toBeUndefined()
    expect(state.documents[homeKey('notes/plan.md')]).toBeUndefined()
  })
})

describe('Home upload', () => {
  const named = (name: string) => new File(['x'], name)

  it('sends picked files one at a time and asks once about the ones that exist', async () => {
    const first = Promise.withResolvers<unknown>()
    const big = named('big.bin')
    Object.defineProperty(big, 'size', { value: 64 * 2 ** 20 + 1 })
    const homeUpload = vi.fn(async (path: string, _content: string, overwrite: boolean) => {
      if (path === 'notes/a.txt') return first.promise
      if (path === 'notes/b.txt' && !overwrite) throw new ApiError(409, 'home.upload: notes/b.txt already exists')
      if (path === 'notes/c.txt') throw new ApiError(503, 'home.upload: no space left on device')
      return {}
    })
    const client = homeApi({ homeUpload })
    await openHome(client)

    await menu('Actions for notes')
    await choose('Upload files…')
    fireEvent.change(screen.getByLabelText('Files to upload'), {
      target: { files: [named('a.txt'), named('b.txt'), named('c.txt'), big] },
    })
    expect((await tree().findByRole('status')).textContent).toBe('Uploading 1 of 4 · a.txt')
    // The file is read before it is sent.
    await waitFor(() => expect(homeUpload).toHaveBeenCalledTimes(1))
    first.resolve({})

    const dialog = within(await screen.findByRole('alertdialog', { name: 'Replace 1 file?' }))
    expect(dialog.getByText('b.txt')).toBeDefined()
    expect(homeUpload.mock.calls.map(([path]) => path)).toEqual(['notes/a.txt', 'notes/b.txt', 'notes/c.txt'])

    fireEvent.click(dialog.getByRole('button', { name: 'Replace' }))
    await waitFor(() => expect(homeUpload).toHaveBeenLastCalledWith('notes/b.txt', 'eA==', true))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())

    const failures = within(tree().getByRole('alert'))
    expect(failures.getByText('2 files were not uploaded')).toBeDefined()
    expect(failures.getByText('notes/c.txt: no space left on device')).toBeDefined()
    expect(failures.getByText('notes/big.bin is over the 64 MiB limit')).toBeDefined()
    fireEvent.click(failures.getByRole('button', { name: 'Dismiss' }))
    expect(tree().queryByRole('alert')).toBeNull()
  })

  it('marks the folder a desktop drop would land in and keeps a dropped folder whole', async () => {
    const homeUpload = vi.fn(async () => ({}))
    const client = homeApi({ homeUpload })
    await openHome(client)
    fireEvent.click(row('notes'))
    const planned = await tree().findByRole('button', { name: 'plan.md' })

    const leaf = (name: string) => ({ isFile: true, isDirectory: false, name, file: (done: (file: File) => void) => done(named(name)) })
    const folder = (name: string, children: unknown[]) => ({
      isFile: false,
      isDirectory: true,
      name,
      createReader: () => {
        const batches = [children, []]
        return { readEntries: (done: (entries: unknown[]) => void) => done(batches.shift() ?? []) }
      },
    })
    const dataTransfer = {
      types: ['Files'],
      dropEffect: 'none',
      items: [
        ...[folder('pics', [leaf('a.png'), folder('raw', [leaf('b.png')])]), leaf('c.txt')].map((entry) => ({
          kind: 'file',
          webkitGetAsEntry: () => entry,
        })),
        { kind: 'file', webkitGetAsEntry: () => null, getAsFile: () => named('d.txt') },
        { kind: 'string', webkitGetAsEntry: () => null, getAsFile: () => null },
      ],
    }

    const over = createEvent.dragOver(planned, { dataTransfer })
    fireEvent(planned, over)
    expect(over.defaultPrevented).toBe(true)
    expect(planned.closest('[data-home-dir]')?.className).toContain('bg-accent-soft')

    fireEvent.drop(planned, { dataTransfer })
    await waitFor(() =>
      expect(homeUpload.mock.calls.map(([path]: unknown[]) => path)).toEqual(['notes/pics/a.png', 'notes/pics/raw/b.png', 'notes/c.txt', 'notes/d.txt']),
    )
    expect(planned.closest('[data-home-dir]')?.className).not.toContain('bg-accent-soft')
  })

  it('unfolds a folder the drag rests on', async () => {
    const client = homeApi()
    await openHome(client)

    fireEvent.dragOver(row('notes'), { dataTransfer: { types: ['Files'], dropEffect: 'none' } })
    expect(row('notes').getAttribute('aria-expanded')).toBe('false')
    expect(await tree().findByRole('button', { name: 'plan.md' }, { timeout: 2000 })).toBeDefined()
  })

  it('keeps a file dropped outside the tree from replacing the dashboard', async () => {
    await openHome(homeApi())
    const dataTransfer = { types: ['Files'], dropEffect: 'copy' }
    for (const event of [createEvent.dragOver(document.body, { dataTransfer }), createEvent.drop(document.body, { dataTransfer })]) {
      fireEvent(document.body, event)
      expect(event.defaultPrevented).toBe(true)
    }
    expect(dataTransfer.dropEffect).toBe('none')
  })
})

describe('Home move', () => {
  function drag() {
    const data: Record<string, string> = {}
    return {
      get types() {
        return Object.keys(data)
      },
      setData: (type: string, value: string) => {
        data[type] = value
      },
      effectAllowed: 'none',
      dropEffect: 'none',
    }
  }

  it('asks before moving a dragged row into a folder, and refuses its own folder', async () => {
    const client = homeApi()
    await openHome(client)
    const dataTransfer = drag()
    const [todo, notes] = [row('todo.md'), row('notes')]
    fireEvent.dragStart(todo, { dataTransfer })

    const home = createEvent.dragOver(row(/^Home ?~$/), { dataTransfer })
    fireEvent(row(/^Home ?~$/), home)
    expect(home.defaultPrevented).toBe(false)

    fireEvent.dragOver(notes, { dataTransfer })
    fireEvent.drop(notes, { dataTransfer })
    fireEvent.dragEnd(todo, { dataTransfer })
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Move todo.md to ~/notes?' }))
    expect(dialog.getByText('~/notes/todo.md')).toBeDefined()
    expect(client.homeRename).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Move' }))
    await waitFor(() => expect(client.homeRename).toHaveBeenCalledWith('todo.md', 'notes/todo.md'))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })
})

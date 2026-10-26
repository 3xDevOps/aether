import { useRef } from 'react'
import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { api } from '@/lib/api'
import type * as apiModule from '@/lib/api'
import { AttachmentChips } from '@/routes/run/composer-attachments'
import { AttachButton, DropOverlay, dropRoom } from '@/routes/run/composer-dropzone'
import { attachmentStates, useComposerImages } from '@/routes/run/composer-images'
import { composerDraft, saveComposerDraft, type ComposerImage } from '@/routes/run/composer-state'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

let runs = 0
let runID = ''
beforeEach(() => {
  runID = `run_attach_${++runs}`
  vi.clearAllMocks()
})

const png = (name = 'totals.png', size = 3) => new File([new Uint8Array(size)], name, { type: 'image/png' })
const text = (name = 'notes.txt', size = 5) => new File([new Uint8Array(size)], name, { type: 'text/plain' })
/** The chooser's answer; userEvent.upload would click the input, which counts as a click outside the open popover. */
const choose = (...files: File[]) => fireEvent.change(screen.getByLabelText('Choose files to attach'), { target: { files } })
const image = (id: number, path?: string): ComposerImage => ({ id, file: png(`f${id}.png`), path })

function Harness({ enabled = true, disabled = false, unsupported }: { enabled?: boolean; disabled?: boolean; unsupported?: string }) {
  const images = useComposerImages(runID, enabled)
  return (
    <>
      <AttachButton images={images} disabled={disabled} unsupported={unsupported} />
      <AttachmentChips images={images} disabled={false} />
    </>
  )
}

describe('attachmentStates', () => {
  it('marks the first unattached file as uploading and the rest as waiting', () => {
    expect(attachmentStates([image(1, '/a'), image(2), image(3)], true, undefined).map((s) => s.status)).toEqual(['attached', 'uploading', 'waiting'])
  })

  it('puts the failure on the first unattached file and leaves the rest unsent', () => {
    expect(attachmentStates([image(1, '/a'), image(2), image(3)], false, 'Upload failed: disk full')).toEqual([
      { status: 'attached' },
      { status: 'failed', reason: 'disk full' },
      { status: 'idle' },
    ])
  })

  it('does not blame a file for a refusal that is not an upload failure', () => {
    expect(attachmentStates([image(1)], false, 'Attach at most 8 files.')).toEqual([{ status: 'idle' }])
  })
})

describe('adding files', () => {
  it('refuses a file over its limit before it joins the draft and still sends the rest', async () => {
    const { result } = renderHook(() => useComposerImages(runID, true))
    const huge = new File([new Uint8Array(1)], 'huge.png', { type: 'image/png' })
    Object.defineProperty(huge, 'size', { value: 9 * 1024 * 1024 })

    await act(() => result.current.upload([huge, text()]))

    expect(result.current.previews.map((p) => p.file.name)).toEqual(['notes.txt'])
    expect(result.current.uploadError).toBe('huge.png is over the 8 MiB image limit.')
    expect(api.uploadTerminalImage).not.toHaveBeenCalled()
    expect(api.uploadTerminalFile).toHaveBeenCalledTimes(1)
  })

  it('does not count an oversized file against the room left for the rest', async () => {
    const { result } = renderHook(() => useComposerImages(runID, true))
    await act(() => result.current.upload(Array.from({ length: 7 }, (_, index) => text(`n${index}.txt`))))
    const huge = Object.defineProperty(text('huge.zip'), 'size', { value: 65 * 1024 * 1024 })

    await act(() => result.current.upload([huge, text('last.txt')]))

    expect(result.current.previews.map((p) => p.file.name)).toContain('last.txt')
    expect(result.current.previews).toHaveLength(8)
  })

  it('keeps nothing when every file is over its limit, and names the size rule for several', async () => {
    const { result } = renderHook(() => useComposerImages(runID, true))
    const big = (name: string) => Object.defineProperty(new File([], name, { type: 'application/zip' }), 'size', { value: 65 * 1024 * 1024 })

    await act(() => result.current.upload([big('a.zip'), big('b.zip')]))

    expect(result.current.previews).toEqual([])
    expect(result.current.uploadError).toBe('2 files are over the size limit. Images up to 8 MiB, other files up to 64 MiB.')
  })

  it('clears the draft only when nothing is uploading', async () => {
    const { result } = renderHook(() => useComposerImages(runID, true))
    await act(() => result.current.upload([text()]))
    saveComposerDraft(runID, { uploading: 1 })
    act(() => result.current.clear())
    expect(composerDraft(runID).images).toHaveLength(1)

    saveComposerDraft(runID, { uploading: undefined })
    act(() => result.current.clear())
    expect(composerDraft(runID).images).toEqual([])
  })
})

describe('the attach control', () => {
  it('opens the drop zone from the plus button and lists the files as they upload', async () => {
    let release = (_: { path: string }) => {}
    vi.mocked(api.uploadTerminalFile).mockReturnValueOnce(new Promise((resolve) => { release = resolve }))
    render(<Harness />)

    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const dialog = await screen.findByRole('dialog', { name: 'Attach files' })
    expect(within(dialog).getByText('Images up to 8 MiB, other files up to 64 MiB · at most 8')).toBeDefined()
    expect(within(dialog).getByText('No files yet')).toBeDefined()

    choose(text('notes.txt'), text('data.json', 2048))

    const list = within(dialog).getByRole('list', { name: 'Attachments' })
    expect(within(list).getByText('5 B · Uploading…')).toBeDefined()
    expect(within(list).getByText('2.0 KB · Waiting')).toBeDefined()
    expect(within(dialog).getAllByRole('status').map((el) => el.textContent)).toContain('Uploading 1 of 2 · 0 B of 2.0 KB')
    expect(within(dialog).getByRole('progressbar', { name: 'Uploading notes.txt' }).getAttribute('aria-valuenow')).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Remove notes.txt' })).toHaveProperty('disabled', true)
    expect(within(dialog).getByText('Uploading…')).toBeDefined()

    release({ path: '/home/alice/notes.txt' })
    expect(await within(dialog).findByText('2 files attached')).toBeDefined()
    expect(within(list).getAllByText(/· Attached/)).toHaveLength(2)
  })

  it('retries a failed file from its row and clears the draft from the footer', async () => {
    vi.mocked(api.uploadTerminalFile).mockRejectedValueOnce(new Error('terminal.file: no space left on device'))
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const dialog = await screen.findByRole('dialog')
    choose(text())

    expect(await within(dialog).findByText('terminal.file: no space left on device')).toBeDefined()
    expect(within(dialog).getByText('0 of 1 attached')).toBeDefined()

    await userEvent.click(within(dialog).getByRole('button', { name: 'Retry notes.txt' }))
    expect(await within(dialog).findByText('1 file attached')).toBeDefined()

    await userEvent.click(within(dialog).getByRole('button', { name: 'Clear' }))
    expect(within(dialog).getByText('No files yet')).toBeDefined()
    expect(screen.queryByRole('group', { name: 'Attached files' })).toBeNull()
  })

  it('says an image row is refused when the agent takes no images', async () => {
    render(<Harness unsupported="This agent does not support image prompts." />)
    await userEvent.upload(screen.getByLabelText('Choose files to attach'), png())
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('Agent takes no images')).toBeDefined()
    expect(within(dialog).getByText('This agent does not support image prompts.')).toBeDefined()
  })

  it('blocks the zone at eight files and while uploading', async () => {
    saveComposerDraft(runID, { images: Array.from({ length: 8 }, (_, i) => image(i, `/p${i}`)) })
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const zone = within(await screen.findByRole('dialog')).getByRole('button', { name: /At most 8 files/ })
    expect(zone).toHaveProperty('disabled', true)
    expect(screen.getByLabelText('Choose files to attach')).toHaveProperty('disabled', true)
  })

  it('opens the file chooser directly under a finger', async () => {
    atViewport(390, { pointer: 'coarse' })
    render(<Harness />)
    const click = vi.spyOn(screen.getByLabelText('Choose files to attach'), 'click')

    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))

    expect(click).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('shows a spinner on a chip while its file uploads and a dot when it failed', async () => {
    vi.mocked(api.uploadTerminalFile).mockRejectedValueOnce(new Error('nope'))
    render(<Harness />)
    await userEvent.upload(screen.getByLabelText('Choose files to attach'), text())
    expect(await screen.findByRole('img', { name: 'Not uploaded' })).toBeDefined()
  })
})

describe('dropping files on the zone', () => {
  const transfer = (...files: File[]) => ({ types: ['Files'], items: files.map(() => ({ kind: 'file' })), files })

  it('adds the dropped files and shows the count while the drag is over', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const zone = within(await screen.findByRole('dialog')).getByRole('button', { name: /Drag files here/ })

    fireEvent.dragEnter(zone, { dataTransfer: transfer(text(), text('b.txt')) })
    expect(within(zone).getByText('Drop to attach 2 files')).toBeDefined()
    expect(zone.hasAttribute('data-drag')).toBe(true)

    fireEvent.drop(zone, { dataTransfer: transfer(text(), text('b.txt')) })
    await waitFor(() => expect(api.uploadTerminalFile).toHaveBeenCalledTimes(2))
    expect(zone.hasAttribute('data-drag')).toBe(false)
  })

  it('ignores a drag that carries no files', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const zone = within(await screen.findByRole('dialog')).getByRole('button', { name: /Drag files here/ })

    fireEvent.dragEnter(zone, { dataTransfer: { types: ['text/plain'], items: [{ kind: 'string' }], files: [] } })

    expect(zone.hasAttribute('data-drag')).toBe(false)
  })

  it('refuses a drag of more files than fit', async () => {
    saveComposerDraft(runID, { images: Array.from({ length: 7 }, (_, i) => image(i, `/p${i}`)) })
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const zone = within(await screen.findByRole('dialog')).getByRole('button', { name: /Drag files here/ })

    fireEvent.dragEnter(zone, { dataTransfer: transfer(text(), text('b.txt')) })
    expect(within(zone).getByText('At most 1 file fits')).toBeDefined()
    fireEvent.drop(zone, { dataTransfer: transfer(text(), text('b.txt')) })
    expect(api.uploadTerminalFile).not.toHaveBeenCalled()
  })

  it('counts only the files in a drag that also carries a link and markup', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const zone = within(await screen.findByRole('dialog')).getByRole('button', { name: /Drag files here/ })

    fireEvent.dragEnter(zone, { dataTransfer: { types: ['Files', 'text/uri-list'], items: [{ kind: 'file' }, { kind: 'string' }, { kind: 'string' }], files: [text()] } })

    expect(within(zone).getByText('Drop to attach 1 file')).toBeDefined()
  })
})

describe('closing the attach popover', () => {
  function WithField() {
    const images = useComposerImages(runID, true)
    const field = useRef<HTMLTextAreaElement>(null)
    return (
      <>
        <textarea aria-label="Prompt" ref={field} />
        <input aria-label="Note" />
        <AttachButton images={images} disabled={false} textarea={field} />
      </>
    )
  }
  const open = async () => {
    render(<WithField />)
    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    await screen.findByRole('dialog')
    choose(text())
    await screen.findByText('1 file attached')
  }

  it('returns focus to the prompt when nothing else took it', async () => {
    await open()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Prompt')))
  })

  it('replays no row motion when the popover reopens, but enters a row added while open', async () => {
    await open()
    const dialog = screen.getByRole('dialog')
    expect(dialog.querySelector('li')?.className).toContain('animate-entrance')
    expect(dialog.querySelector('.animate-pop-in')).not.toBeNull()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    await userEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    const reopened = await screen.findByRole('dialog')
    expect(reopened.querySelector('li')?.className).not.toContain('animate-entrance')
    expect(reopened.querySelector('.animate-pop-in')).toBeNull()
  })

  it('leaves focus in the field the viewer clicked', async () => {
    await open()
    await userEvent.click(screen.getByLabelText('Note'))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).toBe(screen.getByLabelText('Note'))
  })
})

describe('dropRoom', () => {
  it('says why nothing can be added', () => {
    const base = { previews: [], uploading: false } as never
    expect(dropRoom(base, false)).toEqual({ limit: 8 })
    expect(dropRoom(base, true).limit).toBe(0)
    expect(dropRoom({ previews: [], uploading: true } as never, false)).toEqual({ limit: 0, blocked: 'Uploading…' })
    expect(dropRoom({ previews: Array(8), uploading: false } as never, false)).toEqual({ limit: 0, blocked: 'At most 8 files' })
  })
})

describe('the drop overlay', () => {
  it('names the files about to land, or why they cannot', () => {
    const { rerender } = render(<DropOverlay count={3} refused={false} limit={8} />)
    expect(screen.getByRole('status').textContent).toBe('Drop to attach 3 files')
    rerender(<DropOverlay count={3} refused limit={2} />)
    expect(screen.getByRole('status').textContent).toBe('At most 2 files fit')
    rerender(<DropOverlay count={1} refused={false} blocked="Uploading…" limit={0} />)
    expect(screen.getByRole('status').textContent).toBe('Uploading…')
  })
})

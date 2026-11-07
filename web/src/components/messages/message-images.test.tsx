import { fireEvent, render, screen, within } from '@testing-library/react'
import { MessageAttachments } from '@/components/messages/message-images'
import { api } from '@/lib/api'

beforeEach(() => {
  Object.assign(URL, { createObjectURL: () => 'blob:sent', revokeObjectURL: () => {} })
  vi.spyOn(api, 'roomImage').mockResolvedValue(new Blob(['png'], { type: 'image/png' }))
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** jsdom lays nothing out and decodes no image, so give the picture and its frame a size and fire its load. */
function load(dialog: HTMLElement, natural: [number, number], frame: [number, number] = [800, 600]) {
  const image = within(dialog).getByRole('img')
  Object.defineProperties(image, { naturalWidth: { value: natural[0] }, naturalHeight: { value: natural[1] } })
  Object.defineProperties(image.closest('.overflow-auto')!, { clientWidth: { value: frame[0] }, clientHeight: { value: frame[1] } })
  fireEvent.load(image)
}

async function open(name: string, images = [{ messageID: 'm1', index: 0 }, { messageID: 'm2', index: 0 }]) {
  render(<MessageAttachments runID="run_1" images={images} />)
  fireEvent.click(await screen.findByRole('button', { name: `Open ${name.toLowerCase()} full size` }))
  return screen.findByRole('dialog', { name })
}

test('names each image by its place in the row, so two messages never share a name', async () => {
  const dialog = await open('Image 2 of 2')
  expect(within(dialog).getByRole('img', { name: 'Image 2 of 2' }).getAttribute('src')).toBe('blob:sent')
  fireEvent.keyDown(dialog, { key: 'Escape' })
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('a lone image is the attached image', async () => {
  expect(await open('Attached image', [{ messageID: 'm1', index: 3 }])).toBeDefined()
  expect(api.roomImage).toHaveBeenCalledWith('run_1', 'm1', 3, expect.any(AbortSignal))
})

test('an image larger than the window switches between fit and actual size, from the image or the header', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  const toggle = within(dialog).getByRole('button', { name: 'Actual size' })
  expect(toggle.getAttribute('aria-pressed')).toBe('false')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Show image at actual size' }))
  expect(toggle.getAttribute('aria-pressed')).toBe('true')
  expect(within(dialog).getByRole('img').className).toContain('max-w-none')
  fireEvent.click(toggle)
  expect(within(dialog).getByRole('button', { name: 'Show image at actual size' })).toBeDefined()
})

test('an image that already fits offers no zoom', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [320, 180])
  expect(within(dialog).queryByRole('button', { name: 'Actual size' })).toBeNull()
  expect(within(dialog).queryByRole('button', { name: 'Show image at actual size' })).toBeNull()
  expect(within(dialog).getByRole('img', { name: 'Image 1 of 2' })).toBeDefined()
})

test('reopening an image starts fitted again', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  fireEvent.click(within(dialog).getByRole('button', { name: 'Actual size' }))
  fireEvent.keyDown(dialog, { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'Open image 1 of 2 full size' }))
  const reopened = await screen.findByRole('dialog')
  load(reopened, [2400, 1400])
  expect(within(reopened).getByRole('button', { name: 'Actual size' }).getAttribute('aria-pressed')).toBe('false')
})

test('an image that fails to load shows the error instead of a button', async () => {
  vi.spyOn(api, 'roomImage').mockRejectedValue(new Error('404 Not Found'))
  render(<MessageAttachments runID="run_1" images={[{ messageID: 'm1', index: 0 }]} />)
  expect((await screen.findByRole('alert')).textContent).toBe('Attached image could not be loaded: 404 Not Found')
  expect(screen.queryByRole('button')).toBeNull()
})

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

async function openImage(index = 1) {
  render(<MessageAttachments runID="run_1" images={[{ messageID: 'm1', index: 0 }, { messageID: 'm1', index: 1 }]} />)
  fireEvent.click(await screen.findByRole('button', { name: `View image ${index} full size` }))
  return screen.findByRole('dialog', { name: `Attached image ${index}` })
}

test('a sent image opens in a dialog and closes with Escape', async () => {
  const dialog = await openImage(2)
  expect(within(dialog).getByRole('img', { name: 'Attached image 2' }).getAttribute('src')).toBe('blob:sent')
  fireEvent.keyDown(dialog, { key: 'Escape' })
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(screen.getByRole('button', { name: 'View image 2 full size' })).toBeDefined()
})

test('clicking the open image switches between fit and actual size', async () => {
  const dialog = await openImage()
  const image = within(dialog).getByRole('img')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Show image at actual size' }))
  expect(image.className).toContain('max-w-none')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Fit image to window' }))
  expect(image.className).toContain('max-w-full')
})

test('reopening an image starts fitted again', async () => {
  const dialog = await openImage()
  fireEvent.click(within(dialog).getByRole('button', { name: 'Show image at actual size' }))
  fireEvent.keyDown(dialog, { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'View image 1 full size' }))
  expect(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Show image at actual size' })).toBeDefined()
})

test('an image that fails to load shows the error instead of a button', async () => {
  vi.spyOn(api, 'roomImage').mockRejectedValue(new Error('404 Not Found'))
  render(<MessageAttachments runID="run_1" images={[{ messageID: 'm1', index: 0 }]} />)
  expect((await screen.findByRole('alert')).textContent).toContain('Image 1 could not be loaded: 404 Not Found')
  expect(screen.queryByRole('button')).toBeNull()
})

import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MessageAttachments } from '@/components/messages/message-images'
import { api } from '@/lib/api'

beforeEach(() => {
  let made = 0
  Object.assign(URL, { createObjectURL: () => `blob:sent-${++made}`, revokeObjectURL: () => {} })
  vi.spyOn(api, 'roomImage').mockResolvedValue(new Blob(['png'], { type: 'image/png' }))
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** jsdom lays nothing out and decodes no image, so give the picture and its frame a size and fire its load. */
function load(dialog: HTMLElement, natural: [number, number], frame: [number, number] = [800, 600]) {
  const image = within(dialog).getByRole('img')
  Object.defineProperties(image, { naturalWidth: { value: natural[0] }, naturalHeight: { value: natural[1] } })
  Object.defineProperties(image.closest('.overflow-auto')!, {
    clientWidth: { value: frame[0], configurable: true },
    clientHeight: { value: frame[1], configurable: true },
  })
  fireEvent.load(image)
}

async function open(name: string, images = [{ messageID: 'm1', index: 0 }, { messageID: 'm2', index: 0 }]) {
  render(<MessageAttachments runID="run_1" images={images} />)
  fireEvent.click(await screen.findByRole('button', { name: `Open ${name.toLowerCase()} full size` }))
  return screen.findByRole('dialog', { name })
}

test('names each image by its place in the row, so two messages never share a name', async () => {
  const dialog = await open('Image 2 of 2')
  expect(within(dialog).getByRole('img', { name: 'Image 2 of 2' }).getAttribute('src')).toMatch(/^blob:sent-/)
  fireEvent.keyDown(dialog, { key: 'Escape' })
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('a lone image is the attached image', async () => {
  expect(await open('Attached image', [{ messageID: 'm1', index: 3 }])).toBeDefined()
  expect(api.roomImage).toHaveBeenCalledWith('run_1', 'm1', 3, expect.any(AbortSignal))
})

test('a thumbnail fits its natural aspect ratio inside the 320 by 192 pixel bounds', async () => {
  render(<MessageAttachments runID="run_1" images={[{ messageID: 'm1', index: 0 }]} />)
  const button = await screen.findByRole('button', { name: 'Open attached image full size' })
  const image = within(button).getByRole('img')
  Object.defineProperties(image, { naturalWidth: { value: 1800 }, naturalHeight: { value: 180 } })
  fireEvent.load(image)
  const frame = button.parentElement!
  expect(frame.style.width).toBe('320px')
  expect(frame.style.aspectRatio).toBe('1800 / 180')
  expect(frame.style.borderRadius).toBe('min(var(--radius-composer), 8px)')
  expect(frame.className).toContain('max-w-80')
})

const frameOf = (dialog: HTMLElement) => within(dialog).getByRole('group')
const width = (dialog: HTMLElement) => within(dialog).getByRole('img').style.width
/** The drawn width as a multiple of the fitted width: an 800x600 frame fits 2400x1400 at a third. */
const zoomOf = (dialog: HTMLElement) => parseFloat(width(dialog)) / 800

test('a click on an image larger than the window switches between fit and actual size', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  const image = within(dialog).getByRole('img')
  fireEvent.click(image)
  expect(frameOf(dialog).getAttribute('aria-label')).toBe('Image 1 of 2, zoomed to 100%')
  expect(width(dialog)).toBe('2400px')
  fireEvent.click(frameOf(dialog))
  expect(screen.getByRole('dialog')).toBe(dialog)
  fireEvent.click(within(dialog).getByRole('img'))
  expect(width(dialog)).toBe('800px')
})

test('clicking the outer gutters closes the viewer while zoomed frame clicks do not', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  fireEvent.click(within(dialog).getByRole('img'))
  fireEvent.click(frameOf(dialog))
  expect(screen.getByRole('dialog')).toBe(dialog)
  fireEvent.click(dialog)
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('the viewer hides its title and closes from its close button or beside a fitted picture', async () => {
  const dialog = await open('Image 1 of 2')
  const close = within(dialog).getByRole('button', { name: 'Close' })
  const next = await within(dialog).findByRole('button', { name: 'Next image' })
  expect(dialog.querySelector('[data-slot="dialog-title"]')?.className).toContain('sr-only')
  expect(close.className).toContain('size-11')
  expect(close.className).toContain('bg-raised')
  expect(next.className).toContain('coarse:opacity-100')
  expect(within(dialog).getAllByRole('button').map((button) => button.getAttribute('aria-label'))).toEqual(['Next image', 'Close'])
  fireEvent.click(close)
  expect(screen.queryByRole('dialog')).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Open image 1 of 2 full size' }))
  fireEvent.click(frameOf(await screen.findByRole('dialog')))
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('pointer buttons step through loaded images in both directions', async () => {
  const dialog = await open('Image 1 of 3', three)
  fireEvent.click(await within(dialog).findByRole('button', { name: 'Next image' }))
  const middle = await screen.findByRole('dialog', { name: 'Image 2 of 3' })
  expect(within(middle).getByRole('button', { name: 'Previous image' })).toBeDefined()
  fireEvent.click(within(middle).getByRole('button', { name: 'Next image' }))
  const last = await screen.findByRole('dialog', { name: 'Image 3 of 3' })
  expect(within(last).getAllByRole('button').map((button) => button.getAttribute('aria-label'))).toEqual(['Previous image', 'Close'])
  fireEvent.click(within(last).getByRole('button', { name: 'Previous image' }))
  expect(await screen.findByRole('dialog', { name: 'Image 2 of 3' })).toBeDefined()
})

test('Command or Control with plus, minus and zero zoom in steps from the fitted size, and back to fit', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  fireEvent.keyDown(dialog, { key: '=', metaKey: true })
  expect(zoomOf(dialog)).toBeCloseTo(1.25)
  fireEvent.keyDown(dialog, { key: '=', ctrlKey: true })
  fireEvent.keyDown(dialog, { key: '-', ctrlKey: true })
  expect(zoomOf(dialog)).toBeCloseTo(1.25)
  fireEvent.keyDown(dialog, { key: '0', metaKey: true })
  expect(width(dialog)).toBe('800px')
  for (let i = 0; i < 20; i++) fireEvent.keyDown(dialog, { key: '+', metaKey: true })
  expect(width(dialog)).toBe(`${2400 * 4}px`)
})

test('zooming out never goes below fitted', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  fireEvent.keyDown(dialog, { key: '-', ctrlKey: true })
  expect(width(dialog)).toBe('800px')
})

test('a trackpad pinch, a ctrl wheel, zooms the image and keeps the page from zooming', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  const pinch = new WheelEvent('wheel', { deltaY: -50, ctrlKey: true, bubbles: true, cancelable: true })
  frameOf(dialog).dispatchEvent(pinch)
  expect(pinch.defaultPrevented).toBe(true)
  await waitFor(() => expect(width(dialog)).not.toBe(''))
  const scroll = new WheelEvent('wheel', { deltaY: -50, bubbles: true, cancelable: true })
  frameOf(dialog).dispatchEvent(scroll)
  expect(scroll.defaultPrevented).toBe(false)
})

test('spreading two fingers zooms the image', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  const frame = frameOf(dialog)
  const fingers = (gap: number) => [{ clientX: 400 - gap / 2, clientY: 300 }, { clientX: 400 + gap / 2, clientY: 300 }]
  fireEvent.touchStart(frame, { touches: fingers(100), changedTouches: fingers(100) })
  fireEvent.touchMove(frame, { touches: fingers(200), changedTouches: fingers(200) })
  expect(zoomOf(dialog)).toBeCloseTo(2)
  fireEvent.touchEnd(frame, { touches: [], changedTouches: fingers(200) })
})

test('a small image scales up to fit the frame and can zoom further', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [320, 180])
  const image = within(dialog).getByRole('img')
  expect(image.style.width).toBe('800px')
  expect(image.style.height).toBe('450px')
  expect(frameOf(dialog).getAttribute('aria-label')).toBe('Image 1 of 2, fitted')
  fireEvent.keyDown(dialog, { key: '=', metaKey: true })
  expect(image.style.width).toBe('1000px')
  fireEvent.keyDown(dialog, { key: '0', metaKey: true })
  expect(image.style.width).toBe('800px')
  fireEvent.click(image)
  expect(image.style.width).toBe('1000px')
  expect(frameOf(dialog).getAttribute('aria-label')).toBe('Image 1 of 2, zoomed to 313%')
})

test('reopening an image starts fitted again', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  fireEvent.click(within(dialog).getByRole('img'))
  fireEvent.keyDown(dialog, { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'Open image 1 of 2 full size' }))
  const reopened = await screen.findByRole('dialog')
  load(reopened, [2400, 1400])
  expect(width(reopened)).toBe('800px')
})

test('an image that fails to load shows the error instead of a button', async () => {
  vi.spyOn(api, 'roomImage').mockRejectedValue(new Error('404 Not Found'))
  render(<MessageAttachments runID="run_1" images={[{ messageID: 'm1', index: 0 }]} />)
  expect((await screen.findByRole('alert')).textContent).toBe('Attached image could not be loaded: 404 Not Found')
  expect(screen.queryByRole('button')).toBeNull()
})

const three = [{ messageID: 'm1', index: 0 }, { messageID: 'm1', index: 1 }, { messageID: 'm1', index: 2 }]

test('the arrow keys step through the images and stop at either end', async () => {
  const dialog = await open('Image 2 of 3', three)
  fireEvent.keyDown(within(dialog).getByRole('img'), { key: 'ArrowRight' })
  expect(screen.getByRole('dialog', { name: 'Image 3 of 3' })).toBeDefined()
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'ArrowRight' })
  expect(screen.getByRole('dialog', { name: 'Image 3 of 3' })).toBeDefined()
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'ArrowLeft' })
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'ArrowLeft' })
  expect(screen.getByRole('dialog', { name: 'Image 1 of 3' })).toBeDefined()
})

test('zoomed in, the arrow keys scroll the picture instead of switching', async () => {
  const dialog = await open('Image 1 of 3', three)
  load(dialog, [2400, 1400])
  fireEvent.click(within(dialog).getByRole('img'))
  fireEvent.keyDown(dialog, { key: 'ArrowRight' })
  expect(screen.getByRole('dialog', { name: 'Image 1 of 3' })).toBeDefined()
})

test('a horizontal swipe switches images; a short or mostly vertical one does not', async () => {
  const dialog = await open('Image 1 of 3', three)
  const swipe = (dx: number, dy: number) => {
    const frame = within(screen.getByRole('dialog')).getByRole('img')
    fireEvent.touchStart(frame, { touches: [{ clientX: 200, clientY: 300 }], changedTouches: [{ clientX: 200, clientY: 300 }] })
    fireEvent.touchEnd(frame, { touches: [], changedTouches: [{ clientX: 200 + dx, clientY: 300 + dy }] })
  }
  swipe(-30, 0)
  swipe(-80, 60)
  expect(screen.getByRole('dialog', { name: 'Image 1 of 3' })).toBe(dialog)
  swipe(-120, 10)
  expect(screen.getByRole('dialog', { name: 'Image 2 of 3' })).toBeDefined()
  swipe(120, 0)
  expect(screen.getByRole('dialog', { name: 'Image 1 of 3' })).toBeDefined()
})

test('closing returns focus to the thumbnail of the image last shown', async () => {
  const dialog = await open('Image 1 of 3', three)
  fireEvent.keyDown(dialog, { key: 'ArrowRight' })
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open image 2 of 3 full size' })))
})

test('a modifier key with an arrow is left to the browser', async () => {
  const dialog = await open('Image 1 of 3', three)
  fireEvent.keyDown(dialog, { key: 'ArrowRight', altKey: true })
  expect(screen.getByRole('dialog', { name: 'Image 1 of 3' })).toBe(dialog)
})

test('the dialog closes, and stays closed, when the image it shows is reloaded', async () => {
  const images = [{ messageID: 'm1', index: 0 }, { messageID: 'm1', index: 1 }]
  const { rerender } = render(<MessageAttachments runID="run_1" images={images} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Open image 1 of 2 full size' }))
  expect(await screen.findByRole('dialog')).toBeDefined()
  rerender(<MessageAttachments runID="run_1" images={[]} />)
  expect(screen.queryByRole('dialog')).toBeNull()
  rerender(<MessageAttachments runID="run_1" images={images} />)
  await screen.findByRole('button', { name: 'Open image 1 of 2 full size' })
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('stepping keeps keyboard focus in the viewer, on the next image\'s frame', async () => {
  const dialog = await open('Image 1 of 3', three)
  const frame = frameOf(dialog)
  frame.focus()
  fireEvent.keyDown(frame, { key: 'ArrowRight' })
  const next = screen.getByRole('dialog', { name: 'Image 2 of 3' })
  await waitFor(() => expect(document.activeElement).toBe(frameOf(next)))
})

test('keyboard focus is shown around the image without outlining the viewer frame', async () => {
  const dialog = await open('Image 1 of 2')
  const frame = frameOf(dialog)
  const image = within(dialog).getByRole('img')
  expect(frame.className).toContain('outline-none')
  expect(image.className).toContain('group-focus-visible:outline-2')
})

test('keyboard focus remains visible on the frame while the image is zoomed', async () => {
  const dialog = await open('Image 1 of 2')
  const frame = frameOf(dialog)
  load(dialog, [2400, 1400])
  frame.focus()
  fireEvent.click(within(dialog).getByRole('img'))
  expect(width(dialog)).toBe('2400px')
  expect(frame.className).toContain('focus-visible:outline-2')
  expect(frame.className).toContain('focus-visible:outline-solid')
})

test('Control with the zero key fits again on any layout, by its key code', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  fireEvent.keyDown(dialog, { key: '=', ctrlKey: true })
  fireEvent.keyDown(dialog, { key: 'à', code: 'Digit0', ctrlKey: true })
  expect(width(dialog)).toBe('800px')
})

test('a Safari gesture during a two-finger touch pinch does not zoom a second time', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  const frame = frameOf(dialog)
  const fingers = (gap: number) => [{ clientX: 400 - gap / 2, clientY: 300 }, { clientX: 400 + gap / 2, clientY: 300 }]
  fireEvent.touchStart(frame, { touches: fingers(100), changedTouches: fingers(100) })
  frame.dispatchEvent(Object.assign(new Event('gesturestart', { cancelable: true }), { scale: 1 }))
  fireEvent.touchMove(frame, { touches: fingers(200), changedTouches: fingers(200) })
  act(() => {
    frame.dispatchEvent(Object.assign(new Event('gesturechange', { cancelable: true }), { scale: 2, clientX: 400, clientY: 300 }))
  })
  expect(zoomOf(dialog)).toBeCloseTo(2)
})

test('a Safari trackpad gesture zooms by its scale', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  const frame = frameOf(dialog)
  frame.dispatchEvent(Object.assign(new Event('gesturestart', { cancelable: true }), { scale: 1 }))
  const change = Object.assign(new Event('gesturechange', { cancelable: true }), { scale: 1.5, clientX: 400, clientY: 300 })
  frame.dispatchEvent(change)
  expect(change.defaultPrevented).toBe(true)
  await waitFor(() => expect(zoomOf(dialog)).toBeCloseTo(1.5))
})

test('the keypad 0 with Num Lock off is Insert, and Mod+Insert stays copy', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  fireEvent.keyDown(dialog, { key: '=', ctrlKey: true })
  const insert = fireEvent.keyDown(dialog, { key: 'Insert', code: 'Numpad0', ctrlKey: true })
  expect(insert).toBe(true)
  expect(zoomOf(dialog)).toBeCloseTo(1.25)
})

test('a click zooms to actual size with the clicked point kept under the pointer', async () => {
  const dialog = await open('Image 1 of 2')
  load(dialog, [2400, 1400])
  const frame = frameOf(dialog)
  const image = within(dialog).getByRole('img')
  const rect = (width: number, height: number, top = 0) => ({ left: 0, top, width, height }) as DOMRect
  frame.getBoundingClientRect = () => rect(800, 600)
  image.getBoundingClientRect = () => rect(parseFloat(image.style.width), parseFloat(image.style.height), image.style.width === '800px' ? 66.67 : 0)
  const scroll = { left: 0, top: 0 }
  Object.defineProperties(frame, {
    scrollLeft: { get: () => scroll.left, set: (v: number) => (scroll.left = v) },
    scrollTop: { get: () => scroll.top, set: (v: number) => (scroll.top = v) },
  })
  // Fitted, 2400x1400 draws 800x466.67, centred 66.67 from the top; this is its bottom-right quarter point.
  fireEvent.click(image, { clientX: 600, clientY: 66.67 + 466.67 * 0.75 })
  expect(scroll.left).toBeCloseTo(2400 * 0.75 - 600, 0)
  expect(scroll.top).toBeCloseTo(1400 * 0.75 - (66.67 + 466.67 * 0.75), 0)
})

test('the fitted image follows the frame as the window resizes', async () => {
  const observed: ResizeObserverCallback[] = []
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: ResizeObserverCallback) {
      observed.push(callback)
    }
    observe() {}
    disconnect() {}
  })
  const dialog = await open('Image 1 of 2')
  load(dialog, [1000, 500], [1200, 700])
  Object.defineProperties(frameOf(dialog), { clientWidth: { value: 600 }, clientHeight: { value: 400 } })
  act(() => observed.forEach((callback) => callback([], {} as ResizeObserver)))
  fireEvent.click(within(dialog).getByRole('img'))
  expect(width(dialog)).toBe('1000px')
  vi.unstubAllGlobals()
})

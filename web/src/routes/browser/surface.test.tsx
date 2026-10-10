import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import type { DevBrowserActionResult, DevBrowserFrameMetadata, DevBrowserPage, DevControlFence } from '@/lib/types'
import { StubSocket } from '@/test/stub-socket'
import { BrowserSurface, type BrowserSurfaceHandle } from './surface'

const page: DevBrowserPage = {
  session_id: 'browser-1', page_id: 'page-1', page_revision: 3, viewport_id: 'viewport-1',
  width: 390, height: 844, url: 'http://localhost:3000', title: 'App',
}

function frame(shown = page, sequence = 1): ArrayBuffer {
  const metadata: DevBrowserFrameMetadata = { ...shown, run_id: 'run_1', sequence, mime_type: 'image/png', timestamp: '2026-09-26T00:00:00Z' }
  const json = new TextEncoder().encode(JSON.stringify(metadata))
  const packet = new ArrayBuffer(8 + json.length + 1)
  const view = new DataView(packet)
  view.setUint32(0, json.length)
  view.setUint32(4, 1)
  new Uint8Array(packet, 8, json.length).set(json)
  return packet
}

function surfaceProps(control: DevControlFence | null = { control_session_id: 'tab', control_generation: 7 }) {
  return {
    runID: 'run_1', page, control, free: true, paused: false,
    acquire: vi.fn(async (): Promise<DevControlFence | null> => null),
    onBlocked: vi.fn(), onError: vi.fn(), onNavigated: vi.fn(), onPage: vi.fn(),
  }
}

async function stream(...frames: ArrayBuffer[]) {
  await act(async () => {
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    for (const data of frames) socket.onmessage?.({ data })
  })
}

const canvas = () => screen.getByLabelText('Shared browser page') as HTMLCanvasElement
const painted = () => waitFor(() => expect(canvas().getAttribute('aria-busy')).toBe('false'))

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('createImageBitmap', vi.fn(async () => ({ close: () => {} })))
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ clearRect: () => {}, drawImage: () => {} } as unknown as CanvasRenderingContext2D)
  vi.spyOn(api, 'devBrowserAction').mockResolvedValue({ page })
})
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('drops the old input backlog and ignores its late refusal after a new control generation', async () => {
  let reject!: (reason: Error) => void
  const pending = new Promise<DevBrowserActionResult>((_resolve, fail) => { reject = fail })
  vi.mocked(api.devBrowserAction).mockReturnValueOnce(pending)
  const props = surfaceProps()
  const view = render(<BrowserSurface {...props} />)
  await stream(frame())
  await painted()
  const input = screen.getByRole('textbox', { name: 'Remote browser keyboard' })
  fireEvent.keyDown(input, { key: 'a' })
  fireEvent.keyDown(input, { key: 'b' })
  expect(api.devBrowserAction).toHaveBeenCalledTimes(1)
  fireEvent.compositionStart(input)
  view.rerender(<BrowserSurface {...props} control={{ control_session_id: 'tab', control_generation: 8 }} />)
  fireEvent.compositionEnd(input, { data: '旧' })
  fireEvent.input(input, { inputType: 'insertFromComposition', data: '旧', target: { value: '\u200b旧' } })
  fireEvent.keyDown(input, { key: 'c' })
  await act(async () => { reject(new Error('Old controller was fenced')); await pending.catch(() => {}) })
  await waitFor(() => expect(api.devBrowserAction).toHaveBeenCalledTimes(2))
  expect(vi.mocked(api.devBrowserAction).mock.calls.map(([request]) => [request.key, request.control_generation])).toEqual([['a', 7], ['c', 8]])
  expect(props.onError).not.toHaveBeenCalled()
  fireEvent.compositionStart(input)
  fireEvent.compositionEnd(input, { data: '新' })
  fireEvent.input(input, { inputType: 'insertFromComposition', data: '新', target: { value: '\u200b新' } })
  await waitFor(() => expect(api.devBrowserAction).toHaveBeenCalledTimes(3))
  expect(api.devBrowserAction).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'text', text: '新', control_generation: 8 }))
  view.unmount()
})

it('takes a free lease on the first key and sends that key under it', async () => {
  const props = surfaceProps(null)
  props.acquire.mockResolvedValue({ control_session_id: 'tab', control_generation: 4 })
  const view = render(<BrowserSurface {...props} />)
  await stream(frame())
  await painted()
  const input = screen.getByRole('textbox', { name: 'Remote browser keyboard' })
  fireEvent.keyDown(input, { key: 'a' })
  fireEvent.keyDown(input, { key: 'b' })
  await waitFor(() => expect(api.devBrowserAction).toHaveBeenCalledTimes(2))
  expect(vi.mocked(api.devBrowserAction).mock.calls.map(([request]) => [request.key, request.control_session_id, request.control_generation])).toEqual([['a', 'tab', 4], ['b', 'tab', 4]])
  expect(props.onError).not.toHaveBeenCalled()
  view.unmount()
})

it('drops what was queued, without an error, when the lease goes to someone else first', async () => {
  const props = surfaceProps(null)
  const view = render(<BrowserSurface {...props} />)
  await stream(frame())
  await painted()
  fireEvent.keyDown(screen.getByRole('textbox', { name: 'Remote browser keyboard' }), { key: 'a' })
  await waitFor(() => expect(props.acquire).toHaveBeenCalledTimes(1))
  await act(async () => {})
  expect(api.devBrowserAction).not.toHaveBeenCalled()
  expect(props.onError).not.toHaveBeenCalled()
  view.unmount()
})

it('sends nothing while someone else drives, and says so on a press', async () => {
  const props = { ...surfaceProps(null), free: false }
  const view = render(<BrowserSurface {...props} />)
  await stream(frame())
  await painted()
  expect(screen.getByRole('textbox', { name: 'Remote browser keyboard' }).hasAttribute('disabled')).toBe(true)
  expect(canvas().tabIndex).toBe(-1)
  fireEvent.pointerMove(canvas(), { pointerId: 1 })
  expect(props.onBlocked).not.toHaveBeenCalled()
  fireEvent.pointerDown(canvas(), { pointerId: 1 })
  expect(props.onBlocked).toHaveBeenCalledTimes(1)
  expect(props.acquire).not.toHaveBeenCalled()
  expect(api.devBrowserAction).not.toHaveBeenCalled()
  view.unmount()
})

it('lets a wheel take the lease only for the focused window', async () => {
  vi.spyOn(HTMLCanvasElement.prototype, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, width: 390, height: 844 } as DOMRect)
  const windowFocused = vi.spyOn(document, 'hasFocus').mockReturnValue(false)
  const props = surfaceProps(null)
  props.acquire.mockResolvedValue({ control_session_id: 'tab', control_generation: 4 })
  const view = render(<BrowserSurface {...props} />)
  await stream(frame())
  await painted()
  fireEvent.wheel(canvas(), { clientX: 10, clientY: 10, deltaY: 120 })
  await act(async () => {})
  expect(props.acquire).not.toHaveBeenCalled()
  windowFocused.mockReturnValue(true)
  fireEvent.wheel(canvas(), { clientX: 10, clientY: 10, deltaY: 120 })
  await waitFor(() => expect(api.devBrowserAction).toHaveBeenCalledWith(expect.objectContaining({ action: 'scroll', x: 10, y: 10, delta_y: 120, control_generation: 4 })))
  view.unmount()
})

it('holds input while the toolbar changes the page', async () => {
  const props = { ...surfaceProps(), paused: true }
  const view = render(<BrowserSurface {...props} />)
  await stream(frame())
  await painted()
  fireEvent.keyDown(screen.getByRole('textbox', { name: 'Remote browser keyboard' }), { key: 'a' })
  await act(async () => {})
  expect(api.devBrowserAction).not.toHaveBeenCalled()
  expect(props.onError).not.toHaveBeenCalled()
  view.unmount()
})

it('is not idle, so the viewport is left alone, while text is being composed', async () => {
  const handle = createRef<BrowserSurfaceHandle>()
  const view = render(<BrowserSurface {...surfaceProps()} ref={handle} />)
  await stream(frame())
  await painted()
  const input = screen.getByRole('textbox', { name: 'Remote browser keyboard' })
  expect(handle.current!.idle()).toBe(true)
  fireEvent.compositionStart(input)
  expect(handle.current!.idle()).toBe(false)
  fireEvent.compositionEnd(input, { data: '語' })
  await waitFor(() => expect(handle.current!.idle()).toBe(true))
  expect(api.devBrowserAction).toHaveBeenCalledWith(expect.objectContaining({ action: 'text', text: '語' }))
  view.unmount()
})

it('reconnects a dropped stream by itself and keeps the painted frame', async () => {
  const props = surfaceProps()
  const view = render(<BrowserSurface {...props} />)
  await stream(frame())
  await painted()
  vi.useFakeTimers()
  act(() => StubSocket.last().onclose?.({ code: 1006 }))
  expect(canvas().width).toBe(390)
  expect(screen.queryByRole('alert')).toBeNull()
  expect(StubSocket.opened).toHaveLength(1)
  act(() => { vi.advanceTimersByTime(500) })
  expect(StubSocket.opened).toHaveLength(2)
  vi.useRealTimers()
  await stream(frame(page, 2))
  await painted()
  expect(JSON.parse(StubSocket.last().sent[0]!)).toMatchObject({ page_id: 'page-1', page_revision: 3 })
  expect(props.onError).not.toHaveBeenCalled()
  view.unmount()
})

it('shows the server’s refusal with Retry once reconnecting keeps failing', async () => {
  vi.useFakeTimers()
  const view = render(<BrowserSurface {...surfaceProps()} />)
  const refuse = () => act(() => {
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: false, error: 'browser: companion stream unavailable' }) })
  })
  refuse()
  for (const delay of [500, 1000, 2000, 4000, 8000]) {
    expect(screen.queryByRole('alert')).toBeNull()
    act(() => { vi.advanceTimersByTime(delay) })
    refuse()
  }
  expect(StubSocket.opened).toHaveLength(6)
  expect(screen.getByRole('alert').textContent).toContain('browser: companion stream unavailable')
  act(() => { vi.advanceTimersByTime(60_000) })
  expect(StubSocket.opened).toHaveLength(6)
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(StubSocket.opened).toHaveLength(7)
  expect(screen.queryByRole('alert')).toBeNull()
  view.unmount()
})

it('paints a frame that arrives before its new viewport is reported', async () => {
  const resized = { ...page, viewport_id: 'viewport-2', width: 1280, height: 800 }
  const props = surfaceProps(null)
  const view = render(<BrowserSurface {...props} />)
  await stream(frame(resized))
  expect(canvas().getAttribute('aria-busy')).toBe('true')
  view.rerender(<BrowserSurface {...props} page={resized} />)
  await painted()
  expect(canvas().width).toBe(1280)
  view.unmount()
})

it('leaves the painted frame alone when the page is re-reported with nothing parked', async () => {
  const resized = { ...page, viewport_id: 'viewport-2', width: 1280, height: 800 }
  const props = surfaceProps(null)
  const view = render(<BrowserSurface {...props} />)
  await stream(frame(resized, 1))
  view.rerender(<BrowserSurface {...props} page={resized} />)
  await act(async () => {
    StubSocket.last().onmessage?.({ data: frame({ ...resized, width: 1024, height: 640 }, 2) })
  })
  await waitFor(() => expect(canvas().width).toBe(1024))
  view.rerender(<BrowserSurface {...props} page={{ ...resized }} />)
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)) })
  expect(canvas().width).toBe(1024)
  view.unmount()
})

it('asks for a fresh read when a painted frame is of a newer revision', async () => {
  const props = surfaceProps(null)
  const view = render(<BrowserSurface {...props} />)
  await stream(frame({ ...page, page_revision: 4, viewport_id: 'viewport-9' }))
  await painted()
  expect(props.onNavigated).toHaveBeenCalledTimes(1)
  view.unmount()
})

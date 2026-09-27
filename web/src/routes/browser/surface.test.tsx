import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import type { DevBrowserActionResult, DevBrowserFrameMetadata, DevBrowserPage } from '@/lib/types'
import { StubSocket } from '@/test/stub-socket'
import { BrowserSurface } from './surface'

const page: DevBrowserPage = {
  session_id: 'browser-1', page_id: 'page-1', page_revision: 3, viewport_id: 'viewport-1',
  width: 390, height: 844, url: 'http://localhost:3000', title: 'App',
}

function frame(): ArrayBuffer {
  const metadata: DevBrowserFrameMetadata = { ...page, run_id: 'run_1', sequence: 1, mime_type: 'image/png', timestamp: '2026-09-26T00:00:00Z' }
  const json = new TextEncoder().encode(JSON.stringify(metadata))
  const packet = new ArrayBuffer(8 + json.length + 1)
  const view = new DataView(packet)
  view.setUint32(0, json.length)
  view.setUint32(4, 1)
  new Uint8Array(packet, 8, json.length).set(json)
  return packet
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('createImageBitmap', vi.fn(async () => ({ close: () => {} })))
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ clearRect: () => {}, drawImage: () => {} } as unknown as CanvasRenderingContext2D)
  vi.spyOn(api, 'devBrowserAction').mockResolvedValue({ page })
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('drops the old input backlog and ignores its late refusal after a new control generation', async () => {
  let reject!: (reason: Error) => void
  const pending = new Promise<DevBrowserActionResult>((_resolve, fail) => { reject = fail })
  vi.mocked(api.devBrowserAction).mockReturnValueOnce(pending)
  const props = { runID: 'run_1', page, control: { control_session_id: 'tab', control_generation: 7 }, connection: 0, expanded: false, onExpandedChange: vi.fn(), onError: vi.fn(), onPage: vi.fn() }
  const view = render(<BrowserSurface {...props} />)
  await act(async () => {
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    socket.onmessage?.({ data: frame() })
  })
  await screen.findByText('Live frame · 390 × 844')
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

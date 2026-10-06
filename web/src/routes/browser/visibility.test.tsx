import { renderHook, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import { useBrowserTab } from '@/routes/browser/visibility'

afterEach(() => vi.restoreAllMocks())

it('shows the Browser tab only once the run has a browser session', async () => {
  const status = vi.spyOn(api, 'devBrowserStatus').mockResolvedValue({ available: true, running: false, state: 'not_started' })
  const { result, rerender } = renderHook(({ id }) => useBrowserTab({ id, status: 'running' }, true), { initialProps: { id: 'run_1' } })
  await waitFor(() => expect(status).toHaveBeenCalledWith({ run_id: 'run_1' }))
  expect(result.current).toBe(false)

  status.mockResolvedValue({ available: true, running: true, state: 'running', session_id: 'browser-1' })
  rerender({ id: 'run_2' })
  await waitFor(() => expect(result.current).toBe(true))
})

it('never asks for a finished run or a gateway without the method', () => {
  const status = vi.spyOn(api, 'devBrowserStatus')
  expect(renderHook(() => useBrowserTab({ id: 'run_1', status: 'completed' }, true)).result.current).toBe(false)
  expect(renderHook(() => useBrowserTab({ id: 'run_1', status: 'running' }, false)).result.current).toBe(false)
  expect(status).not.toHaveBeenCalled()
})

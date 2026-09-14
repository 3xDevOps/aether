import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { InjectDialog } from '@/components/palette/inject-dialog'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { roomMessage, run } from '@/test/fixtures'

const injectMocks = vi.hoisted(() => ({
  runInject: vi.fn(),
}))

const toastMocks = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: injectMocks }))
vi.mock('sonner', () => ({ toast: toastMocks }))

beforeEach(() => {
  vi.clearAllMocks()
  injectMocks.runInject.mockResolvedValue({ message: roomMessage({ state: 'queued' }) })
  useStore.setState({
    paletteDialog: 'inject',
    paletteRunID: 'run_1',
    runs: { run_1: toRecord(run({ id: 'run_1' })) },
  })
})

describe('inject dialog idempotency', () => {
  it('reuses the caller key when the first response is lost', async () => {
    injectMocks.runInject
      .mockRejectedValueOnce(new Error('connection closed'))
      .mockResolvedValueOnce({ message: roomMessage({ state: 'queued' }) })
    render(<InjectDialog />)

    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'keep going' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(api.runInject).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(api.runInject).toHaveBeenCalledTimes(2))

    const first = vi.mocked(api.runInject).mock.calls[0]
    const retry = vi.mocked(api.runInject).mock.calls[1]
    expect(first).toEqual(['run_1', 'keep going', expect.any(String)])
    expect(retry).toEqual(first)
  })

  it('starts a new caller key after the payload changes', async () => {
    injectMocks.runInject
      .mockRejectedValueOnce(new Error('connection closed'))
      .mockResolvedValueOnce({ message: roomMessage({ state: 'queued' }) })
    render(<InjectDialog />)

    const message = screen.getByLabelText('Message')
    fireEvent.change(message, { target: { value: 'keep going' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(api.runInject).toHaveBeenCalledTimes(1))
    fireEvent.change(message, { target: { value: 'stop now' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(api.runInject).toHaveBeenCalledTimes(2))

    const first = vi.mocked(api.runInject).mock.calls[0]
    const changed = vi.mocked(api.runInject).mock.calls[1]
    expect(first[2]).not.toBe(changed[2])
    expect(changed).toEqual(['run_1', 'stop now', expect.any(String)])
  })
})

describe('inject dialog receipts', () => {
  it.each([
    ['queued', 'success', 'queued'],
    ['sent', 'success', 'Sent'],
    ['not_sent', 'error', 'Not sent'],
    ['uncertain', 'error', 'Delivery uncertain'],
  ] as const)('reports a %s legacy injection receipt', async (state, toastKind, label) => {
    injectMocks.runInject.mockResolvedValue({
      message: roomMessage({ state }),
      ...(state === 'queued' ? {} : { receipt: state }),
    })
    render(<InjectDialog />)

    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'report this result' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(injectMocks.runInject).toHaveBeenCalledTimes(1))

    expect(toastMocks[toastKind]).toHaveBeenCalledWith(label)
  })
})

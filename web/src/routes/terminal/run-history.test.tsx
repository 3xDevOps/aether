import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import { RunHistory } from '@/routes/terminal/run-history'

vi.mock('@/lib/api', () => ({
  api: { runRecording: vi.fn() },
}))

describe('RunHistory', () => {
  it('aborts the finite recording request when returning to the live terminal', async () => {
    let resolveRecording: (cast: string) => void = () => {}
    const pending = new Promise<string>((resolve) => {
      resolveRecording = resolve
    })
    vi.mocked(api.runRecording).mockReturnValue(pending)

    render(<RunHistory runID="run_1" />)
    fireEvent.click(screen.getByRole('button', { name: 'Open complete terminal history' }))

    await screen.findByRole('dialog')
    await waitFor(() => expect(api.runRecording).toHaveBeenCalledTimes(1))
    const signal = vi.mocked(api.runRecording).mock.calls[0][1]
    expect(signal?.aborted).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Back to live' }))
    expect(signal?.aborted).toBe(true)

    resolveRecording('')
    await Promise.resolve()
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

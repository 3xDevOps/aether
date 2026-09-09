import { act, fireEvent, render, screen } from '@testing-library/react'
import { MissingRun } from '@/components/missing-run'
import { useStore } from '@/store'

const gone = 'This run is not on the server. It may have been deleted.'

describe('missing run', () => {
  it('shows nothing at all until the load has run past the delay', () => {
    vi.useFakeTimers()
    try {
      useStore.setState({ hydrated: false, hydrationError: null, streamDead: false })
      render(<MissingRun />)

      expect(screen.queryByRole('status')).toBeNull()
      expect(document.querySelectorAll('[data-slot="skeleton"]')).toHaveLength(0)
      expect(screen.queryByText(gone)).toBeNull()

      // useDelayed flips at 200ms, which is what keeps the skeleton from
      // flashing on a load that was never slow.
      act(() => vi.advanceTimersByTime(200))

      expect(screen.getByRole('status', { name: 'Loading the run' })).toBeDefined()
      expect(screen.queryByText(gone)).toBeNull()
      expect(screen.queryByRole('button', { name: 'Back to board' })).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('reports an unreachable server instead of claiming the run is gone', () => {
    useStore.setState({
      hydrated: false,
      hydrationError: 'server unreachable: connection refused',
      streamDead: false,
    })
    render(<MissingRun />)

    expect(screen.getByText('Cannot reach the server. Retrying.')).toBeDefined()
    expect(screen.queryByText(gone)).toBeNull()
    expect(document.querySelectorAll('[data-slot="skeleton"]')).toHaveLength(0)
  })

  it('says what the error recorded when nothing is retrying', () => {
    useStore.setState({
      hydrated: false,
      hydrationError: 'invite token rejected',
      streamDead: true,
    })
    render(<MissingRun />)

    expect(screen.getByText('invite token rejected')).toBeDefined()
    expect(screen.queryByText('Cannot reach the server. Retrying.')).toBeNull()
  })

  it('says the run is gone and takes the reader back to the board', () => {
    useStore.setState({
      hydrated: true,
      hydrationError: null,
      streamDead: false,
      route: { name: 'terminal', params: { runId: 'run_1' } },
    })
    render(<MissingRun />)

    expect(screen.getByText(gone)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Back to board' }))

    expect(useStore.getState().route.name).toBe('board')
  })
})

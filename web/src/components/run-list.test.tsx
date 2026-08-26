import { render, screen } from '@testing-library/react'
import { RunList } from '@/components/run-list'
import { useStore } from '@/store'

describe('run list empty states', () => {
  it('claims a retry while one is actually coming', () => {
    useStore.setState({
      hydrated: false,
      hydrationError: 'fetch failed',
      streamDead: false,
    })
    render(<RunList runs={[]} empty="No runs yet" />)
    expect(screen.getByText('Cannot reach the server. Retrying.')).toBeDefined()
  })

  it('names the dead token once the stream has stopped for good', () => {
    useStore.setState({
      hydrated: false,
      hydrationError:
        'dashboard token revoked or expired; mint one with `aether gui`',
      streamDead: true,
    })
    render(<RunList runs={[]} empty="No runs yet" />)
    // Nothing retries a dead token; saying so is what points at the fix.
    expect(screen.getByText(/aether gui/)).toBeDefined()
    expect(screen.queryByText(/Retrying/)).toBeNull()
  })
})

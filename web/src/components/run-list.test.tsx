import { fireEvent, render, screen, within } from '@testing-library/react'
import { RunList } from '@/components/run-list'
import { listedRuns } from '@/store/selectors'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run, runRecords, stateContext } from '@/test/fixtures'

describe('run list', () => {
  it('marks a working row and opens it', () => {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false })
    const listed = toRecord(run())
    const rows = listedRuns('', stateContext({ runs: runRecords(run()) }))
    const { container } = render(<RunList runs={rows} empty="No runs yet" />)

    expect(within(container).getByRole('img', { name: 'Working' })).toBeDefined()

    fireEvent.click(screen.getByText('rewrite the checkout flow'))

    expect(useStore.getState().route).toEqual({
      name: 'run',
      params: { runId: listed.id },
    })
  })
})

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
        'a valid gateway token is required; restart `aether gui` for a fresh URL',
      streamDead: true,
    })
    render(<RunList runs={[]} empty="No runs yet" />)
    expect(screen.getByText(/aether gui/)).toBeDefined()
    expect(screen.queryByText(/Retrying/)).toBeNull()
  })

  it('gives a taskless TUI run a placeholder title', () => {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false })
    render(
      <RunList
        runs={listedRuns('', stateContext({ runs: runRecords(run({ task: '' })) }))}
        empty="No runs yet"
      />,
    )
    expect(screen.getByText('Untitled run')).toBeDefined()
  })
})

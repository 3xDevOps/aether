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
    const { container } = render(<RunList runs={rows} empty="No sessions yet" />)

    expect(within(container).getByRole('img', { name: 'Working' })).toBeDefined()

    fireEvent.click(screen.getByText('rewrite the checkout flow'))

    expect(useStore.getState().route).toEqual({
      name: 'run',
      params: { runId: listed.id },
    })
  })
})

describe('run list child runs', () => {
  const family = [
    run({ id: 'run_parent', task: 'integrate' }),
    run({ id: 'run_c1', task: 'child one', parent_run_id: 'run_parent' }),
    run({ id: 'run_c2', task: 'child two', parent_run_id: 'run_parent' }),
    run({ id: 'run_solo', task: 'unrelated' }),
    run({ id: 'run_orphan', task: 'orphan child', parent_run_id: 'run_gone' }),
  ]

  function renderFamily() {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false, route: { name: 'overview', params: {} } })
    const rows = listedRuns('', stateContext({ runs: runRecords(...family) }))
    return render(<RunList runs={rows} empty="No sessions yet" />)
  }

  it('nests child runs under their parent and keeps every other run top-level', () => {
    renderFamily()
    const children = screen.getByRole('list', { name: 'Subsessions of integrate' })

    expect(within(children).getByText('child one')).toBeDefined()
    expect(within(children).getByText('child two')).toBeDefined()
    expect(within(children).queryByText('orphan child')).toBeNull()
    expect(within(children).queryByText('unrelated')).toBeNull()
    for (const task of ['integrate', 'child one', 'child two', 'unrelated', 'orphan child']) {
      expect(screen.getByText(task)).toBeDefined()
    }
  })

  it('collapses and expands the child runs of one parent', () => {
    renderFamily()
    fireEvent.click(screen.getByRole('button', { name: 'Hide 2 subsessions' }))

    expect(screen.queryByRole('list', { name: 'Subsessions of integrate' })).toBeNull()
    expect(screen.getByText('orphan child')).toBeDefined()
    expect(useStore.getState().route.name).not.toBe('run')

    fireEvent.click(screen.getByRole('button', { name: 'Show 2 subsessions' }))
    expect(screen.getByRole('list', { name: 'Subsessions of integrate' })).toBeDefined()
  })

  it('opens a nested child run', () => {
    renderFamily()
    fireEvent.click(screen.getByText('child two'))

    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_c2' } })
  })
})

describe('run list family ordering', () => {
  const runs = [
    run({ id: 'run_solo', task: 'solo', created_at: '2026-08-14T10:00:00Z' }),
    run({ id: 'run_parent', task: 'integrate', created_at: '2026-08-14T10:01:00Z' }),
    run({ id: 'run_ask', task: 'asking child', parent_run_id: 'run_parent', mode: 'headless', status: 'needs-attention', state: 'needs-you', reason: 'blocked: no database access', created_at: '2026-08-14T10:02:00Z' }),
    run({ id: 'run_busy', task: 'busy child', parent_run_id: 'run_parent', created_at: '2026-08-14T10:03:00Z' }),
  ]

  function renderAsking() {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false, route: { name: 'overview', params: {} } })
    const ctx = stateContext({ runs: runRecords(...runs) })
    return render(<RunList runs={listedRuns('', ctx)} empty="No sessions yet" />)
  }

  it('places a tree where its earliest member sits', () => {
    renderAsking()
    const top = screen.getAllByRole('listitem').filter((item) => item.parentElement?.getAttribute('aria-label') === null)

    expect(within(top[0]).getByText('integrate')).toBeDefined()
    expect(within(top[1]).getByText('solo')).toBeDefined()
  })

  it('keeps a child run that needs you visible when its parent is folded', () => {
    renderAsking()
    fireEvent.click(screen.getByRole('button', { name: 'Hide 1 subsession' }))

    expect(screen.getByText('asking child')).toBeDefined()
    expect(screen.queryByText('busy child')).toBeNull()
    expect(screen.getByRole('button', { name: 'Show 1 subsession' }).getAttribute('aria-expanded')).toBe('false')
  })

  it('offers no fold when every child run needs you', () => {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false, route: { name: 'overview', params: {} } })
    const asking = runs.filter((item) => item.id !== 'run_busy')
    render(<RunList runs={listedRuns('', stateContext({ runs: runRecords(...asking) }))} empty="No sessions yet" />)

    expect(screen.getByText('asking child')).toBeDefined()
    expect(screen.queryByRole('button', { name: /^(Hide|Show) / })).toBeNull()
  })
})

describe('run list empty states', () => {
  it('claims a retry while one is actually coming', () => {
    useStore.setState({
      hydrated: false,
      hydrationError: 'fetch failed',
      streamDead: false,
    })
    render(<RunList runs={[]} empty="No sessions yet" />)
    expect(screen.getByText('Cannot reach the server. Retrying.')).toBeDefined()
  })

  it('names the dead token once the stream has stopped for good', () => {
    useStore.setState({
      hydrated: false,
      hydrationError:
        'a valid gateway token is required; restart `aether gui` for a fresh URL',
      streamDead: true,
    })
    render(<RunList runs={[]} empty="No sessions yet" />)
    expect(screen.getByText(/aether gui/)).toBeDefined()
    expect(screen.queryByText(/Retrying/)).toBeNull()
  })

  it('gives a taskless TUI run a placeholder title', () => {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false })
    render(
      <RunList
        runs={listedRuns('', stateContext({ runs: runRecords(run({ task: '' })) }))}
        empty="No sessions yet"
      />,
    )
    expect(screen.getByText('Untitled session')).toBeDefined()
  })
})

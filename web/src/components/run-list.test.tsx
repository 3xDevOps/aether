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

describe('run list swarms', () => {
  const swarm = [
    run({ id: 'run_int', task: 'integrate', mission_id: 'm1', mission_role: 'integrator' }),
    run({ id: 'run_w1', task: 'worker one', mission_id: 'm1', mission_role: 'worker', integrator_run_id: 'run_int' }),
    run({ id: 'run_w2', task: 'worker two', mission_id: 'm1', mission_role: 'worker', integrator_run_id: 'run_int' }),
    run({ id: 'run_solo', task: 'unrelated' }),
    run({ id: 'run_orphan', task: 'orphan worker', mission_id: 'm2', mission_role: 'worker', integrator_run_id: 'run_gone' }),
  ]

  function renderSwarm() {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false, route: { name: 'overview', params: {} } })
    const rows = listedRuns('', stateContext({ runs: runRecords(...swarm) }))
    return render(<RunList runs={rows} empty="No runs yet" />)
  }

  it('nests workers under their integrator and keeps every other run top-level', () => {
    renderSwarm()
    const workers = screen.getByRole('list', { name: 'Workers of integrate' })

    expect(within(workers).getByText('worker one')).toBeDefined()
    expect(within(workers).getByText('worker two')).toBeDefined()
    expect(within(workers).queryByText('orphan worker')).toBeNull()
    expect(within(workers).queryByText('unrelated')).toBeNull()
    for (const task of ['integrate', 'worker one', 'worker two', 'unrelated', 'orphan worker']) {
      expect(screen.getByText(task)).toBeDefined()
    }
  })

  it('collapses and expands the workers of one integrator', () => {
    renderSwarm()
    fireEvent.click(screen.getByRole('button', { name: 'Hide 2 workers' }))

    expect(screen.queryByRole('list', { name: 'Workers of integrate' })).toBeNull()
    expect(screen.getByText('orphan worker')).toBeDefined()
    expect(useStore.getState().route.name).not.toBe('run')

    fireEvent.click(screen.getByRole('button', { name: 'Show 2 workers' }))
    expect(screen.getByRole('list', { name: 'Workers of integrate' })).toBeDefined()
  })

  it('opens a nested worker run', () => {
    renderSwarm()
    fireEvent.click(screen.getByText('worker two'))

    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_w2' } })
  })
})

describe('run list swarm ordering', () => {
  const runs = [
    run({ id: 'run_solo', task: 'solo', created_at: '2026-08-14T10:00:00Z' }),
    run({ id: 'run_int', task: 'integrate', mission_id: 'm1', mission_role: 'integrator', created_at: '2026-08-14T10:01:00Z' }),
    run({ id: 'run_ask', task: 'asking worker', mission_id: 'm1', mission_role: 'worker', integrator_run_id: 'run_int', mode: 'headless', status: 'needs-attention', activity: 'needs-you', reason: 'blocked: no database access', created_at: '2026-08-14T10:02:00Z' }),
    run({ id: 'run_busy', task: 'busy worker', mission_id: 'm1', mission_role: 'worker', integrator_run_id: 'run_int', created_at: '2026-08-14T10:03:00Z' }),
  ]

  function renderAsking() {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false, route: { name: 'overview', params: {} } })
    const ctx = stateContext({ runs: runRecords(...runs) })
    return render(<RunList runs={listedRuns('', ctx)} empty="No runs yet" />)
  }

  it('places a tree where its earliest member sits', () => {
    renderAsking()
    const top = screen.getAllByRole('listitem').filter((item) => item.parentElement?.getAttribute('aria-label') === null)

    expect(within(top[0]).getByText('integrate')).toBeDefined()
    expect(within(top[1]).getByText('solo')).toBeDefined()
  })

  it('keeps a worker that needs you visible when its integrator is folded', () => {
    renderAsking()
    fireEvent.click(screen.getByRole('button', { name: 'Hide 1 worker' }))

    expect(screen.getByText('asking worker')).toBeDefined()
    expect(screen.queryByText('busy worker')).toBeNull()
    expect(screen.getByRole('button', { name: 'Show 1 worker' }).getAttribute('aria-expanded')).toBe('false')
  })

  it('offers no fold when every worker needs you', () => {
    useStore.setState({ hydrated: true, hydrationError: null, streamDead: false, route: { name: 'overview', params: {} } })
    const asking = runs.filter((item) => item.id !== 'run_busy')
    render(<RunList runs={listedRuns('', stateContext({ runs: runRecords(...asking) }))} empty="No runs yet" />)

    expect(screen.getByText('asking worker')).toBeDefined()
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

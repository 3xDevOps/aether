import { act, fireEvent, render, screen } from '@testing-library/react'
import { useEffect } from 'react'
import type { ComponentType } from 'react'
import type { RouteProps } from '@/routes/registry'
import { toRecord } from '@/store/runs'
import { useStore } from '@/store'
import { run } from '@/test/fixtures'

const routeRegistry = vi.hoisted(() => ({
  current: {} as Record<string, ComponentType<RouteProps>>,
}))

vi.mock('@/routes', () => ({
  lookupRoute: (name: string) => routeRegistry.current[name],
}))

import { CenterView } from '@/components/shell/center-view'

const mounts: Record<string, number> = {}
const weights: Record<string, number> = {}

function StubTerminal(props: RouteProps) {
  const runID = props.params.runId ?? ''
  useEffect(() => {
    mounts[runID] = (mounts[runID] ?? 0) + 1
  }, [runID])
  useEffect(() => {
    if (props.active === false) props.onTerminalWeight?.(weights[runID] ?? 0)
  }, [props.active, props.onTerminalWeight, runID])
  return (
    <div data-testid={`terminal-${runID}`} data-active={props.active === undefined ? 'default' : String(props.active)}>
      <button type="button" onClick={() => props.onTerminalInvalidate?.()}>
        Invalidate {runID}
      </button>
      <button type="button" onClick={() => props.onTerminalWeight?.(weights[runID] ?? 0)}>
        Weight {runID}
      </button>
    </div>
  )
}

function StubBoard() {
  return <div data-testid="board">board</div>
}

const ids = ['run_a', 'run_b', 'run_c', 'run_d', 'run_e']

function setRoute(name: string, runID?: string) {
  act(() => {
    useStore.setState({
      route: { name, params: runID ? { runId: runID } : {} },
    })
  })
}

function seed() {
  for (const id of Object.keys(mounts)) delete mounts[id]
  for (const id of Object.keys(weights)) delete weights[id]
  routeRegistry.current = { terminal: StubTerminal, board: StubBoard }
  useStore.setState({
    identityKey: 'identity-a',
    terminalCacheEpoch: 0,
    runs: Object.fromEntries(ids.map((id) => [id, toRecord(run({ id }))])),
    route: { name: 'board', params: {} },
  })
}

beforeEach(seed)

describe('CenterView persistent terminal cache', () => {
  it('keeps a terminal mounted across another route and resumes it on revisit', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    expect(mounts.run_a).toBe(1)

    setRoute('board')
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
    expect(screen.getByTestId('terminal-run_a').getAttribute('data-active')).toBe('false')

    const inactive = screen.getByTestId('terminal-run_a').parentElement
    expect(inactive?.style.visibility).toBe('hidden')
    expect(inactive?.hasAttribute('inert')).toBe(true)
    expect(inactive?.getAttribute('aria-hidden')).toBe('true')
    expect(inactive?.className).toContain('absolute')
  })

  it('keeps terminal-to-terminal entries mounted and marks only the current one active', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    setRoute('terminal', 'run_b')

    expect(mounts.run_a).toBe(1)
    expect(mounts.run_b).toBe(1)
    expect(screen.getByTestId('terminal-run_a').getAttribute('data-active')).toBe('false')
    expect(screen.getByTestId('terminal-run_b').getAttribute('data-active')).toBe('true')

    setRoute('terminal', 'run_a')
    expect(mounts.run_a).toBe(1)
    expect(screen.getByTestId('terminal-run_b').getAttribute('data-active')).toBe('false')
  })

  it('evicts the least-recent terminal after the four-entry limit', () => {
    render(<CenterView />)
    for (const id of ids) setRoute('terminal', id)

    expect(screen.queryByTestId('terminal-run_a')).toBeNull()
    expect(screen.getByTestId('terminal-run_b')).toBeDefined()
    expect(screen.getByTestId('terminal-run_c')).toBeDefined()
    expect(screen.getByTestId('terminal-run_d')).toBeDefined()
    expect(screen.getByTestId('terminal-run_e')).toBeDefined()
  })

  it('evicts older scrollback when inactive cells would exceed the budget', () => {
    weights.run_a = 2_500_000
    weights.run_b = 2_500_000
    weights.run_c = 1_000_000
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    setRoute('terminal', 'run_b')
    setRoute('terminal', 'run_c')
    setRoute('board')

    expect(screen.queryByTestId('terminal-run_a')).toBeNull()
    expect(screen.getByTestId('terminal-run_b')).toBeDefined()
    expect(screen.getByTestId('terminal-run_c')).toBeDefined()
  })

  it('filters deleted runs from retained entries', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    setRoute('board')
    act(() => {
      useStore.setState((state) => ({
        runs: Object.fromEntries(
          Object.entries(state.runs).filter(([id]) => id !== 'run_a'),
        ),
      }))
    })
    expect(screen.queryByTestId('terminal-run_a')).toBeNull()
  })

  it('resets the cache when identity or terminal event generation changes', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    setRoute('board')
    act(() => useStore.setState({ identityKey: 'identity-b' }))
    setRoute('terminal', 'run_a')
    expect(mounts.run_a).toBe(2)

    setRoute('board')
    act(() => useStore.setState({ terminalCacheEpoch: 1 }))
    setRoute('terminal', 'run_a')
    expect(mounts.run_a).toBe(3)
  })

  it('protects the active entry while an oversized weight is reported', () => {
    weights.run_a = 5_000_000
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    setRoute('terminal', 'run_b')
    setRoute('terminal', 'run_c')
    setRoute('terminal', 'run_d')
    setRoute('terminal', 'run_a')
    fireEvent.click(screen.getByRole('button', { name: 'Weight run_a' }))

    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
    expect(screen.getByTestId('terminal-run_a').getAttribute('data-active')).toBe('true')
  })

  it('removes an invalidated entry after it becomes inactive', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    fireEvent.click(screen.getByRole('button', { name: 'Invalidate run_a' }))
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()

    setRoute('board')
    expect(screen.queryByTestId('terminal-run_a')).toBeNull()
  })

  it('renders a missing terminal route normally instead of caching it', () => {
    useStore.setState({ runs: {} })
    render(<CenterView />)
    setRoute('terminal', 'run_missing')

    expect(screen.getByTestId('terminal-run_missing').getAttribute('data-active')).toBe('default')
  })
})

import { act, render, screen } from '@testing-library/react'
import { Component } from 'react'
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
const unmounts: Record<string, number> = {}
const sockets: Record<string, Array<{ close: () => void }>> = {}
let boardMounts = 0
let boardUnmounts = 0
const runIDs = ['run_a', 'run_b', 'run_c', 'run_d', 'run_e', 'run_f']

class StubTerminal extends Component<RouteProps> {
  private readonly socket = { close: vi.fn() }

  componentDidMount() {
    const runID = this.props.params.runId ?? ''
    mounts[runID] = (mounts[runID] ?? 0) + 1
    ;(sockets[runID] ??= []).push(this.socket)
  }

  componentWillUnmount() {
    const runID = this.props.params.runId ?? ''
    unmounts[runID] = (unmounts[runID] ?? 0) + 1
    this.socket.close()
  }

  render() {
    const runID = this.props.params.runId ?? ''
    return <div data-testid={`terminal-${runID}`}>terminal {runID}</div>
  }
}

class StubBoard extends Component<RouteProps> {
  componentDidMount() {
    boardMounts += 1
  }

  componentWillUnmount() {
    boardUnmounts += 1
  }

  render() {
    return <div data-testid="board">board</div>
  }
}

function setRoute(name: string, runID?: string) {
  act(() => {
    useStore.setState({
      route: { name, params: runID ? { runId: runID } : {} },
    })
  })
}

function seed() {
  for (const id of Object.keys(mounts)) delete mounts[id]
  for (const id of Object.keys(unmounts)) delete unmounts[id]
  for (const id of Object.keys(sockets)) delete sockets[id]
  boardMounts = 0
  boardUnmounts = 0
  routeRegistry.current = { terminal: StubTerminal, board: StubBoard }
  useStore.setState({
    identityKey: 'identity-a',
    terminalCacheEpoch: 0,
    runs: Object.fromEntries(
      runIDs.map((id) => [id, toRecord(run({ id }))]),
    ),
    route: { name: 'board', params: {} },
  })
}

beforeEach(seed)

describe('CenterView route mounting', () => {
  it('renders only the active registered route', () => {
    render(<CenterView />)
    expect(screen.getByTestId('board')).toBeDefined()
    expect(screen.queryByTestId('terminal-run_a')).toBeNull()

    setRoute('terminal', 'run_a')

    expect(screen.queryByTestId('board')).toBeNull()
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
  })

  it('unmounts the terminal when switching away', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    expect(mounts.run_a).toBe(1)

    setRoute('board')

    expect(screen.queryByTestId('terminal-run_a')).toBeNull()
    expect(unmounts.run_a).toBe(1)
  })

  it('mounts a fresh terminal when revisiting a run', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    setRoute('board')

    setRoute('terminal', 'run_a')

    expect(mounts.run_a).toBe(2)
    expect(unmounts.run_a).toBe(1)
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
  })

  it('preserves the terminal instance for the same run, identity, and epoch', () => {
    const view = render(<CenterView />)
    setRoute('terminal', 'run_a')
    const originalSocket = sockets.run_a[0]

    view.rerender(<CenterView />)
    act(() => {
      useStore.setState({ hydrated: true })
    })
    setRoute('terminal', 'run_a')

    expect(mounts.run_a).toBe(1)
    expect(unmounts.run_a).toBeUndefined()
    expect(originalSocket.close).not.toHaveBeenCalled()
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
  })

  it('disposes and remounts the terminal when the run ID changes', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    const oldSocket = sockets.run_a[0]

    setRoute('terminal', 'run_b')

    expect(screen.queryByTestId('terminal-run_a')).toBeNull()
    expect(unmounts.run_a).toBe(1)
    expect(oldSocket.close).toHaveBeenCalledOnce()
    expect(mounts.run_b).toBe(1)
    expect(screen.getByTestId('terminal-run_b')).toBeDefined()
  })

  it('keeps only the selected terminal mounted across more than the old cache limit', () => {
    render(<CenterView />)

    for (const id of runIDs) {
      setRoute('terminal', id)
      expect(screen.getByTestId(`terminal-${id}`)).toBeDefined()
      for (const other of runIDs.filter((candidate) => candidate !== id)) {
        expect(screen.queryByTestId(`terminal-${other}`)).toBeNull()
      }
    }

    for (const id of runIDs.slice(0, -1)) {
      expect(unmounts[id]).toBe(1)
      expect(sockets[id][0].close).toHaveBeenCalledOnce()
    }
    expect(unmounts.run_f).toBeUndefined()

    setRoute('terminal', 'run_a')
    expect(mounts.run_a).toBe(2)
    expect(unmounts.run_f).toBe(1)
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
  })

  it('disposes and remounts the terminal when the store identity changes', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    const oldSocket = sockets.run_a[0]

    act(() => {
      useStore.setState({ identityKey: 'identity-b' })
    })

    expect(unmounts.run_a).toBe(1)
    expect(oldSocket.close).toHaveBeenCalledOnce()
    expect(mounts.run_a).toBe(2)
    expect(sockets.run_a[1].close).not.toHaveBeenCalled()
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
  })

  it('disposes and remounts the terminal when the event epoch changes', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    const oldSocket = sockets.run_a[0]

    act(() => {
      useStore.setState({ terminalCacheEpoch: 1 })
    })

    expect(unmounts.run_a).toBe(1)
    expect(oldSocket.close).toHaveBeenCalledOnce()
    expect(mounts.run_a).toBe(2)
    expect(sockets.run_a[1].close).not.toHaveBeenCalled()
    expect(screen.getByTestId('terminal-run_a')).toBeDefined()
  })

  it('keeps a non-terminal route mounted across identity and epoch changes', () => {
    render(<CenterView />)

    act(() => {
      useStore.setState({ identityKey: 'identity-b', terminalCacheEpoch: 1 })
    })

    expect(boardMounts).toBe(1)
    expect(boardUnmounts).toBe(0)
    expect(screen.getByTestId('board')).toBeDefined()
  })

  it('shows the fallback for an unknown route', () => {
    render(<CenterView />)

    setRoute('missing')

    expect(screen.queryByTestId('board')).toBeNull()
    expect(screen.getByText('No view registered for “missing”.')).toBeDefined()
  })

  it('does not retain a hidden terminal across store and run changes', () => {
    render(<CenterView />)
    setRoute('terminal', 'run_a')
    setRoute('board')

    act(() => {
      useStore.setState({ identityKey: 'identity-b', runs: {} })
    })

    expect(screen.getByTestId('board')).toBeDefined()
    expect(screen.queryByTestId('terminal-run_a')).toBeNull()
    expect(unmounts.run_a).toBe(1)

    setRoute('terminal', 'run_a')
    expect(mounts.run_a).toBe(2)
  })
})

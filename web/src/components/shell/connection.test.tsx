import { renderHook } from '@testing-library/react'
import { useConnectionProblem, useConnectionWord } from '@/components/shell/connection'
import { useStore } from '@/store'

const linkStatus = { server_configured: true, linked: true, addr: 'server', user: 'alice', repo: '' }

beforeEach(() => {
  useStore.setState(useStore.getInitialState(), true)
})

describe('the connection problem', () => {
  it('reports an offline feed to a configured server', () => {
    useStore.setState({ connection: 'offline', linkStatus })
    expect(renderHook(useConnectionProblem).result.current).toBe('Offline')
  })

  it('says nothing while no server is configured to connect to', () => {
    useStore.setState({ connection: 'offline', linkStatus: { ...linkStatus, server_configured: false, linked: false } })
    expect(renderHook(useConnectionProblem).result.current).toBeNull()
  })
})

describe('a failed request on a link through an edge', () => {
  const edgeLink = { ...linkStatus, addr: '', edge_url: 'https://edge.example.test', server_id: 'srv123' }

  it('never reads as Live, and names the layer, the server and the next command', () => {
    useStore.setState({ connection: 'live', linkStatus: edgeLink, unreachable: 'edge-server' })
    const problem = renderHook(useConnectionProblem).result.current
    expect(problem).toBe(
      'Server registration failed · edge.example.test has no live registration for server srv123 - compare with sudo aether-server edge status on that host',
    )
    expect(renderHook(useConnectionWord).result.current).toBe('Unreachable')
  })

  it('says a server mid-claim is finishing its claim, not to issue a claim code', () => {
    useStore.setState({ connection: 'reconnecting', linkStatus: edgeLink, unreachable: 'edge-not-claimed' })
    expect(renderHook(useConnectionProblem).result.current).toBe(
      'Server registration failed · server srv123 is connected to edge.example.test and is finishing its claim - retrying',
    )
  })

  it('names the first hop of the link for an offline computer', () => {
    useStore.setState({ connection: 'reconnecting', linkStatus: edgeLink, unreachable: 'network' })
    expect(renderHook(useConnectionProblem).result.current).toMatch(/^Client to edge failed · /)
    useStore.setState({ linkStatus: { ...linkStatus, addr: 'host:2222' } })
    expect(renderHook(useConnectionProblem).result.current).toMatch(/^Client to server failed · /)
  })

  it('reads as Live once the failure is cleared', () => {
    useStore.setState({ connection: 'live', linkStatus: edgeLink, unreachable: null })
    expect(renderHook(useConnectionProblem).result.current).toBeNull()
    expect(renderHook(useConnectionWord).result.current).toBe('Live')
  })
})

describe('where the gateway says the failure happened', () => {
  const edgeLink = { ...linkStatus, addr: '', edge_url: 'https://edge.example.test', server_id: 'srv123' }

  it('takes the layer, server and edge from the edge path', () => {
    useStore.setState({
      connection: 'reconnecting',
      linkStatus: edgeLink,
      unreachable: 'edge-refused',
      unreachableAt: { layer: 'server-registration', server_id: 'srv123', edge: 'edge.onaether.dev' },
    })
    expect(renderHook(useConnectionProblem).result.current).toBe(
      'Server registration failed · edge.onaether.dev refused the connection to server srv123',
    )
  })

  it('shows a failure measured on another server than the linked one, named by its own id', () => {
    useStore.setState({
      connection: 'live',
      linkStatus: { ...edgeLink, server_id: 'srvA' },
      unreachable: 'edge-server',
      unreachableAt: { server_id: 'srvB' },
    })
    expect(renderHook(useConnectionProblem).result.current).toBe(
      'Server registration failed · edge.example.test has no live registration for server srvB - compare with sudo aether-server edge status on that host',
    )
    expect(renderHook(useConnectionWord).result.current).toBe('Unreachable')
  })
})

it('names the servers the edge does see online when the linked one is not', () => {
  useStore.setState({
    connection: 'reconnecting',
    linkStatus: { ...linkStatus, addr: '', edge_url: 'https://edge.example.test', server_id: 'srv123' },
    unreachable: 'edge-server',
    unreachableAt: {
      layer: 'server-registration',
      server_id: 'srv123',
      edge: 'edge.example.test',
      online: [{ id: 'srv456', name: 'workstation', online: true, role: 'admin', access_policy: 'account', kind: 'self-hosted' }],
    },
  })
  expect(renderHook(useConnectionProblem).result.current).toBe(
    'Server registration failed · edge.example.test has no live registration for server srv123 - compare with sudo aether-server edge status on that host; edge.example.test sees workstation (srv456) online, and this dashboard is linked to server srv123',
  )
})

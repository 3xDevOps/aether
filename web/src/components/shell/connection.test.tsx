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

  it('tells a server nobody claimed to issue a claim code', () => {
    useStore.setState({ connection: 'reconnecting', linkStatus: edgeLink, unreachable: 'edge-not-claimed' })
    expect(renderHook(useConnectionProblem).result.current).toBe(
      'Server registration failed · server srv123 is connected to edge.example.test but not claimed - run sudo aether-server edge claim-code on that host',
    )
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

  it('does not show a failure measured on the server linked before', () => {
    useStore.setState({
      connection: 'live',
      linkStatus: { ...edgeLink, server_id: 'srvB' },
      unreachable: 'edge-server',
      unreachableAt: { server_id: 'srvA' },
    })
    expect(renderHook(useConnectionProblem).result.current).toBeNull()
    expect(renderHook(useConnectionWord).result.current).toBe('Live')
  })
})

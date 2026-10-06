import { renderHook } from '@testing-library/react'
import { useConnectionProblem } from '@/components/shell/connection'
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

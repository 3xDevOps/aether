import { act } from '@testing-library/react'
import { bindRouteToUrl, historyDepth, hrefFor, redirectRoute, routeFromQuery, routeQuery, sessionEntry } from '@/lib/url-state'
import { createRootStore } from '@/store'
import { atViewport } from '@/test/viewport'
import type { Route } from '@/store/ui'

const query = (route: Route) => routeQuery(route).toString()

afterEach(() => {
  window.history.replaceState(null, '', '/')
})

describe('route in the query string', () => {
  it.each<[Route, string]>([
    [{ name: 'board', params: {} }, ''],
    [{ name: 'run', params: { runId: 'run_1' } }, 'run=run_1'],
    [{ name: 'run', params: { runId: 'run_1', view: 'changes' } }, 'run=run_1&view=changes'],
    [{ name: 'run', params: { runId: 'run_1', view: 'session', agent: 'toolu_1' } }, 'run=run_1&view=session&agent=toolu_1'],
    [{ name: 'settings', params: {} }, 'page=settings'],
    [{ name: 'settings', params: { section: 'connectors' } }, 'page=settings&id=connectors'],
    [{ name: 'workspace', params: { workspaceId: 'wsp_1' } }, 'page=workspace&id=wsp_1'],
    [{ name: 'templates', params: { template: 'dep-audit' } }, 'page=templates&id=dep-audit'],
  ])('writes %o as "%s" and reads it back', (route, text) => {
    expect(query(route)).toBe(text)
    expect(routeFromQuery(new URLSearchParams(text))).toEqual(route)
  })

  it('reads the shells\' bare ?run= deep link as that run with its default view', () => {
    expect(routeFromQuery(new URLSearchParams('run=run_9'))).toEqual({ name: 'run', params: { runId: 'run_9' } })
  })

  it('reads the old diff view as Changes and drops a view it does not know', () => {
    expect(routeFromQuery(new URLSearchParams('run=run_9&view=diff'))).toEqual({ name: 'run', params: { runId: 'run_9', view: 'changes' } })
    expect(routeFromQuery(new URLSearchParams('run=run_9&view=events'))).toEqual({ name: 'run', params: { runId: 'run_9' } })
  })

  it('keeps every other query parameter and the hash', () => {
    expect(hrefFor('http://h/?keep=1&page=files#x', { name: 'run', params: { runId: 'r' } }))
      .toBe('http://h/?keep=1&run=r#x')
    expect(hrefFor('http://h/?run=r&agent=old&keep=1', { name: 'run', params: { runId: 'r' } }))
      .toBe('http://h/?keep=1&run=r')
  })
})

describe('bindRouteToUrl', () => {
  it('reads a phone\'s bare address as Needs you when Back returns to it, as on load', () => {
    atViewport(390, { pointer: 'coarse' })
    window.history.replaceState(null, '', '/')
    const store = createRootStore()
    const stop = bindRouteToUrl(store)
    onTestFinished(stop)
    window.history.replaceState(null, '', '/')

    act(() => store.getState().navigate('run', { runId: 'run_1' }))
    act(() => {
      window.history.back()
    })
    return vi.waitFor(() => expect(store.getState().route).toEqual({ name: 'needs-you', params: {} }))
  })

  it('remembers where a session was opened across its own entries, for a phone\'s Back', () => {
    const store = createRootStore()
    store.setState({ route: { name: 'needs-you', params: {} } })
    const stop = bindRouteToUrl(store)
    onTestFinished(stop)
    expect(sessionEntry()).toBeUndefined()

    act(() => store.getState().navigate('run', { runId: 'run_1' }))
    act(() => redirectRoute(store, { name: 'run', params: { runId: 'run_1', view: 'terminal' } }))
    act(() => store.getState().navigate('run', { runId: 'run_1', view: 'session', agent: 'toolu_1' }))
    expect([historyDepth(), sessionEntry()]).toEqual([2, 1])

    act(() => store.getState().navigate('run', { runId: 'run_2' }))
    expect([historyDepth(), sessionEntry()]).toEqual([3, 3])
    act(() => store.getState().navigate('settings'))
    expect([historyDepth(), sessionEntry()]).toEqual([4, undefined])
  })

  it('starts the store on the address, pushes each navigation and follows back', () => {
    window.history.replaceState(null, '', '/?run=run_1&view=changes')
    const store = createRootStore()
    expect(store.getState().route).toEqual({ name: 'run', params: { runId: 'run_1', view: 'changes' } })
    const stop = bindRouteToUrl(store)
    onTestFinished(stop)

    act(() => store.getState().navigate('settings'))
    expect(window.location.search).toBe('?page=settings')

    act(() => {
      window.history.back()
    })
    return vi.waitFor(() => {
      expect(window.location.search).toBe('?run=run_1&view=changes')
      expect(store.getState().route).toEqual({ name: 'run', params: { runId: 'run_1', view: 'changes' } })
    })
  })

  it('leaves the history alone when the route does not change the address', () => {
    const store = createRootStore()
    const stop = bindRouteToUrl(store)
    onTestFinished(stop)
    const length = window.history.length

    act(() => store.getState().navigate('board'))

    expect(window.history.length).toBe(length)
  })

  it('replaces the entry on a redirect so back skips the redirected address', () => {
    window.history.replaceState(null, '', '/?page=settings')
    window.history.pushState(null, '', '/?run=run_gone')
    const store = createRootStore()
    const stop = bindRouteToUrl(store)
    onTestFinished(stop)

    act(() => redirectRoute(store, { name: 'board', params: {} }))
    expect(window.location.search).toBe('')

    act(() => {
      window.history.back()
    })
    return vi.waitFor(() => {
      expect(window.location.search).toBe('?page=settings')
      expect(store.getState().route).toEqual({ name: 'settings', params: {} })
    })
  })
})

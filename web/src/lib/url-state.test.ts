import { act } from '@testing-library/react'
import { bindRouteToUrl, hrefFor, redirectRoute, routeFromQuery, routeQuery } from '@/lib/url-state'
import { createRootStore } from '@/store'
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
    [{ name: 'settings', params: {} }, 'page=settings'],
    [{ name: 'missions', params: { missionId: 'mis_1' } }, 'page=missions&id=mis_1'],
    [{ name: 'workspace', params: { workspaceId: 'wsp_1' } }, 'page=workspace&id=wsp_1'],
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
  })
})

describe('bindRouteToUrl', () => {
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

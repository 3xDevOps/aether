import { isRunView, runRoute } from '@/routes/run/views'
import type { Route } from '@/store/ui'

const renamedViews: Record<string, string> = { diff: 'changes' }

const pageIDs: Record<string, string> = {
  missions: 'missionId',
  workspace: 'workspaceId',
}

const keys = ['run', 'view', 'page', 'id']

const boardRoute: Route = { name: 'board', params: {} }

export function routeQuery(route: Route): URLSearchParams {
  const query = new URLSearchParams()
  const { runId, view } = route.params
  if (route.name === 'run' && runId) {
    query.set('run', runId)
    if (view) query.set('view', view)
    return query
  }
  if (route.name === 'board') return query
  query.set('page', route.name)
  const id = route.params[pageIDs[route.name] ?? '']
  if (id) query.set('id', id)
  return query
}

export function routeFromQuery(query: URLSearchParams): Route {
  const run = query.get('run')
  if (run) {
    const asked = query.get('view') ?? ''
    const view = renamedViews[asked] ?? asked
    return runRoute(run, isRunView(view) ? view : undefined)
  }
  const page = query.get('page')
  if (!page) return boardRoute
  const id = query.get('id')
  const param = pageIDs[page]
  return { name: page, params: param && id ? { [param]: id } : {} }
}

export function hrefFor(href: string, route: Route): string {
  const url = new URL(href)
  for (const key of keys) url.searchParams.delete(key)
  for (const [key, value] of routeQuery(route)) url.searchParams.set(key, value)
  return url.toString()
}

export function initialRoute(): Route {
  if (typeof window === 'undefined') return boardRoute
  return routeFromQuery(new URLSearchParams(window.location.search))
}

/**
 * Swaps the route without a history entry, for redirects the member did not
 * ask for: back must not return to the address that was redirected away from.
 */
export function redirectRoute(store: { setState: (state: { route: Route }) => void }, route: Route) {
  window.history.replaceState(null, '', hrefFor(window.location.href, route))
  store.setState({ route })
}

interface RouteStore {
  getState: () => { route: Route; navigate: (name: string, params?: Record<string, string>) => void }
  subscribe: (listener: (state: { route: Route }, previous: { route: Route }) => void) => () => void
}

/**
 * Keeps the address bar and the store's route in step: a navigation pushes a
 * history entry, and back or forward navigates to the entry's route.
 */
export function bindRouteToUrl(store: RouteStore): () => void {
  const sync = (route: Route, push: boolean) => {
    const next = hrefFor(window.location.href, route)
    if (next === window.location.href) return
    if (push) window.history.pushState(null, '', next)
    else window.history.replaceState(null, '', next)
  }
  sync(store.getState().route, false)
  const stop = store.subscribe((state, previous) => {
    if (state.route !== previous.route) sync(state.route, true)
  })
  const pop = () => {
    const route = routeFromQuery(new URLSearchParams(window.location.search))
    store.getState().navigate(route.name, route.params)
  }
  window.addEventListener('popstate', pop)
  return () => {
    stop()
    window.removeEventListener('popstate', pop)
  }
}

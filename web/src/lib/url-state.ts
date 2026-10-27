import { isRunView, runRoute } from '@/routes/run/views'
import type { Route } from '@/store/ui'

const renamedViews: Record<string, string> = { diff: 'changes' }

const pageIDs: Record<string, string> = {
  missions: 'missionId',
  workspace: 'workspaceId',
  insights: 'tab',
  settings: 'section',
  templates: 'template',
}

const keys = ['run', 'view', 'agent', 'repo', 'page', 'id', 'watch']

const boardRoute: Route = { name: 'board', params: {} }

export function routeQuery(route: Route): URLSearchParams {
  const query = new URLSearchParams()
  const { runId, view, agent, repo } = route.params
  if (route.name === 'run' && runId) {
    query.set('run', runId)
    if (view) query.set('view', view)
    if (agent) query.set('agent', agent)
    if (repo && view === 'changes') query.set('repo', repo)
    return query
  }
  if (route.name === 'board') return query
  query.set('page', route.name)
  const id = route.params[pageIDs[route.name] ?? '']
  if (id) query.set('id', id)
  if (route.name === 'settings' && route.params.watch) query.set('watch', route.params.watch)
  return query
}

export function routeFromQuery(query: URLSearchParams): Route {
  const run = query.get('run')
  if (run) {
    const asked = query.get('view') ?? ''
    const view = renamedViews[asked] ?? asked
    const route = runRoute(run, isRunView(view) ? view : undefined)
    const agent = query.get('agent')
    const repo = route.params.view === 'changes' ? query.get('repo') : null
    return agent || repo ? { ...route, params: { ...route.params, ...(agent ? { agent } : {}), ...(repo ? { repo } : {}) } } : route
  }
  const page = query.get('page')
  if (!page) return boardRoute
  const id = query.get('id')
  const param = pageIDs[page]
  const watch = page === 'settings' ? query.get('watch') : null
  return { name: page, params: { ...(param && id ? { [param]: id } : {}), ...(watch ? { watch } : {}) } }
}

export function hrefFor(href: string, route: Route): string {
  const url = new URL(href)
  for (const key of keys) url.searchParams.delete(key)
  for (const [key, value] of routeQuery(route)) url.searchParams.set(key, value)
  return url.toString()
}

export function initialRoute(): Route {
  if (typeof window === 'undefined') return boardRoute
  if (handedLink()) return { name: 'onboarding', params: {} }
  const route = routeFromQuery(new URLSearchParams(window.location.search))
  const hash = window.location.hash.slice(1)
  if (route.name !== 'settings' || route.params.section || !hash) return route
  window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search)
  return { name: 'settings', params: { section: hash } }
}

export type HandedLink = { kind: 'join' | 'claim'; link: string }

let handed: HandedLink | null = null

/**
 * The join or claim link a shell handed over in the fragment as #join= or
 * #claim=, which the browser never sends to the server. Reading takes it
 * off the address, so the secret in it stays in neither the address bar
 * nor history; it is kept in memory until forgetHandedLink.
 */
export function handedLink(): HandedLink | null {
  const fragment = new URLSearchParams(window.location.hash.slice(1))
  for (const kind of ['join', 'claim'] as const) {
    const link = fragment.get(kind)
    if (link) handed = { kind, link }
  }
  clearHandedFragment()
  return handed
}

const handedListeners = new Set<(link: HandedLink) => void>()

function handedOnHashChange() {
  const fragment = new URLSearchParams(window.location.hash.slice(1))
  if (!fragment.get('join') && !fragment.get('claim')) return
  const link = handedLink()
  if (link) for (const listener of handedListeners) listener(link)
}

/** Calls listener with each join or claim link handed over after load. */
export function onHandedLink(listener: (link: HandedLink) => void): () => void {
  if (handedListeners.size === 0) window.addEventListener('hashchange', handedOnHashChange)
  handedListeners.add(listener)
  return () => {
    handedListeners.delete(listener)
    if (handedListeners.size === 0) window.removeEventListener('hashchange', handedOnHashChange)
  }
}

const forgottenListeners = new Set<() => void>()

/** Calls listener each time the handed link is forgotten. */
export function onHandedLinkForgotten(listener: () => void): () => void {
  forgottenListeners.add(listener)
  return () => forgottenListeners.delete(listener)
}

export function forgetHandedLink() {
  handed = null
  clearHandedFragment()
  for (const listener of forgottenListeners) listener()
}

function clearHandedFragment() {
  const fragment = new URLSearchParams(window.location.hash.slice(1))
  if (!fragment.has('join') && !fragment.has('claim')) return
  const url = new URL(window.location.href)
  url.hash = ''
  window.history.replaceState(window.history.state, '', url.toString())
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
  const stopHanded = onHandedLink(() => store.getState().navigate('onboarding'))
  return () => {
    stop()
    stopHanded()
    window.removeEventListener('popstate', pop)
  }
}

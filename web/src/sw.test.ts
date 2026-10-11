import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// The worker is a plain script served as /sw.js; it is run here against a
// stand-in for the ServiceWorkerGlobalScope it registers its handlers on.
const source = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), '../public/sw.js'), 'utf8')

type Handler = (event: Record<string, unknown>) => void

function worker(windows: { focused: boolean; focus: () => Promise<void>; postMessage: (m: unknown) => void }[] = []) {
  const handlers: Record<string, Handler> = {}
  const self = {
    addEventListener: (type: string, handler: Handler) => {
      handlers[type] = handler
    },
    skipWaiting: vi.fn(),
    registration: { showNotification: vi.fn(async () => {}) },
    clients: {
      matchAll: vi.fn(async () => windows),
      openWindow: vi.fn(async () => null),
    },
  }
  new Function('self', source)(self)
  const dispatch = async (type: string, event: Record<string, unknown>) => {
    let work: Promise<unknown> = Promise.resolve()
    handlers[type]({ ...event, waitUntil: (promise: Promise<unknown>) => { work = promise } })
    await work
  }
  return { self, dispatch }
}

const window = (focused: boolean) => ({ focused, focus: vi.fn(async () => {}), postMessage: vi.fn() })

describe('service worker', () => {
  it('shows one notification per run, replacing the older one', async () => {
    const { self, dispatch } = worker()
    await dispatch('push', {
      data: { json: () => ({ title: 'Fix the login redirect', body: 'Waiting for your reply', run: 'run-7k2m9q4xbd' }) },
    })
    expect(self.registration.showNotification).toHaveBeenCalledWith('Fix the login redirect', {
      body: 'Waiting for your reply',
      tag: 'run-7k2m9q4xbd',
      renotify: true,
      icon: '/icons/icon-192.png',
      data: { run: 'run-7k2m9q4xbd' },
    })
  })

  it('still shows something for a push it cannot read', async () => {
    const { self, dispatch } = worker()
    await dispatch('push', { data: null })
    expect(self.registration.showNotification).toHaveBeenCalledWith('Aether', expect.objectContaining({ body: 'A run needs you.', tag: 'aether' }))
  })

  it('opens the run in a window when no dashboard is open', async () => {
    const { self, dispatch } = worker()
    const close = vi.fn()
    await dispatch('notificationclick', { notification: { close, data: { run: 'run-7k2m9q4xbd' } } })
    expect(close).toHaveBeenCalled()
    expect(self.clients.matchAll).toHaveBeenCalledWith({ type: 'window', includeUncontrolled: true })
    expect(self.clients.openWindow).toHaveBeenCalledWith('/?run=run-7k2m9q4xbd')
  })

  it('focuses an open dashboard and tells it which run to open', async () => {
    const background = window(false)
    const front = window(true)
    const { self, dispatch } = worker([background, front])
    await dispatch('notificationclick', { notification: { close: vi.fn(), data: { run: 'run-7k2m9q4xbd' } } })
    expect(front.postMessage).toHaveBeenCalledWith({ type: 'aether:open-run', run: 'run-7k2m9q4xbd' })
    expect(front.focus).toHaveBeenCalled()
    expect(background.focus).not.toHaveBeenCalled()
    expect(self.clients.openWindow).not.toHaveBeenCalled()
  })

  it('opens the dashboard itself from a test notification', async () => {
    const { self, dispatch } = worker()
    await dispatch('notificationclick', { notification: { close: vi.fn(), data: {} } })
    expect(self.clients.openWindow).toHaveBeenCalledWith('/')
  })

  it('takes over from an older worker at once and never handles fetch', async () => {
    const { self, dispatch } = worker()
    await dispatch('install', {})
    expect(self.skipWaiting).toHaveBeenCalled()
    await expect(dispatch('fetch', {})).rejects.toThrow()
  })
})

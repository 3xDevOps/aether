import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// The worker is a plain script served as /sw.js; it is run here against a
// stand-in for the ServiceWorkerGlobalScope it registers its handlers on.
const source = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), '../public/sw.js'), 'utf8')

type Handler = (event: Record<string, unknown>) => void

type Window = { focused: boolean; focus: () => Promise<void>; postMessage: (m: unknown) => void; navigate: (url: string) => Promise<unknown> }

function worker(
  windows: Window[] = [],
  shown: { tag: string; title: string; body?: string; data?: Record<string, unknown> }[] = [],
) {
  const handlers: Record<string, Handler> = {}
  const self = {
    addEventListener: (type: string, handler: Handler) => {
      handlers[type] = handler
    },
    skipWaiting: vi.fn(),
    registration: {
      showNotification: vi.fn(async (_title: string, _options: { body: string }) => {}),
      getNotifications: vi.fn(async ({ tag }: { tag: string }) => shown.filter((n) => n.tag === tag)),
    },
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

const window = (focused: boolean): Window => ({ focused, focus: vi.fn(async () => {}), postMessage: vi.fn(), navigate: vi.fn(async () => null) })

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

  it('quietly replaces the notification of a need that ended, keeping its title', async () => {
    const { self, dispatch } = worker([], [{ tag: 'run-7k2m9q4xbd', title: 'Fix the login redirect' }])
    await dispatch('push', {
      data: { json: () => ({ title: 'Renamed run', body: 'No longer needs you', run: 'run-7k2m9q4xbd', clear: true }) },
    })
    expect(self.registration.showNotification).toHaveBeenCalledWith('Fix the login redirect', {
      body: 'No longer needs you',
      tag: 'run-7k2m9q4xbd',
      silent: true,
      icon: '/icons/icon-192.png',
      data: { run: 'run-7k2m9q4xbd' },
    })
  })

  it("quietly replaces a swarm's need, tagged by the page it opens, when it ends", async () => {
    const url = '/?page=missions&id=mis-4c8v2n6k1p'
    const { self, dispatch } = worker([], [{ tag: url, title: 'Swarm: login rework', data: { url } }])
    await dispatch('push', { data: { json: () => ({ title: 'Swarm: login rework', body: 'No longer needs you', url, clear: true }) } })
    expect(self.registration.showNotification).toHaveBeenCalledWith('Swarm: login rework', {
      body: 'No longer needs you',
      tag: url,
      silent: true,
      icon: '/icons/icon-192.png',
      data: { url },
    })
  })

  it("keeps an assistant's newer notice about the run when its need clears", async () => {
    const notice = { tag: 'run-7k2m9q4xbd', title: 'Run "Fix it" finished', body: 'All green', data: { run: 'run-7k2m9q4xbd', notice: true } }
    const { self, dispatch } = worker([], [notice])
    await dispatch('push', { data: { json: () => ({ title: 'Fix it', body: 'No longer needs you', run: 'run-7k2m9q4xbd', clear: true }) } })
    expect(self.registration.showNotification).toHaveBeenCalledWith('Run "Fix it" finished', {
      body: 'All green',
      tag: 'run-7k2m9q4xbd',
      silent: true,
      icon: '/icons/icon-192.png',
      data: { run: 'run-7k2m9q4xbd', notice: true },
    })
  })

  it('still shows a cleared need the user already dismissed, without alerting', async () => {
    const { self, dispatch } = worker()
    await dispatch('push', {
      data: { json: () => ({ title: 'Fix the login redirect', body: 'No longer needs you', run: 'run-7k2m9q4xbd', clear: true }) },
    })
    expect(self.registration.showNotification).toHaveBeenCalledWith('Fix the login redirect', expect.objectContaining({ silent: true, tag: 'run-7k2m9q4xbd' }))
    expect(self.registration.showNotification).not.toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ renotify: true }))
  })

  it('never lets a clear still reading the shown notification land over a newer need', async () => {
    const { self, dispatch } = worker()
    let release = () => {}
    self.registration.getNotifications.mockImplementationOnce(() => new Promise((resolve) => { release = () => resolve([]) }))
    const cleared = dispatch('push', { data: { json: () => ({ title: 'Fix it', body: 'No longer needs you', run: 'run-7k2m9q4xbd', clear: true }) } })
    const asked = dispatch('push', { data: { json: () => ({ title: 'Fix it', body: 'which region?', run: 'run-7k2m9q4xbd' }) } })
    await Promise.resolve()
    expect(self.registration.showNotification).not.toHaveBeenCalled()
    release()
    await Promise.all([cleared, asked])
    expect(self.registration.showNotification.mock.calls.map(([, options]) => options.body)).toEqual(['No longer needs you', 'which region?'])
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

  it('keeps one notification per workspace budget and opens the workspace', async () => {
    const { self, dispatch } = worker()
    await dispatch('push', {
      data: { json: () => ({ title: 'proj', body: 'Budget warning: $8.50 of $10.00 spent', workspace: 'ws-b9gjv14p8h' }) },
    })
    expect(self.registration.showNotification).toHaveBeenCalledWith('proj', expect.objectContaining({
      tag: 'budget:ws-b9gjv14p8h',
      data: { workspace: 'ws-b9gjv14p8h' },
    }))
    await dispatch('notificationclick', { notification: { close: vi.fn(), data: { workspace: 'ws-b9gjv14p8h' } } })
    expect(self.clients.openWindow).toHaveBeenCalledWith('/?page=workspace&id=ws-b9gjv14p8h')

    const front = window(true)
    await worker([front]).dispatch('notificationclick', { notification: { close: vi.fn(), data: { workspace: 'ws-b9gjv14p8h' } } })
    expect(front.postMessage).toHaveBeenCalledWith({ type: 'aether:open-workspace', workspace: 'ws-b9gjv14p8h' })
  })

  it('keeps one notification per page and opens the page it names', async () => {
    const { self, dispatch } = worker()
    const url = '/?page=missions&id=mis-rtgje8zxp6'
    await dispatch('push', { data: { json: () => ({ title: 'Ship watches', body: 'Swarm completed', url }) } })
    expect(self.registration.showNotification).toHaveBeenCalledWith('Ship watches', expect.objectContaining({ tag: url, data: { url } }))
    await dispatch('notificationclick', { notification: { close: vi.fn(), data: { url } } })
    expect(self.clients.openWindow).toHaveBeenCalledWith(url)

    const front = window(true)
    await worker([front]).dispatch('notificationclick', { notification: { close: vi.fn(), data: { url } } })
    expect(front.navigate).toHaveBeenCalledWith(url)
    expect(front.focus).toHaveBeenCalled()
  })

  it('opens the page in a new window when the open one is not its own', async () => {
    const front = { ...window(true), navigate: vi.fn(async () => { throw new TypeError('not controlled') }) }
    const { self, dispatch } = worker([front])
    await dispatch('notificationclick', { notification: { close: vi.fn(), data: { url: '/?page=settings&id=watches' } } })
    expect(self.clients.openWindow).toHaveBeenCalledWith('/?page=settings&id=watches')
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

import { ApiError } from '@/lib/api'
import { disablePush, enablePush, readPush, testPush, watchPush } from '@/lib/push'
import type { GatewayCapabilities } from '@/lib/types'
import { createRootStore } from '@/store'
import { fakeApi, serverInfo } from '@/test/fixtures'

// The 65-octet uncompressed point a server hands out, unpadded base64url.
const serverKey = `BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8`
const endpoint = 'https://push.example/send/device'

function bytes(key: string): ArrayBuffer {
  return Uint8Array.from(atob(key.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0)).buffer
}

function fakeSubscription(key: ArrayBuffer) {
  const json = { endpoint, keys: { p256dh: 'p256dh', auth: 'auth' } }
  return {
    endpoint,
    options: { applicationServerKey: key },
    toJSON: () => json,
    unsubscribe: vi.fn(async () => true),
  }
}

type Subscription = ReturnType<typeof fakeSubscription>

/** What a browser with push looks like to the dashboard. */
function browser(permission: NotificationPermission, held: Subscription | null = null, answer: NotificationPermission = 'granted') {
  const order: string[] = []
  const pushManager = {
    getSubscription: vi.fn(async () => held),
    subscribe: vi.fn(async (options: { applicationServerKey: Uint8Array }) => {
      order.push('subscribe')
      held = fakeSubscription(options.applicationServerKey.slice().buffer)
      return held
    }),
  }
  const registration = { pushManager }
  const serviceWorker = Object.assign(new EventTarget(), {
    register: vi.fn(async () => {
      order.push('register')
      return registration
    }),
    ready: Promise.resolve(registration),
    getRegistration: vi.fn(async () => registration),
  })
  Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: serviceWorker })
  vi.stubGlobal('PushManager', class {})
  vi.stubGlobal('Notification', {
    permission,
    requestPermission: vi.fn(async () => {
      order.push('permission')
      return answer
    }),
  })
  return { order, pushManager, serviceWorker, subscription: () => held }
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  Reflect.deleteProperty(navigator, 'serviceWorker')
  Reflect.deleteProperty(window, 'aetherDesktop')
})

describe('readPush', () => {
  it('says why a dashboard cannot receive notifications', async () => {
    const client = fakeApi()
    expect(await readPush(client, 'local')).toEqual({ kind: 'unsupported', why: 'gateway' })
    // jsdom has neither a service worker nor a push manager: a WebView.
    expect(await readPush(client, 'server')).toEqual({ kind: 'unsupported', why: 'browser' })

    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) Safari/604.1', maxTouchPoints: 5 })
    expect(await readPush(client, 'server')).toEqual({ kind: 'unsupported', why: 'home-screen' })
    vi.unstubAllGlobals()

    Object.assign(window, { aetherDesktop: { platform: 'linux' } })
    browser('granted', fakeSubscription(bytes(serverKey)))
    expect(await readPush(client, 'server')).toEqual({ kind: 'unsupported', why: 'desktop' })
    expect(client.pushStatus).not.toHaveBeenCalled()
  })

  it('is on only while the server still holds this browser', async () => {
    browser('denied')
    expect(await readPush(fakeApi(), 'server')).toEqual({ kind: 'blocked' })

    browser('default')
    expect(await readPush(fakeApi(), 'server')).toEqual({ kind: 'off' })

    browser('granted', fakeSubscription(bytes(serverKey)))
    const held = fakeApi({ pushStatus: vi.fn(async () => ({ public_key: serverKey, subscribed: true })) })
    expect(await readPush(held, 'server')).toEqual({ kind: 'on' })
    expect(held.pushStatus).toHaveBeenCalledWith(endpoint)

    // The push service reported the device gone and the server dropped it.
    expect(await readPush(fakeApi(), 'server')).toEqual({ kind: 'off' })
  })
})

describe('enablePush', () => {
  const client = () => fakeApi({ pushStatus: vi.fn(async () => ({ public_key: serverKey, subscribed: false })) })

  it('asks for permission first, then subscribes with the server key and hands the subscription over', async () => {
    const env = browser('default')
    const api = client()

    expect(await enablePush(api)).toEqual({ kind: 'on' })

    expect(env.order).toEqual(['permission', 'register', 'subscribe'])
    expect(env.serviceWorker.register).toHaveBeenCalledWith('/sw.js')
    const options = env.pushManager.subscribe.mock.calls[0][0] as { userVisibleOnly: boolean; applicationServerKey: Uint8Array }
    expect(options.userVisibleOnly).toBe(true)
    expect(Array.from(options.applicationServerKey)).toEqual(Array.from(new Uint8Array(bytes(serverKey))))
    expect(api.pushSubscribe).toHaveBeenCalledWith({ endpoint, keys: { p256dh: 'p256dh', auth: 'auth' } })
  })

  it('stops at a refused prompt without registering anything', async () => {
    const denied = browser('default', null, 'denied')
    expect(await enablePush(client())).toEqual({ kind: 'blocked' })
    const dismissed = browser('default', null, 'default')
    expect(await enablePush(client())).toEqual({ kind: 'off' })
    expect(denied.serviceWorker.register).not.toHaveBeenCalled()
    expect(dismissed.serviceWorker.register).not.toHaveBeenCalled()
  })

  it('keeps a subscription made for this server key and replaces one made for another', async () => {
    const kept = fakeSubscription(bytes(serverKey))
    const same = browser('granted', kept)
    await enablePush(client())
    expect(same.pushManager.subscribe).not.toHaveBeenCalled()
    expect(kept.unsubscribe).not.toHaveBeenCalled()

    const stale = fakeSubscription(new Uint8Array(65).buffer)
    const other = browser('granted', stale)
    await enablePush(client())
    expect(stale.unsubscribe).toHaveBeenCalled()
    expect(other.pushManager.subscribe).toHaveBeenCalled()
  })
})

describe('disablePush and testPush', () => {
  it('unsubscribes on the server and in the browser, even when the server had already dropped it', async () => {
    const held = fakeSubscription(bytes(serverKey))
    browser('granted', held)
    const api = fakeApi()
    expect(await disablePush(api)).toEqual({ kind: 'off' })
    expect(api.pushUnsubscribe).toHaveBeenCalledWith(endpoint)
    expect(held.unsubscribe).toHaveBeenCalled()

    const dropped = fakeSubscription(bytes(serverKey))
    browser('granted', dropped)
    const gone = fakeApi({ pushUnsubscribe: vi.fn(async () => { throw new ApiError(404, 'push.unsubscribe: store: not found') }) })
    expect(await disablePush(gone)).toEqual({ kind: 'off' })
    expect(dropped.unsubscribe).toHaveBeenCalled()
  })

  it('keeps the browser subscribed when the server could not be told', async () => {
    const held = fakeSubscription(bytes(serverKey))
    browser('granted', held)
    const down = fakeApi({ pushUnsubscribe: vi.fn(async () => { throw new ApiError(503, 'push.unsubscribe: unavailable') }) })
    await expect(disablePush(down)).rejects.toThrow('push.unsubscribe: unavailable')
    expect(held.unsubscribe).not.toHaveBeenCalled()
  })

  it('tests the subscription this browser holds', async () => {
    browser('granted', fakeSubscription(bytes(serverKey)))
    const api = fakeApi()
    await testPush(api)
    expect(api.pushTest).toHaveBeenCalledWith(endpoint)
  })
})

describe('watchPush', () => {
  const caps: GatewayCapabilities = { gateway: 'server', methods: ['*'], ws: [] }

  it('opens the run a tapped notification names', () => {
    const env = browser('granted')
    const store = createRootStore()
    const stop = watchPush(store, fakeApi())

    env.serviceWorker.dispatchEvent(new MessageEvent('message', { data: { type: 'aether:open-run', run: 'run-7k2m9q4xbd' } }))
    expect(store.getState().route).toEqual({ name: 'run', params: { runId: 'run-7k2m9q4xbd' } })

    stop()
    env.serviceWorker.dispatchEvent(new MessageEvent('message', { data: { type: 'aether:open-run', run: 'run-other' } }))
    expect(store.getState().route.params.runId).toBe('run-7k2m9q4xbd')
  })

  it('tells the server a person is here, at most once every 20 seconds', () => {
    vi.useFakeTimers({ now: 1_800_000_000_000 })
    const store = createRootStore()
    const api = fakeApi()
    const stop = watchPush(store, api)

    // Before the dashboard knows who it is, there is nobody to report.
    window.dispatchEvent(new Event('keydown'))
    expect(api.pushActive).not.toHaveBeenCalled()

    store.setState({ info: serverInfo, capabilities: caps })
    window.dispatchEvent(new Event('pointermove'))
    window.dispatchEvent(new Event('keydown'))
    expect(api.pushActive).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(20_000)
    window.dispatchEvent(new Event('wheel'))
    expect(api.pushActive).toHaveBeenCalledTimes(2)

    stop()
    vi.advanceTimersByTime(20_000)
    window.dispatchEvent(new Event('pointerdown'))
    expect(api.pushActive).toHaveBeenCalledTimes(2)
  })

  it('stops reporting to a server that refuses the report', async () => {
    vi.useFakeTimers({ now: 1_800_000_000_000 })
    const store = createRootStore()
    store.setState({ info: serverInfo, capabilities: caps })
    const api = fakeApi({ pushActive: vi.fn(async () => { throw new ApiError(404, 'push.active: method not found: push.active', -32601) }) })
    const stop = watchPush(store, api)

    window.dispatchEvent(new Event('keydown'))
    await vi.advanceTimersByTimeAsync(20_000)
    window.dispatchEvent(new Event('keydown'))

    expect(api.pushActive).toHaveBeenCalledTimes(1)
    stop()
  })
})

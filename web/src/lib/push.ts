// Web Push for this browser: whether it can receive notifications, whether
// it does, and what the dashboard does around them. The server side is
// internal/push; the worker that shows them is public/sw.js.

import { desktopBridge } from '@/components/shell/window-bar'
import { ApiError, type Api } from '@/lib/api'
import { runRoute } from '@/routes/run/views'
import type { RootStore } from '@/store'
import { capability } from '@/store/hooks'

export type PushState =
  | { kind: 'on' | 'off' | 'blocked' }
  | { kind: 'unsupported'; why: 'desktop' | 'gateway' | 'home-screen' | 'browser' }

const worker = '/sw.js'

/** iOS and iPadOS offer push only to a dashboard opened from the home screen. */
function needsHomeScreen(): boolean {
  const apple =
    /iPhone|iPad|iPod/.test(navigator.userAgent) ||
    (navigator.userAgent.includes('Macintosh') && navigator.maxTouchPoints > 1)
  return apple && !window.matchMedia('(display-mode: standalone)').matches
}

async function subscription(): Promise<PushSubscription | null> {
  const registration = await navigator.serviceWorker.getRegistration(worker)
  return (await registration?.pushManager.getSubscription()) ?? null
}

/**
 * `gateway` is the capabilities' name for what serves this dashboard. Only
 * the server's own has an address a notification can open later: `aether
 * gui` is a loopback port behind a per-process token.
 */
export async function readPush(client: Api, gateway: string | undefined): Promise<PushState> {
  if (desktopBridge()) return { kind: 'unsupported', why: 'desktop' }
  if (gateway !== 'server') return { kind: 'unsupported', why: 'gateway' }
  if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) {
    return { kind: 'unsupported', why: needsHomeScreen() ? 'home-screen' : 'browser' }
  }
  if (Notification.permission === 'denied') return { kind: 'blocked' }
  const current = await subscription()
  if (!current) return { kind: 'off' }
  // The server forgets a device the push service reported gone.
  const { subscribed } = await client.pushStatus(current.endpoint)
  return { kind: subscribed ? 'on' : 'off' }
}

function decodeKey(key: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(atob(key.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0))
}

function sameKey(held: ArrayBuffer | null, key: Uint8Array): boolean {
  if (!held || held.byteLength !== key.length) return false
  const bytes = new Uint8Array(held)
  return key.every((byte, i) => byte === bytes[i])
}

export async function enablePush(client: Api): Promise<PushState> {
  // Safari grants the prompt only straight from the tap, before any other await.
  const permission = await Notification.requestPermission()
  if (permission === 'denied') return { kind: 'blocked' }
  if (permission !== 'granted') return { kind: 'off' }
  await navigator.serviceWorker.register(worker)
  const registration = await navigator.serviceWorker.ready
  const key = decodeKey((await client.pushStatus()).public_key)
  let current = await registration.pushManager.getSubscription()
  // A subscription made for another server key cannot be sent to by this one.
  if (current && !sameKey(current.options.applicationServerKey, key)) {
    await current.unsubscribe()
    current = null
  }
  current ??= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
  await client.pushSubscribe(current.toJSON())
  return { kind: 'on' }
}

export async function disablePush(client: Api): Promise<PushState> {
  const current = await subscription()
  if (current) {
    await client.pushUnsubscribe(current.endpoint).catch((err: unknown) => {
      if (!(err instanceof ApiError) || err.status !== 404) throw err
    })
    await current.unsubscribe()
  }
  return { kind: 'off' }
}

export async function testPush(client: Api): Promise<void> {
  const current = await subscription()
  if (!current) throw new Error('This device has no push subscription. Turn notifications off and on again.')
  await client.pushTest(current.endpoint)
}

const activityEvents = ['pointerdown', 'pointermove', 'keydown', 'wheel'] as const
const activityEveryMs = 20_000
const methodNotFound = -32601
const denied = -32001

/**
 * Opens the run a tapped notification names, which the worker posts to a
 * dashboard that is already open, and tells the server while a person is
 * using this one: it holds their notifications until they have left every
 * dashboard alone for a minute.
 */
export function watchPush(store: RootStore, client: Api): () => void {
  const open = (event: MessageEvent) => {
    const data = event.data as { type?: unknown; run?: unknown } | null
    if (data?.type !== 'aether:open-run' || typeof data.run !== 'string') return
    const route = runRoute(data.run)
    store.getState().navigate(route.name, route.params)
  }
  let reported = 0
  let refused = false
  const active = () => {
    const now = Date.now()
    const s = store.getState()
    if (refused || now - reported < activityEveryMs || !s.info || !capability(s.capabilities).hasMethod('push.active')) return
    reported = now
    // An older server has no such method and a pending member may not call
    // it. Neither has notifications to hold, so that refusal is the last.
    client.pushActive().catch((err: unknown) => {
      refused = err instanceof ApiError && (err.code === methodNotFound || err.code === denied)
    })
  }
  navigator.serviceWorker?.addEventListener('message', open)
  for (const type of activityEvents) window.addEventListener(type, active, { capture: true, passive: true })
  return () => {
    navigator.serviceWorker?.removeEventListener('message', open)
    for (const type of activityEvents) window.removeEventListener(type, active, { capture: true })
  }
}

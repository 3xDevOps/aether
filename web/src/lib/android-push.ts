// Push in the Android app, whose WebView has no Push API. The app registers
// the phone with a UnifiedPush distributor and hands the endpoint to
// push.subscribe itself (android/.../PushBridge.kt); the page only asks it
// through window.AetherAndroid, which the app offers to this origin alone.

export type AndroidPushState = {
  kind: 'on' | 'off' | 'blocked' | 'no-distributor' | 'pending'
  endpoint?: string
  error?: string
}

interface Bridge {
  postMessage(message: string): void
  addEventListener(type: 'message', listener: (event: MessageEvent<string>) => void): void
  removeEventListener(type: 'message', listener: (event: MessageEvent<string>) => void): void
}

function bridge(): Bridge | undefined {
  return (window as { AetherAndroid?: Bridge }).AetherAndroid
}

export function androidShell(): boolean {
  return bridge() !== undefined
}

// Enabling can wait on Android's permission prompt and a distributor picker.
const settleMs = 90_000

/**
 * Calls listener with every settled state the app sends, such as the one it
 * sends on returning to the front after notifications were allowed in
 * Android's settings.
 */
export function watchAndroidPush(listener: (state: AndroidPushState) => void): () => void {
  const app = bridge()
  if (!app) return () => {}
  const listen = (event: MessageEvent<string>) => {
    const state = JSON.parse(event.data) as AndroidPushState
    if (state.kind !== 'pending') listener(state)
  }
  app.addEventListener('message', listen)
  return () => app.removeEventListener('message', listen)
}

/** Sends op and resolves with the first settled state the app sends back. */
export function androidPush(op: 'state' | 'enable' | 'disable'): Promise<AndroidPushState> {
  const app = bridge()
  if (!app) return Promise.reject(new Error('Not running in the Aether Android app.'))
  return new Promise((resolve, reject) => {
    const done = () => {
      clearTimeout(timer)
      app.removeEventListener('message', listen)
    }
    const listen = (event: MessageEvent<string>) => {
      const state = JSON.parse(event.data) as AndroidPushState
      if (state.kind === 'pending') return
      done()
      resolve(state)
    }
    const timer = setTimeout(() => {
      done()
      reject(new Error('The Android app did not answer within 90 seconds.'))
    }, settleMs)
    app.addEventListener('message', listen)
    app.postMessage(JSON.stringify({ op }))
  })
}

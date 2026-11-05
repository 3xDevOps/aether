import { useEffect, useState } from 'react'
import { desktopBridge } from '@/components/shell/window-bar'
import { pushChanged } from '@/lib/push'
import { useAskingCount } from '@/routes/needs-you/attention'

type Badging = Navigator & { setAppBadge?: (count: number) => Promise<void>; clearAppBadge?: () => Promise<void> }

let latest = 0

/** Whether this browser can badge an app icon, and nothing else badges it. */
export const badgeSupported = () => !desktopBridge() && 'setAppBadge' in navigator && 'clearAppBadge' in navigator

/** Whether the app badge applies here: the browser can badge and this device holds a push subscription. */
export function useBadging(): boolean {
  const [on, setOn] = useState(false)
  useEffect(() => {
    if (!badgeSupported()) return
    let live = true
    const read = () => {
      navigator.serviceWorker?.getRegistration('/sw.js')
        .then((registration) => registration?.pushManager.getSubscription())
        .then((subscription) => live && setOn(Boolean(subscription)), () => live && setOn(false))
    }
    read()
    window.addEventListener(pushChanged, read)
    return () => {
      live = false
      window.removeEventListener(pushChanged, read)
    }
  }, [])
  return on
}

/** Sets the installed app's badge to count while this device has a push subscription, and clears it otherwise. Rejects when the browser refuses. */
export async function syncAppBadge(count: number): Promise<void> {
  const nav: Badging = navigator
  if (desktopBridge() || !nav.setAppBadge || !nav.clearAppBadge) return
  const mine = ++latest
  try {
    const registration = await navigator.serviceWorker?.getRegistration('/sw.js')
    const subscribed = await registration?.pushManager.getSubscription()
    if (mine !== latest) return
    await (subscribed && count > 0 ? nav.setAppBadge(count) : nav.clearAppBadge())
  } catch (err) {
    throw new Error(`set the app badge to ${count}: ${err instanceof Error ? err.message : String(err)}`, { cause: err })
  }
}

/** Renders nothing: the badge follows the Needs you count in any layout. The count is only fresh while an attention feed runs. */
export function AppBadge() {
  const count = useAskingCount()
  useEffect(() => {
    const sync = () => syncAppBadge(count).catch((err: unknown) => console.error(err))
    sync()
    document.addEventListener('visibilitychange', sync)
    window.addEventListener(pushChanged, sync)
    return () => {
      document.removeEventListener('visibilitychange', sync)
      window.removeEventListener(pushChanged, sync)
    }
  }, [count])
  useEffect(() => () => void syncAppBadge(0).catch((err: unknown) => console.error(err)), [])
  return null
}

import { useEffect } from 'react'
import { desktopBridge } from '@/components/shell/window-bar'
import { pushChanged } from '@/lib/push'
import { useAskingCount, useAttentionFeed } from '@/routes/needs-you/attention'

type Badging = Navigator & { setAppBadge?: (count: number) => Promise<void>; clearAppBadge?: () => Promise<void> }

/** Sets the installed app's badge to count while this device has a push subscription, and clears it otherwise. */
export async function syncAppBadge(count: number): Promise<void> {
  const nav: Badging = navigator
  if (desktopBridge() || !nav.setAppBadge || !nav.clearAppBadge) return
  try {
    const registration = await navigator.serviceWorker?.getRegistration('/sw.js')
    const subscribed = await registration?.pushManager.getSubscription()
    await (subscribed && count > 0 ? nav.setAppBadge(count) : nav.clearAppBadge())
  } catch (err) {
    console.error('Could not set the app badge:', err)
  }
}

/** Renders nothing: the badge follows the Needs you count in any layout, so the count is read here and not in the phone shell. */
export function AppBadge() {
  useAttentionFeed()
  const count = useAskingCount()
  useEffect(() => {
    const sync = () => void syncAppBadge(count)
    sync()
    document.addEventListener('visibilitychange', sync)
    window.addEventListener(pushChanged, sync)
    return () => {
      document.removeEventListener('visibilitychange', sync)
      window.removeEventListener(pushChanged, sync)
    }
  }, [count])
  useEffect(() => () => void syncAppBadge(0), [])
  return null
}

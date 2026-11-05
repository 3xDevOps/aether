// The installed app's icon badge mirrors the Needs you count while this
// device has a push subscription. The push payload carries no count, so
// the page sets it; a closed app keeps the last value until it is opened.

import { useEffect } from 'react'
import { useAskingCount } from '@/routes/needs-you/attention'

type Badging = Navigator & { setAppBadge?: (count: number) => Promise<void>; clearAppBadge?: () => Promise<void> }

export async function syncAppBadge(count: number): Promise<void> {
  const nav: Badging = navigator
  if (!nav.setAppBadge || !nav.clearAppBadge) return
  const subscribed = await navigator.serviceWorker?.getRegistration('/sw.js').then((r) => r?.pushManager.getSubscription())
  await (subscribed && count > 0 ? nav.setAppBadge(count) : nav.clearAppBadge()).catch(() => {})
}

export function useAppBadge(): void {
  const count = useAskingCount()
  useEffect(() => {
    void syncAppBadge(count)
  }, [count])
}

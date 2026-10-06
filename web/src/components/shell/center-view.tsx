import { useEffect, useRef } from 'react'
import { withheld } from '@/lib/surfaces'
import { redirectRoute } from '@/lib/url-state'
import { lookupRoute } from '@/routes'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const terminalRouteName = 'terminal'

export function focusView() {
  const main = document.getElementById('main')
  ;(main?.querySelector<HTMLElement>('h1[tabindex]') ?? main)?.focus({ preventScroll: true })
}

export function CenterView() {
  const route = useStore((s) => s.route)
  const identityKey = useStore((s) => s.identityKey)
  const terminalCacheEpoch = useStore((s) => s.terminalCacheEpoch)
  const hydrated = useStore((s) => s.hydrated)
  const cap = useCapability()
  const View = lookupRoute(route.name)
  const unavailable = !View || (hydrated && withheld(route.name, cap))
  const viewKey =
    route.name === terminalRouteName
      ? JSON.stringify([
          route.name,
          identityKey,
          terminalCacheEpoch,
          route.params.runId ?? '',
        ])
      : route.name

  // A view that took focus for itself keeps it; otherwise the heading takes
  // it so a screen reader announces where the navigation landed.
  const shown = useRef(JSON.stringify(route))
  useEffect(() => {
    const key = JSON.stringify(route)
    if (shown.current === key) return
    shown.current = key
    if (document.getElementById('main')?.contains(document.activeElement)) return
    focusView()
  }, [route])

  useEffect(() => {
    if (unavailable) redirectRoute(useStore, { name: 'board', params: {} })
  }, [unavailable, route])

  return (
    <div className="relative h-full">
      {View && !unavailable && <View key={viewKey} params={route.params} />}
    </div>
  )
}

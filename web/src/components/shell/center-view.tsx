import { lookupRoute } from '@/routes'
import { useStore } from '@/store'

const terminalRouteName = 'terminal'

export function CenterView() {
  const route = useStore((s) => s.route)
  const identityKey = useStore((s) => s.identityKey)
  const terminalCacheEpoch = useStore((s) => s.terminalCacheEpoch)
  const View = lookupRoute(route.name)
  const viewKey =
    route.name === terminalRouteName
      ? JSON.stringify([
          route.name,
          identityKey,
          terminalCacheEpoch,
          route.params.runId ?? '',
        ])
      : route.name

  return (
    <div className="relative h-full">
      {View && <View key={viewKey} params={route.params} />}
      {!View && (
        <p className="p-4 text-sm text-muted-foreground">
          No view registered for “{route.name}”.
        </p>
      )}
    </div>
  )
}

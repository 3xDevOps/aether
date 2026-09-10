import { lookupRoute } from '@/routes'
import { useStore } from '@/store'

export function CenterView() {
  const route = useStore((s) => s.route)
  const View = lookupRoute(route.name)

  if (!View) {
    return (
      <p className="p-4 text-sm text-muted-foreground">
        No view registered for “{route.name}”.
      </p>
    )
  }
  return (
    <div className="relative h-full">
      <View params={route.params} />
    </div>
  )
}

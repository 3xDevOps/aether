import { useEffect, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { lookupRoute } from '@/routes'
import { useStore } from '@/store'

export function CenterView() {
  const route = useStore((s) => s.route)
  const View = lookupRoute(route.name)
  const flash = useRevealFlash(`${route.name}:${JSON.stringify(route.params)}`)

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
      <div
        aria-hidden
        className={cn(
          'pointer-events-none absolute inset-0 z-30 bg-foreground/10 transition-opacity duration-500',
          flash ? 'opacity-100 duration-0' : 'opacity-0',
        )}
      />
    </div>
  )
}

/**
 * The GUI spec's "brief focus flash": every reveal lands on this one view,
 * so a navigation briefly lights the revealed content to confirm what a
 * card or row click just jumped to. True for one frame, then fades out.
 */
function useRevealFlash(key: string): boolean {
  const [flash, setFlash] = useState(false)
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    setFlash(true)
    const timer = setTimeout(() => setFlash(false), 50)
    return () => clearTimeout(timer)
  }, [key])
  return flash
}

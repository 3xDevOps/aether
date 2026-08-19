import { useEffect, useState } from 'react'

/**
 * Styleguide rule: match in-flight feedback to perceived duration - a spinner
 * that flashes for 60ms is worse than none. True only once `active` has held
 * for `delayMs`.
 */
export function useDelayed(active: boolean, delayMs = 200): boolean {
  const [shown, setShown] = useState(false)
  useEffect(() => {
    if (!active) {
      setShown(false)
      return
    }
    const timer = setTimeout(() => setShown(true), delayMs)
    return () => clearTimeout(timer)
  }, [active, delayMs])
  return shown
}

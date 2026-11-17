import { useEffect, useState } from 'react'
import { Opening } from '@/components/launch-splash-opening'
import { desktopBridge } from '@/components/shell/window-bar'
import { prefersReducedMotion } from '@/lib/hooks'
import { useStore } from '@/store'

/** Held this long so a fast hydrate reads as a beat rather than a flicker. */
const MIN_VISIBLE_MS = 600
/**
 * Hard ceiling on the splash. It covers the frameless window's title bar, so
 * while it is up the member cannot drag, minimize or close the window and
 * cannot read the connection error. The store only reports a failed hydrate
 * after the stream has retried for several seconds, and a socket that hangs
 * never reports at all, so nothing else brings the window controls back.
 */
const MAX_VISIBLE_MS = 2500
/** The fade in index.css runs 260ms; unmount as it finishes. */
const FADE_MS = 260

const SHOWN_KEY = 'aether.launchSplashShown'

/**
 * The splash belongs to the desktop shell opening, not to loading: a browser
 * tab and every reload inside the same shell session skip it. Reading storage
 * throws in some embedded contexts, and a decoration is never worth a crash.
 */
function firstLaunch(): boolean {
  if (!desktopBridge()) return false
  if (prefersReducedMotion()) return false
  try {
    return window.sessionStorage.getItem(SHOWN_KEY) === null
  } catch {
    return false
  }
}

export function LaunchSplash() {
  // Read at first render, marked in an effect: StrictMode runs both twice, and
  // a claim made while rendering would hide the splash from its own remount.
  const [show] = useState(firstLaunch)
  const hydrated = useStore((s) => s.hydrated)
  // A hydrate that failed is also over: the connection error page below is
  // what the member needs to read, not an opening that never ends.
  const failed = useStore((s) => s.hydrationError !== null)
  const [held, setHeld] = useState(true)
  const [capped, setCapped] = useState(false)
  const [leaving, setLeaving] = useState(false)
  const [removed, setRemoved] = useState(false)

  useEffect(() => {
    // The shell session counts as launched whether or not the splash played,
    // so a reduced-motion launch does not leave the key unwritten and make the
    // next reload in the same window look like a first launch.
    if (!desktopBridge()) return
    try {
      window.sessionStorage.setItem(SHOWN_KEY, '1')
    } catch {
      // Storage is blocked; the splash repeats rather than the app breaking.
    }
    if (!show) return
    const hold = window.setTimeout(() => setHeld(false), MIN_VISIBLE_MS)
    const cap = window.setTimeout(() => setCapped(true), MAX_VISIBLE_MS)
    return () => {
      window.clearTimeout(hold)
      window.clearTimeout(cap)
    }
  }, [show])

  useEffect(() => {
    if (!show || held || !(hydrated || failed || capped)) return
    setLeaving(true)
    const remove = window.setTimeout(() => setRemoved(true), FADE_MS)
    return () => window.clearTimeout(remove)
  }, [show, held, hydrated, failed, capped])

  if (!show || removed) return null

  return (
    <div className={`launch-splash${leaving ? ' launch-splash--leaving' : ''}`} aria-hidden="true">
      <Opening />
    </div>
  )
}

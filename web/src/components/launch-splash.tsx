import { useEffect, useState } from 'react'
import { desktopBridge } from '@/components/shell/title-bar'
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
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return false
  try {
    return window.sessionStorage.getItem(SHOWN_KEY) === null
  } catch {
    return false
  }
}

const stars = [
  ['14%', '16%', 'mint'],
  ['8%', '74%', 'pale'],
  ['61%', '31%', 'violet'],
  ['49%', '89%', 'amber'],
  ['81%', '53%', 'pale'],
  ['72%', '7%', 'mint'],
  ['27%', '44%', 'pale'],
  ['90%', '82%', 'violet'],
  ['38%', '5%', 'amber'],
] as const

export function LaunchSplash() {
  // Read at first render, marked in an effect: StrictMode runs both twice, and
  // a claim made while rendering would hide the splash from its own remount.
  const [show] = useState(firstLaunch)
  const hydrated = useStore((s) => s.hydrated)
  // A hydrate that failed is also over: the connection error page below is
  // what the member needs to read, not a night sky that never ends.
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
      <div className="launch-splash__sky" aria-hidden="true">
        <img className="launch-splash__grain" src="/grain.png" alt="" />
        <div className="launch-splash__cloud launch-splash__cloud--one">
          <img src="/cloud-soft-1.png" alt="" />
        </div>
        <div className="launch-splash__cloud launch-splash__cloud--two">
          <img src="/cloud-soft-2.png" alt="" />
        </div>
        <div className="launch-splash__cloud launch-splash__cloud--three">
          <img src="/cloud-soft-3.png" alt="" />
        </div>
        <div className="launch-splash__starfield" data-testid="launch-splash-stars" />
        <div className="launch-splash__big-stars">
          {stars.map(([top, left, color], index) => (
            <span
              key={`${top}-${left}`}
              className={`launch-splash__big-star launch-splash__big-star--${color}`}
              style={{ top, left, animationDelay: `${-index * 0.55}s` }}
            />
          ))}
        </div>
        <span className="launch-splash__satellite launch-splash__satellite--pale" />
        <span className="launch-splash__satellite launch-splash__satellite--mint" />
        <div className="launch-splash__shooting-stars" data-testid="launch-splash-shooting-stars">
          <span className="launch-splash__shooting-star launch-splash__shooting-star--one" />
          <span className="launch-splash__shooting-star launch-splash__shooting-star--two" />
          <span className="launch-splash__shooting-star launch-splash__shooting-star--three" />
        </div>
      </div>

      <div className="launch-splash__logo">
        <img src="/aether-mark.png" alt="" />
        <span className="launch-splash__wordmark">aether</span>
      </div>
    </div>
  )
}

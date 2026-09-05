import { useEffect, useState } from 'react'

const EXIT_START_MS = 2000
const REMOVE_MS = 2250

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
  const [leaving, setLeaving] = useState(false)
  const [visible, setVisible] = useState(true)

  useEffect(() => {
    if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
      setVisible(false)
      return
    }

    const exit = window.setTimeout(() => setLeaving(true), EXIT_START_MS)
    const remove = window.setTimeout(() => setVisible(false), REMOVE_MS)
    return () => {
      window.clearTimeout(exit)
      window.clearTimeout(remove)
    }
  }, [])

  if (!visible) return null

  return (
    <div
      className={`launch-splash${leaving ? ' launch-splash--leaving' : ''}`}
      role="status"
      aria-label="Launching Aether"
    >
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
        <img src="/aether-mark.png" alt="Aether" />
        <span className="launch-splash__wordmark">aether</span>
      </div>
    </div>
  )
}

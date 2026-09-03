import type { LucideIcon } from 'lucide-react'
import { Copy, Minus, Square, X } from 'lucide-react'
import { type CSSProperties, useEffect, useState } from 'react'
import { cn } from '@/lib/utils'

/** The window buttons, present only when the shell draws none of its own. */
export type DesktopControls = {
  minimize: () => void
  toggleMaximize: () => void
  close: () => void
  isMaximized: () => Promise<boolean>
  /** Fires on maximize/unmaximize; returns its own unsubscribe. */
  onMaximizedChange: (cb: (maximized: boolean) => void) => () => void
}

/**
 * What `desktop/preload.js` puts on `window`. Declared here rather than as a
 * global augmentation: the bar is the bridge's main reader, so the shape it
 * depends on is documented beside the code depending on it.
 */
export type AetherDesktop = {
  platform: string
  /** Absent on darwin, where the native traffic lights are kept. */
  controls?: DesktopControls
  /**
   * The CLI version that built this shell, without the leading "v" (for
   * example "1.2.3"). A shell a dev CLI built keeps the manifest's own
   * "0.1.0" rather than going absent, so it reads as stale against any
   * release, which it is. Absent only in a browser tab, where the whole
   * bridge is. The update banner compares it with the CLI serving the
   * gateway, because a newer CLI needs `aether gui build` run again.
   */
  shellVersion?: string
}

/** The desktop bridge, or undefined in a browser tab. */
export function desktopBridge(): AetherDesktop | undefined {
  return (window as Window & { aetherDesktop?: AetherDesktop }).aetherDesktop
}

// `-webkit-app-region` is non-standard, so csstype does not declare it and
// Tailwind has no utility for it. Chromium is the only renderer here.
const DRAG = { WebkitAppRegion: 'drag' } as CSSProperties
const NO_DRAG = { WebkitAppRegion: 'no-drag' } as CSSProperties

/** One window button: a fixed 46px cell over the full bar height. */
function ControlButton({
  label,
  icon: Icon,
  onClick,
  className,
}: {
  label: string
  icon: LucideIcon
  onClick: () => void
  className?: string
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      style={NO_DRAG}
      className={cn(
        'grid h-full w-[46px] place-items-center text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground',
        className,
      )}
    >
      <Icon size={14} aria-hidden />
    </button>
  )
}

/**
 * The desktop shell's title bar. That window is frameless, so this bar is the
 * only way to move or close the app, and it renders above the whole SPA -
 * error page included. A browser tab has no bridge and gets no bar: it
 * already has the browser's own chrome.
 */
export function TitleBar() {
  const desktop = desktopBridge()
  const controls = desktop?.controls
  const [maximized, setMaximized] = useState(false)

  useEffect(() => {
    if (!controls) return
    let live = true
    controls.isMaximized().then((value) => {
      // The query can resolve after the bar is gone; a set then is a warning
      // at best and a write into a dead tree at worst.
      if (live) setMaximized(value)
    })
    const stop = controls.onMaximizedChange(setMaximized)
    return () => {
      live = false
      stop()
    }
  }, [controls])

  if (!desktop) return null

  return (
    <header
      aria-label="Aether"
      style={{
        ...DRAG,
        // macOS keeps its native traffic lights; the bar leaves room for them
        // rather than letting the lockup sit underneath.
        paddingInlineStart: controls ? undefined : '78px',
      }}
      className="flex h-9 shrink-0 select-none items-center border-b bg-background"
    >
      <div className="flex items-center gap-2.5 px-3">
        <img src="/aether-mark.png" alt="" aria-hidden className="h-[18px] w-auto" />
        <span className="font-pixel text-[19px] leading-none text-foreground">aether</span>
      </div>

      {controls && (
        <div className="ml-auto flex h-full items-center">
          <ControlButton label="Minimize" icon={Minus} onClick={controls.minimize} />
          <ControlButton
            label={maximized ? 'Restore' : 'Maximize'}
            icon={maximized ? Copy : Square}
            onClick={controls.toggleMaximize}
          />
          <ControlButton
            label="Close"
            icon={X}
            onClick={controls.close}
            className="hover:bg-destructive hover:text-destructive-foreground"
          />
        </div>
      )}
    </header>
  )
}

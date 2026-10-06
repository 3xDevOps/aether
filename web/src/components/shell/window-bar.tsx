import { type CSSProperties, useEffect, useState } from 'react'
import { Copy, Minus, Square, X } from '@/components/icons'
import { Button } from '@/components/ui/button'

/** The window buttons, present only when the shell draws none of its own. */
export type DesktopControls = {
  minimize: () => void
  toggleMaximize: () => void
  close: () => void
  isMaximized: () => Promise<boolean>
  /** Fires on maximize/unmaximize; returns its own unsubscribe. */
  onMaximizedChange: (cb: (maximized: boolean) => void) => () => void
}

/** What `desktop/preload.js` puts on `window`. */
export type AetherDesktop = {
  platform: string
  /** Absent on darwin, where the native traffic lights are kept. */
  controls?: DesktopControls
  /**
   * The CLI version that built this shell, without the leading "v". A shell
   * a dev CLI built keeps the manifest's own "0.1.0", so it reads as stale
   * against any release, which it is. Absent only in a browser tab.
   */
  shellVersion?: string
  /**
   * Opens the shell's native directory dialog and resolves to the chosen
   * folder's absolute path, or "" when it was cancelled. Absent in a shell
   * built before the method existed.
   */
  chooseFolder?: () => Promise<string>
}

export function desktopBridge(): AetherDesktop | undefined {
  return (window as Window & { aetherDesktop?: AetherDesktop }).aetherDesktop
}

// `-webkit-app-region` is non-standard, so csstype does not declare it.
const DRAG = { WebkitAppRegion: 'drag' } as CSSProperties
const NO_DRAG = { WebkitAppRegion: 'no-drag' } as CSSProperties

/**
 * The frameless desktop window's top edge: the strip the window is dragged
 * by, with the window buttons on Windows and Linux and room for the native
 * traffic lights on macOS. A browser tab has neither and renders nothing.
 */
export function WindowBar() {
  const desktop = desktopBridge()
  const controls = desktop?.controls
  const [maximized, setMaximized] = useState(false)

  useEffect(() => {
    if (!controls) return
    let live = true
    void controls.isMaximized().then((value) => {
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
    <div
      data-slot="window-bar"
      style={DRAG}
      className="flex h-[var(--window-bar-height)] shrink-0 select-none items-center justify-end border-b border-seam bg-chrome"
    >
      {controls && (
        <div style={NO_DRAG} className="flex h-full items-center gap-1 px-1">
          <Button variant="ghost" size="icon" label="Minimize" onClick={controls.minimize}>
            <Minus />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            label={maximized ? 'Restore' : 'Maximize'}
            onClick={controls.toggleMaximize}
          >
            {maximized ? <Copy /> : <Square />}
          </Button>
          <Button variant="ghost" size="icon" label="Close" onClick={controls.close}>
            <X />
          </Button>
        </div>
      )}
    </div>
  )
}

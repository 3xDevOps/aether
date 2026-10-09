import { useEffect, useState } from 'react'
import { Copy, Minus, Square, X } from '@/components/icons'
import { Button } from '@/components/ui/button'

export type DesktopControls = {
  minimize: () => void
  toggleMaximize: () => void
  close: () => void
  isMaximized: () => Promise<boolean>
  onMaximizedChange: (cb: (maximized: boolean) => void) => () => void
}

/** What `desktop/preload.js` puts on `window`. */
export type AetherDesktop = {
  platform: string
  /** Absent on darwin, where the native traffic lights are kept. */
  controls?: DesktopControls
  /** No leading "v"; a dev-built shell reports the manifest's "0.1.0". */
  shellVersion?: string
  /** Resolves to "" on cancel. Absent in shells built before it existed. */
  chooseFolder?: () => Promise<string>
}

export function desktopBridge(): AetherDesktop | undefined {
  return (window as Window & { aetherDesktop?: AetherDesktop }).aetherDesktop
}

export function WindowControls() {
  const controls = desktopBridge()?.controls
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

  if (!controls) return null
  return (
    <div
      data-slot="window-controls"
      className="ml-auto flex shrink-0 items-center gap-1 [-webkit-app-region:no-drag]"
    >
      <Button variant="ghost" size="icon" label="Minimize" onClick={() => controls.minimize()}>
        <Minus />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        label={maximized ? 'Restore' : 'Maximize'}
        onClick={() => controls.toggleMaximize()}
      >
        {maximized ? <Copy /> : <Square />}
      </Button>
      <Button variant="ghost" size="icon" label="Close" onClick={() => controls.close()}>
        <X />
      </Button>
    </div>
  )
}

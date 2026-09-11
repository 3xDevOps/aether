import type { LucideIcon } from 'lucide-react'
import { Copy, Minus, Square, X } from 'lucide-react'
import { type CSSProperties, useEffect, useState } from 'react'
import { Tooltip } from '@/components/ui/heroui'
import { CommandPaletteTrigger } from '@/components/palette'
import { cn, focusRing } from '@/lib/utils'
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
  /**
   * Opens the shell's native directory dialog and resolves to the chosen
   * folder's absolute path, or "" when it was cancelled. Absent in a browser
   * tab with the rest of the bridge, and in a shell built before the method
   * existed, so every caller keeps a typed fallback.
   */
  chooseFolder?: () => Promise<string>
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
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            aria-label={label}
            onClick={() => {
              onClick()
            }}
            style={NO_DRAG}
            className={cn(
              focusRing,
              'grid h-full w-[46px] place-items-center text-muted-foreground transition-colors hover:bg-toolbar-hover hover:text-foreground',
              className,
            )}
          >
            <Icon size={14} aria-hidden />
          </button>
        )}
      />
      <Tooltip.Content>{label}</Tooltip.Content>
    </Tooltip>
  )
}
/**
 * The workbench title/command bar. Electron supplies a frameless window, so
 * its drag region and native controls are drawn here. Browsers receive the
 * same compact command bar, but never fake window controls.
 */
export function TitleBar({
  commandPaletteDisabled = false,
}: {
  commandPaletteDisabled?: boolean
} = {}) {
  const desktop = desktopBridge()
  const controls = desktop?.controls
  const [maximized, setMaximized] = useState(false)

  useEffect(() => {
    if (!controls) return
    let live = true
    controls.isMaximized().then((value) => {
      if (live) setMaximized(value)
    })
    const stop = controls.onMaximizedChange(setMaximized)
    return () => {
      live = false
      stop()
    }
  }, [controls])

  return (
    <header
      aria-label="Aether"
      style={{
        ...(desktop ? DRAG : {}),
        // macOS keeps its native traffic lights; reserve their inset only for
        // the Electron bar, never for a browser viewport.
        ...(desktop && !controls
          ? { paddingInlineStart: '78px', paddingInlineEnd: '78px' }
          : {}),
      }}
      className={cn(
        // viewport-fit=cover paints the bar under a landscape notch; the
        // insets are 0 everywhere else, and the Electron inline padding above
        // wins where it applies.
        'relative z-50 grid h-[35px] shrink-0 select-none grid-cols-[minmax(36px,1fr)_minmax(0,600px)_minmax(36px,1fr)] items-center border-b border-border bg-sidebar pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)] text-foreground max-[767px]:grid-cols-[36px_minmax(0,1fr)_36px]',
        desktop && 'backdrop-blur',
      )}
    >
      <div className="flex min-w-0 shrink items-center gap-2 px-3 max-[767px]:justify-center max-[767px]:px-0">
        <img src="/aether-mark.png" alt="" aria-hidden className="h-4 w-auto shrink-0" />
        <span className="font-pixel text-[18px] leading-none tracking-wide text-foreground max-[767px]:hidden">
          aether
        </span>
      </div>

      <div className="flex min-w-0 w-full items-center justify-center">
        <div style={NO_DRAG} className="flex min-w-0 w-full max-w-[600px]">
          <CommandPaletteTrigger disabled={commandPaletteDisabled} />
        </div>
      </div>

      {controls && (
        <div className="flex h-full shrink-0 items-center justify-self-end">
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

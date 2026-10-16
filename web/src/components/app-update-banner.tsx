// The update prompt for a CLI an app installs, the Android app's sidecar.
// The gateway reports the release but never installs it; the button opens
// the page that does - the Play listing or the GitHub release - and the
// member updates there.

import { useState } from 'react'
import { Smartphone } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  banner,
  bannerActions,
  bannerContent,
  bannerIcon,
  Dismiss,
  packagedLine,
  verbatim,
} from '@/components/update-banner-shared'
import { message } from '@/lib/format'
import type { UpdateStatus } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

/** `window.aetherAndroid`, android/app/src/main/kotlin/io/aether/android/UpdateBridge.kt. */
export interface AetherAndroid {
  /** `play` for a Google Play install, `debug` for a debug build, `release` for anything else. */
  updateSource(): string
  /** Opens the page that updates to the tag; "" once it opened, else why not. */
  openUpdate(tag: string): string
}

export function androidBridge(): AetherAndroid | undefined {
  return (window as Window & { aetherAndroid?: AetherAndroid }).aetherAndroid
}

export function AppUpdateBanner({ update }: { update: UpdateStatus }) {
  const { latest = '', packaged = '', release_url: releaseUrl } = update.cli
  const dismissed = useStore((s) => s.dismissedUpdates.cli)
  const [error, setError] = useState('')
  if (!update.cli.update_available || !latest || dismissed === latest) return null

  const bridge = androidBridge()
  const source = bridge?.updateSource()
  const play = source === 'play'
  const open = () => {
    try {
      setError(bridge?.openUpdate(latest) ?? '')
    } catch (err) {
      setError(message(err))
    }
  }

  return (
    <div role="status" className={banner}>
      <div aria-hidden className={bannerIcon}>
        <Smartphone className="size-4" />
      </div>
      <div className={bannerContent}>
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <p className="font-medium">Aether {latest} is available.</p>
          <p className="text-ui-sm text-muted">You are running {update.cli.version}.</p>
        </div>
        <p className="text-ui-sm leading-5 text-muted">
          {!bridge
            ? packagedLine(packaged)
            : play
              ? 'Google Play installs it once the release reaches Play; until then Play offers no update. Nothing is downloaded until you update there.'
              : source === 'debug'
                ? 'This is a debug build. The release APK is signed with another key, so Android will not install it over this app: uninstall this one first, which deletes its data, or rebuild with make android-debug.'
                : 'Download aether-android.apk from the release page and open it to install over this app. Your settings and sign-in stay.'}
        </p>
        {error && <p className={cn(verbatim, 'text-state-failed')}>{error}</p>}
      </div>
      <div className={bannerActions}>
        {bridge && (
          <Button size="sm" onClick={open}>
            {play ? 'Open Google Play' : 'Open release page'}
          </Button>
        )}
        {releaseUrl && (play || !bridge) && (
          <a
            href={releaseUrl}
            target="_blank"
            rel="noreferrer"
            className={cn(
              focusRing,
              'inline-flex min-h-[22px] items-center whitespace-nowrap px-1 text-ui-sm underline underline-offset-2 hover:text-text',
            )}
          >
            Release notes
          </a>
        )}
      </div>
      <Dismiss kind="cli" version={latest} />
    </div>
  )
}

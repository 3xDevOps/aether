// The template form stays with the palette: its open state is local to that
// host, not in the store's dialog union.

import { ClearDoneDialog, ReleaseFinishedDialog } from '@/components/palette/clear-done-dialog'
import { CloseDialog } from '@/components/palette/close-dialog'
import { CloseConfirmation } from '@/components/close-warnings'
import { ForwardDialog } from '@/components/palette/forward-dialog'
import { LaunchDialog } from '@/components/launch/launch-dialog'
import { InjectDialog } from '@/components/palette/inject-dialog'
import { WatchDialog } from '@/components/palette/watch-dialog'
import { useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { useIsMobile } from '@/lib/breakpoints'
import { useLingering } from '@/components/ui/slide-out'
import { useStore } from '@/store'

export function PaletteDialogs() {
  const dialog = useStore((s) => s.paletteDialog)
  const identity = useStore((s) => s.identityKey)
  const draft = useStore((s) => s.launchDraft)
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const phone = useIsMobile()
  const [sidebar, setSidebar] = useState(() => document.getElementById('sidebar'))
  useLayoutEffect(() => setSidebar(document.getElementById('sidebar')), [collapsed, phone])
  const active = dialog === 'launch' || draft !== null
  const retained = useLingering(active)
  const launchOpen = dialog === 'launch'
  const launchShown = useLingering(launchOpen)
  // Discarding starts a fresh form even if its closing slide has not finished.
  const opened = useRef({ open: false, count: 0 })
  if (active && !opened.current.open) opened.current.count++
  opened.current.open = active
  return (
    <>
      {/* A different member's launch starts over from their own accounts and saved choices. */}
      {retained && <LaunchDialog key={`${identity ?? ''}:${opened.current.count}`} open={launchOpen} visible={launchShown} />}
      {!phone && !launchShown && draft && createPortal(
        <Button
          variant="secondary"
          size="icon"
          label="Resume new session draft"
          className={`top-16 z-40 h-11 w-8 rounded-l-none border-l-0 shadow-overlay ${sidebar ? 'absolute left-full' : 'fixed left-0'}`}
          onClick={() => useStore.getState().openPaletteDialog('launch')}
        ><ChevronRight /></Button>,
        sidebar ?? document.body,
      )}
      {dialog === 'inject' && <InjectDialog />}
      {dialog === 'forward' && <ForwardDialog />}
      {dialog === 'close' && <CloseDialog />}
      {dialog === 'clear-done' && <ClearDoneDialog />}
      {dialog === 'release-finished' && <ReleaseFinishedDialog />}
      {dialog === 'watch' && <WatchDialog />}
      <CloseConfirmation />
    </>
  )
}

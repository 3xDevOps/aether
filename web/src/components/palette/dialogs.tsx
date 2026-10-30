// The template form stays with the palette: its open state is local to that
// host, not in the store's dialog union.

import { ClearDoneDialog, ReleaseFinishedDialog } from '@/components/palette/clear-done-dialog'
import { CloseDialog } from '@/components/palette/close-dialog'
import { ForwardDialog } from '@/components/palette/forward-dialog'
import { LaunchDialog } from '@/components/launch/launch-dialog'
import { InjectDialog } from '@/components/palette/inject-dialog'
import { WatchDialog } from '@/components/palette/watch-dialog'
import { useRef } from 'react'
import { useLingering } from '@/components/ui/slide-out'
import { useStore } from '@/store'

export function PaletteDialogs() {
  const dialog = useStore((s) => s.paletteDialog)
  const identity = useStore((s) => s.identityKey)
  const launchOpen = dialog === 'launch' || dialog === 'swarm'
  const launchShown = useLingering(launchOpen)
  // Opening again while the last panel is still sliding out starts a fresh form, not the old one mid-launch.
  const opened = useRef({ open: false, count: 0 })
  if (launchOpen && !opened.current.open) opened.current.count++
  opened.current.open = launchOpen
  return (
    <>
      {/* A different member's launch starts over from their own accounts and saved choices. */}
      {launchShown && <LaunchDialog key={`${identity ?? ''}:${opened.current.count}`} open={launchOpen} />}
      {dialog === 'inject' && <InjectDialog />}
      {dialog === 'forward' && <ForwardDialog />}
      {dialog === 'close' && <CloseDialog />}
      {dialog === 'clear-done' && <ClearDoneDialog />}
      {dialog === 'release-finished' && <ReleaseFinishedDialog />}
      {dialog === 'watch' && <WatchDialog />}
    </>
  )
}

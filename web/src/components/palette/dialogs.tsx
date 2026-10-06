// The template form stays with the palette: its open state is local to that
// host, not in the store's dialog union.

import { ClearDoneDialog, ReleaseFinishedDialog } from '@/components/palette/clear-done-dialog'
import { CloseDialog } from '@/components/palette/close-dialog'
import { ForwardDialog } from '@/components/palette/forward-dialog'
import { LaunchDialog } from '@/components/palette/launch-dialog'
import { InjectDialog } from '@/components/palette/inject-dialog'
import { useStore } from '@/store'

export function PaletteDialogs() {
  const dialog = useStore((s) => s.paletteDialog)
  return (
    <>
      {(dialog === 'launch' || dialog === 'swarm') && <LaunchDialog />}
      {dialog === 'inject' && <InjectDialog />}
      {dialog === 'forward' && <ForwardDialog />}
      {dialog === 'close' && <CloseDialog />}
      {dialog === 'clear-done' && <ClearDoneDialog />}
      {dialog === 'release-finished' && <ReleaseFinishedDialog />}
    </>
  )
}

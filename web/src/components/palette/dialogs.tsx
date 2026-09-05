// The store-driven forms, hosted where every surface can reach them. They
// used to live inside the palette, which made a New run button in the sidebar
// depend on the status bar being on screen; they belong to the shell instead,
// so `openPaletteDialog('launch')` works from anywhere. The template form
// stays with the palette: its open state is local to that host, not in the
// store's dialog union.

import { CloseDialog } from '@/components/palette/close-dialog'
import { ForwardDialog } from '@/components/palette/forward-dialog'
import { LaunchDialog } from '@/components/palette/launch-dialog'
import { InjectDialog } from '@/components/palette/inject-dialog'
import { useStore } from '@/store'

export function PaletteDialogs() {
  const dialog = useStore((s) => s.paletteDialog)
  return (
    <>
      {dialog === 'launch' && <LaunchDialog />}
      {dialog === 'inject' && <InjectDialog />}
      {dialog === 'forward' && <ForwardDialog />}
      {dialog === 'close' && <CloseDialog />}
    </>
  )
}

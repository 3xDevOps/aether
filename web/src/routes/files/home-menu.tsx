import { useRef } from 'react'
import { Ellipsis } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { copyText } from '@/lib/clipboard'
import { useHomeFiles } from '@/routes/files/home'
import { baseName, tildePath } from '@/routes/files/sources'
import { useStore } from '@/store'

export type RowKind = 'root' | 'dir' | 'file' | 'link'

/** A Home row's actions, behind its … button and its right click. */
export function HomeMenu({
  path,
  kind,
  locked,
  open,
  onOpenChange,
}: {
  path: string
  kind: RowKind
  locked: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const home = useHomeFiles()
  const refreshHome = useStore((s) => s.refreshHome)
  const folder = kind === 'root' || kind === 'dir'
  const readable = kind === 'file' && !locked
  const renamable = kind !== 'root' && !locked
  // A closing menu takes focus back to its button, which would close the
  // field an item has just opened; the field opens once the menu is gone.
  const afterClose = useRef<(() => void) | null>(null)
  const thenName = (start: () => void) => () => {
    afterClose.current = start
  }
  return (
    <Menu open={open} onOpenChange={onOpenChange}>
      <MenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" label={kind === 'root' ? 'Home actions' : `Actions for ${baseName(path)}`}>
          <Ellipsis />
        </Button>
      </MenuTrigger>
      <MenuContent
        align="start"
        onCloseAutoFocus={(event) => {
          const start = afterClose.current
          afterClose.current = null
          if (!start) return
          event.preventDefault()
          start()
        }}
      >
        {folder && (
          <>
            <MenuItem onSelect={thenName(() => home.create(path, 'file'))}>New file</MenuItem>
            <MenuItem onSelect={thenName(() => home.create(path, 'dir'))}>New folder</MenuItem>
            <MenuItem disabled={home.uploading} onSelect={() => home.pickUpload(path)}>Upload files…</MenuItem>
          </>
        )}
        {kind === 'root' && <MenuItem onSelect={refreshHome}>Refresh</MenuItem>}
        {readable && <MenuItem onSelect={() => home.download(path)}>Download</MenuItem>}
        {renamable && (
          <>
            {(folder || readable) && <MenuSeparator />}
            <MenuItem onSelect={thenName(() => home.rename(path))}>Rename</MenuItem>
          </>
        )}
        <MenuItem onSelect={() => void copyText(tildePath(path), null)}>Copy path</MenuItem>
        {kind !== 'root' && (
          <>
            <MenuSeparator />
            <MenuItem tone="danger" onSelect={() => home.remove(path, kind === 'dir')}>Delete</MenuItem>
          </>
        )}
      </MenuContent>
    </Menu>
  )
}

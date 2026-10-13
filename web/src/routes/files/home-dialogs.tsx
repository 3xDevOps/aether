import { useState, type ReactNode } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import type { ButtonProps } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import type { Upload } from '@/routes/files/home-upload'
import { baseName, childPath, parentPath, tildePath } from '@/routes/files/sources'
import { useExpansion } from '@/routes/files/tree'
import { sameIdentity, useStore } from '@/store'
import { homeKey } from '@/store/files'

/** Asks before a change to the home. A refusal stays in the dialog, which
 * stays open. */
function Confirm({
  title,
  action,
  variant,
  cancel = 'Cancel',
  onConfirm,
  onClose,
  children,
}: {
  title: string
  action: string
  variant?: ButtonProps['variant']
  cancel?: string
  onConfirm: () => Promise<void>
  onClose: () => void
  children: ReactNode
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const returnFocus = useReturnFocus()
  return (
    <AlertDialog open onOpenChange={() => !busy && onClose()}>
      <AlertDialogContent {...returnFocus}>
        <AlertDialogHeader>
          <AlertDialogTitle className="break-words">{title}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="flex min-w-0 flex-col gap-2 break-words">{children}</div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error !== null && <Callout tone="failed" role="alert">{error}</Callout>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{cancel}</AlertDialogCancel>
          <AlertDialogAction
            variant={variant}
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              setBusy(true)
              setError(null)
              void onConfirm().then(onClose, (err) => {
                setBusy(false)
                setError(errorSentence(err))
              })
            }}
          >
            {action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

const Path = ({ children }: { children: string }) => <span className="font-code text-text">{children}</span>

export function DeleteDialog({ path, dir, client, onClose }: { path: string; dir: boolean; client: Api; onClose: () => void }) {
  const unsaved = useStore(
    (s) =>
      s.fileTabs.filter((tab) => {
        const draft = s.drafts[tab.key]
        return tab.kind === 'home' && (tab.path === path || tab.path.startsWith(`${path}/`)) && draft && draft.content !== draft.baseContent
      }).length,
  )
  return (
    <Confirm
      title={`Delete ${baseName(path)}?`}
      action="Delete"
      onClose={onClose}
      onConfirm={async () => {
        const same = sameIdentity()
        await client.homeRemove(path)
        if (!same()) return
        const store = useStore.getState()
        store.dropHomePath(path)
        store.invalidateTree(homeKey(parentPath(path)))
      }}
    >
      <p>
        <Path>{tildePath(path)}</Path>
        {dir ? ' and everything in it will be deleted.' : ' will be deleted.'} This cannot be undone.
      </p>
      {unsaved > 0 && (
        <p>
          {!dir
            ? 'Its unsaved edits will be discarded.'
            : unsaved === 1
              ? 'An open file in it has unsaved edits, which will be discarded.'
              : `${unsaved} open files in it have unsaved edits, which will be discarded.`}
        </p>
      )}
    </Confirm>
  )
}

export function MoveDialog({ from, dir, client, onClose }: { from: string; dir: string; client: Api; onClose: () => void }) {
  const expansion = useExpansion()
  const to = childPath(dir, baseName(from))
  return (
    <Confirm
      title={`Move ${baseName(from)} to ${tildePath(dir)}?`}
      action="Move"
      variant="primary"
      onClose={onClose}
      onConfirm={async () => {
        const same = sameIdentity()
        await client.homeRename(from, to)
        if (!same()) return
        const store = useStore.getState()
        store.moveHomePath(from, to)
        expansion.move(homeKey(from), homeKey(to))
        store.invalidateTree(homeKey(parentPath(from)))
        store.invalidateTree(homeKey(dir))
      }}
    >
      <p>
        <Path>{tildePath(from)}</Path> becomes <Path>{tildePath(to)}</Path>.
      </p>
    </Confirm>
  )
}

export function ReplaceDialog({ dir, uploads, onReplace, onSkip }: { dir: string; uploads: Upload[]; onReplace: () => void; onSkip: () => void }) {
  return (
    <Confirm
      title={uploads.length === 1 ? 'Replace 1 file?' : `Replace ${uploads.length} files?`}
      action="Replace"
      cancel="Skip"
      onClose={onSkip}
      onConfirm={async () => onReplace()}
    >
      <p>
        {uploads.length === 1 ? 'This file already exists' : 'These files already exist'} in <Path>{tildePath(dir)}</Path>:
      </p>
      <ul className="max-h-40 overflow-y-auto font-code text-text">
        {uploads.map((upload) => (
          <li key={upload.path}>{upload.path}</li>
        ))}
      </ul>
    </Confirm>
  )
}

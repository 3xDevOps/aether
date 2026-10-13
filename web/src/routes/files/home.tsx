import { createContext, useContext, useEffect, useRef, useState, type DragEvent, type ReactNode } from 'react'
import { toast } from 'sonner'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { DeleteDialog, MoveDialog, ReplaceDialog } from '@/routes/files/home-dialogs'
import { droppedUploads, useHomeUpload, type UploadState } from '@/routes/files/home-upload'
import { baseName, childPath, parentPath } from '@/routes/files/sources'
import { useExpansion } from '@/routes/files/tree'
import { sameIdentity, useStore } from '@/store'
import { homeKey } from '@/store/files'

/** What the tree's inline field is naming: a new entry in `parent`, or `path`. */
export type Naming = { kind: 'file' | 'dir'; parent: string } | { kind: 'rename'; path: string }

/** Marks a drag of a Home row, as opposed to files dragged from the desktop. */
const rowDrag = 'application/x-aether-home-path'

const expandAfterMs = 600

interface Home {
  client: Api
  naming: Naming | null
  /** The folder a drag in progress would land in. */
  dropTarget: string | null
  uploads: UploadState
  uploading: boolean
  /** The path being fetched for the browser to save. */
  downloading: string | null
  dismissFailures: () => void
  create: (parent: string, kind: 'file' | 'dir') => void
  rename: (path: string) => void
  cancelNaming: () => void
  commitName: (name: string) => Promise<void>
  remove: (path: string, dir: boolean) => void
  download: (path: string) => void
  pickUpload: (dir: string) => void
  /** True once, for the row that gets the keyboard back after naming ends. */
  takeFocus: (path: string) => boolean
  dragStart: (path: string, event: DragEvent) => void
  dragEnd: () => void
  dropHandlers: Pick<React.ComponentProps<'section'>, 'onDragOver' | 'onDragLeave' | 'onDrop'>
}

const HomeFilesContext = createContext<Home | null>(null)

export function useHomeFiles(): Home {
  const home = useContext(HomeFilesContext)
  if (!home) throw new Error('useHomeFiles outside HomeFiles')
  return home
}

/** Owns every change the Home tree makes, above the tree itself: a phone
 * remounts the tree in a sheet, and an upload or a dialog must outlive that. */
export function HomeFiles({ client, onOpen, children }: { client: Api; onOpen: (path: string) => void; children: ReactNode }) {
  const expansion = useExpansion()
  const [naming, setNaming] = useState<Naming | null>(null)
  const [removing, setRemoving] = useState<{ path: string; dir: boolean } | null>(null)
  const [moving, setMoving] = useState<{ from: string; dir: string } | null>(null)
  const [dropTarget, setDropTarget] = useState<string | null>(null)
  const [downloading, setDownloading] = useState<string | null>(null)
  const uploader = useHomeUpload(client)
  const picker = useRef<HTMLInputElement>(null)
  const pickedFor = useRef('')
  const dragged = useRef<string | null>(null)
  const focus = useRef<string | null>(null)

  useEffect(() => {
    if (dropTarget === null) return
    const timer = window.setTimeout(() => expansion.set(homeKey(dropTarget), true), expandAfterMs)
    return () => window.clearTimeout(timer)
  }, [dropTarget])

  // A file dropped anywhere else would replace the dashboard with that file.
  useEffect(() => {
    const refuse = (event: globalThis.DragEvent) => {
      if (event.defaultPrevented || !event.dataTransfer?.types.includes('Files')) return
      event.preventDefault()
      event.dataTransfer.dropEffect = 'none'
    }
    window.addEventListener('dragover', refuse)
    window.addEventListener('drop', refuse)
    return () => {
      window.removeEventListener('dragover', refuse)
      window.removeEventListener('drop', refuse)
    }
  }, [])

  const endNaming = (focusPath: string | null) => {
    focus.current = focusPath
    setNaming(null)
  }

  const commitName = async (name: string) => {
    if (!naming) return
    const same = sameIdentity()
    const store = useStore.getState()
    if (naming.kind === 'rename') {
      const from = naming.path
      const to = childPath(parentPath(from), name)
      if (to !== from) {
        await client.homeRename(from, to)
        if (!same()) return
        store.moveHomePath(from, to)
        expansion.move(homeKey(from), homeKey(to))
        store.invalidateTree(homeKey(parentPath(from)))
      }
      return endNaming(to)
    }
    const path = childPath(naming.parent, name)
    if (naming.kind === 'dir') {
      await client.homeMkdir(path)
      if (!same()) return
    } else {
      const created = await client.homeWrite({ path, content: '', revision: '' })
      if (!same()) return
      store.setDocument(homeKey(path), { ...created, loading: false, error: undefined })
      onOpen(path)
    }
    store.invalidateTree(homeKey(naming.parent))
    endNaming(naming.kind === 'dir' ? path : null)
  }

  /** The folder a drag would land in: the folder under the pointer, or the
   * one holding the file under it. Null when the drag cannot land there. */
  const landing = (event: DragEvent): string | null => {
    const dir = (event.target as Element).closest<HTMLElement>('[data-home-dir]')?.dataset.homeDir ?? ''
    const from = dragged.current
    if (event.dataTransfer.types.includes(rowDrag)) {
      return from === null || dir === from || dir.startsWith(`${from}/`) || dir === parentPath(from) ? null : dir
    }
    return event.dataTransfer.types.includes('Files') && !uploader.busy ? dir : null
  }

  const value: Home = {
    client,
    naming,
    dropTarget,
    uploads: uploader.state,
    uploading: uploader.busy,
    downloading,
    dismissFailures: uploader.dismissFailures,
    create: (parent, kind) => {
      expansion.set(homeKey(parent), true)
      setNaming({ kind, parent })
    },
    rename: (path) => setNaming({ kind: 'rename', path }),
    cancelNaming: () => endNaming(naming === null ? null : naming.kind === 'rename' ? naming.path : naming.parent),
    commitName,
    remove: (path, dir) => setRemoving({ path, dir }),
    download: (path) => {
      setDownloading(path)
      void client
        .homeDownload(path)
        .then(
          (result) => {
            const bytes = Uint8Array.from(atob(result.content_base64), (char) => char.charCodeAt(0))
            const link = document.createElement('a')
            link.href = URL.createObjectURL(new Blob([bytes]))
            link.download = baseName(path)
            link.click()
            // Some engines read the blob only after the click has returned.
            window.setTimeout(() => URL.revokeObjectURL(link.href), 60_000)
          },
          (err) => toast.error(errorSentence(err)),
        )
        .finally(() => setDownloading(null))
    },
    pickUpload: (dir) => {
      pickedFor.current = dir
      picker.current?.click()
    },
    takeFocus: (path) => {
      if (focus.current !== path) return false
      focus.current = null
      return true
    },
    dragStart: (path, event) => {
      dragged.current = path
      event.dataTransfer.setData(rowDrag, path)
      event.dataTransfer.effectAllowed = 'move'
    },
    dragEnd: () => {
      dragged.current = null
      setDropTarget(null)
    },
    dropHandlers: {
      onDragOver: (event) => {
        const dir = landing(event)
        setDropTarget(dir)
        if (dir === null) return
        event.preventDefault()
        event.dataTransfer.dropEffect = dragged.current === null ? 'copy' : 'move'
      },
      onDragLeave: (event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropTarget(null)
      },
      onDrop: (event) => {
        const dir = landing(event)
        setDropTarget(null)
        if (dir === null) return
        event.preventDefault()
        if (dragged.current !== null) setMoving({ from: dragged.current, dir })
        else {
          void droppedUploads(event.dataTransfer).then(
            (dropped) => uploader.upload(dir, dropped),
            (err) => toast.error(errorSentence(err)),
          )
        }
      },
    },
  }

  return (
    <HomeFilesContext.Provider value={value}>
      {children}
      <input
        ref={picker}
        type="file"
        multiple
        hidden
        aria-label="Files to upload"
        onChange={(event) => {
          uploader.upload(pickedFor.current, [...(event.target.files ?? [])].map((file) => ({ file, path: file.name })))
          event.target.value = ''
        }}
      />
      {removing && <DeleteDialog {...removing} client={client} onClose={() => setRemoving(null)} />}
      {moving && <MoveDialog {...moving} client={client} onClose={() => setMoving(null)} />}
      {uploader.state.conflicts && <ReplaceDialog {...uploader.state.conflicts} onReplace={uploader.replace} onSkip={uploader.skip} />}
    </HomeFilesContext.Provider>
  )
}

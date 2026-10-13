import { useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronRight, File, FileSymlink, Folder, House, Lock, RefreshCw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { ListRow } from '@/components/ui/list-row'
import { Spinner } from '@/components/ui/spinner'
import type { FileTreeEntry } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useHomeFiles } from '@/routes/files/home'
import { HomeMenu, type RowKind } from '@/routes/files/home-menu'
import { NameRow } from '@/routes/files/home-name'
import { baseName, childPath, homeSource, type SelectFile, type Selection } from '@/routes/files/sources'
import { indent, ListingStatus, scrollToRow, useExpanded, useListing } from '@/routes/files/tree'
import { useStore } from '@/store'
import { homeKey } from '@/store/files'

const lockedHint = 'Credential file. Files lists it but does not open it; use the Environment terminal.'
const linkHint = 'Symlink. Files does not follow links; use the Environment terminal.'

const fileSpacer = <span className="w-3.5 shrink-0" />

const dropTargetClass = 'bg-accent-soft outline-1 -outline-offset-1 outline-accent'

/** The member's whole persistent home under one root, with the file
 * operations the other trees do not have. */
export function HomeTree({ onSelect, selected }: { onSelect: SelectFile; selected: Selection | null }) {
  const home = useHomeFiles()
  const selectedPath = selected?.kind === 'home' ? selected.path : null
  return (
    <section aria-label="Home" className="flex flex-1 flex-col" {...home.dropHandlers}>
      <HomeDirectory path="" depth={0} selectedPath={selectedPath} onOpen={(path) => onSelect(homeSource, path)} />
    </section>
  )
}

function HomeDirectory({
  path,
  depth,
  selectedPath,
  onOpen,
}: {
  path: string
  depth: number
  selectedPath: string | null
  onOpen: (path: string) => void
}) {
  const home = useHomeFiles()
  const [expanded, toggle] = useExpanded(homeKey(path), false)
  const listing = useListing(homeSource, path, expanded, home.client)
  const creating = home.naming !== null && home.naming.kind !== 'rename' && home.naming.parent === path ? home.naming.kind : null
  const root = path === ''
  return (
    <div data-home-dir={path} className={cn('rounded-control', root && 'flex-1', home.dropTarget === path && dropTargetClass)}>
      <HomeRow
        path={path}
        kind={root ? 'root' : 'dir'}
        depth={depth}
        expanded={expanded}
        onClick={toggle}
        leading={
          <>
            <ChevronRight className={cn(expanded && 'rotate-90')} />
            {root ? <House /> : <Folder />}
          </>
        }
      />
      {expanded && (
        <div>
          {creating && (
            <NameRow
              depth={depth + 1}
              label={creating === 'dir' ? 'New folder name' : 'New file name'}
              leading={<>{fileSpacer}{creating === 'dir' ? <Folder /> : <File />}</>}
              onCommit={home.commitName}
              onCancel={home.cancelNaming}
            />
          )}
          <ListingStatus listing={listing} depth={depth} />
          {listing?.entries.map((entry) => {
            const child = childPath(path, entry.name)
            return entry.kind === 'dir' ? (
              <HomeDirectory key={child} path={child} depth={depth + 1} selectedPath={selectedPath} onOpen={onOpen} />
            ) : (
              <HomeLeaf key={child} path={child} entry={entry} depth={depth + 1} selected={child === selectedPath} onOpen={onOpen} />
            )
          })}
        </div>
      )}
    </div>
  )
}

function HomeLeaf({ path, entry, depth, selected, onOpen }: { path: string; entry: FileTreeEntry; depth: number; selected: boolean; onOpen: (path: string) => void }) {
  const link = entry.kind === 'link'
  return (
    <HomeRow
      path={path}
      kind={link ? 'link' : 'file'}
      locked={entry.locked}
      depth={depth}
      selected={selected}
      onClick={link ? undefined : () => onOpen(path)}
      leading={
        <>
          {fileSpacer}
          {link ? <span role="img" aria-label="Symlink" title={linkHint} className="flex"><FileSymlink /></span> : <File />}
        </>
      }
      trailing={entry.locked ? <span role="img" aria-label="Credential file" title={lockedHint} className="flex"><Lock /></span> : undefined}
    />
  )
}

function HomeRow({
  path,
  kind,
  locked = false,
  depth,
  expanded,
  selected = false,
  onClick,
  leading,
  trailing,
}: {
  path: string
  kind: RowKind
  locked?: boolean
  depth: number
  expanded?: boolean
  selected?: boolean
  onClick?: () => void
  leading: ReactNode
  trailing?: ReactNode
}) {
  const home = useHomeFiles()
  const refreshHome = useStore((s) => s.refreshHome)
  const [menu, setMenu] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (selected) scrollToRow(button.current)
  }, [selected])
  useEffect(() => {
    if (button.current && home.takeFocus(path)) button.current.focus()
  })
  const root = kind === 'root'
  const name = root ? homeSource.label : baseName(path)
  if (home.naming?.kind === 'rename' && home.naming.path === path) {
    return <NameRow depth={depth} label={`New name for ${name}`} initial={name} leading={leading} onCommit={home.commitName} onCancel={home.cancelNaming} />
  }
  const row = (
    <ListRow
      ref={button}
      selected={selected}
      aria-expanded={expanded}
      aria-keyshortcuts={root ? undefined : 'F2 Delete'}
      style={indent(depth)}
      title={name}
      onClick={onClick}
      onContextMenu={(event) => {
        event.preventDefault()
        setMenu(true)
      }}
      onKeyDown={(event) => {
        if (root) return
        if (event.key === 'F2') home.rename(path)
        if (event.key === 'Delete') home.remove(path, kind === 'dir')
      }}
      leading={leading}
      trailing={root ? '~' : trailing}
      hoverAction={
        <>
          {root && (
            <Button variant="ghost" size="icon-sm" label="Refresh Home" onClick={refreshHome}>
              <RefreshCw />
            </Button>
          )}
          <HomeMenu path={path} kind={kind} locked={locked} open={menu} onOpenChange={setMenu} />
        </>
      }
    >
      {name}
    </ListRow>
  )
  if (root) return row
  return (
    <div draggable onDragStart={(event) => home.dragStart(path, event)} onDragEnd={home.dragEnd}>
      {row}
    </div>
  )
}

/** Upload progress and what could not be uploaded, pinned under the tree. */
export function HomeStatus() {
  const { uploads, dismissFailures } = useHomeFiles()
  if (!uploads.progress && uploads.failures.length === 0) return null
  return (
    <div className="sticky bottom-0 flex flex-col gap-2 border-t border-seam bg-chrome p-2">
      {uploads.failures.length > 0 && (
        <Callout
          tone="failed"
          role="alert"
          title={uploads.failures.length === 1 ? '1 file was not uploaded' : `${uploads.failures.length} files were not uploaded`}
          actions={<Button size="sm" variant="ghost" onClick={dismissFailures}>Dismiss</Button>}
        >
          <ul className="max-h-32 overflow-y-auto text-ui-sm">
            {uploads.failures.map((failure) => (
              <li key={failure}>{failure}</li>
            ))}
          </ul>
        </Callout>
      )}
      {uploads.progress && (
        <p role="status" className="flex min-w-0 items-center gap-2 text-ui-sm text-muted">
          <Spinner />
          <span className="min-w-0 truncate">
            Uploading {uploads.progress.at} of {uploads.progress.of} · {uploads.progress.name}
          </span>
        </p>
      )}
    </div>
  )
}

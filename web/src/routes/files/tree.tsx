import { createContext, useContext, useEffect, useRef, useState, type ComponentProps, type DragEvent, type ReactNode } from 'react'
import { ChevronRight, File, Folder, GitBranch, Upload } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { ListRow } from '@/components/ui/list-row'
import { Spinner } from '@/components/ui/spinner'
import { ApiError, type Api } from '@/lib/api'
import { useRefuseStrayFileDrops } from '@/lib/file-drag'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { runLabel } from '@/lib/status'
import type { Run, Workspace } from '@/lib/types'
import { chevronTurn, cn } from '@/lib/utils'
import { droppedUploads, useUpload, type UploadState } from '@/routes/files/home-upload'
import { baseName, type FileSource, isLiveRun, type SelectFile, type Selection, sourceKey, type WorkspaceSource } from '@/routes/files/sources'
import { useStore } from '@/store'
import type { FileTreeState } from '@/store/files'
import { useCapability, useSelf } from '@/store/hooks'

interface Expansion {
  open: (key: string, fallback: boolean) => boolean
  set: (key: string, open: boolean) => void
  /** Keeps a renamed folder, and the folders under it, as open as they were. */
  move: (from: string, to: string) => void
}

const ExpansionContext = createContext<Expansion>({ open: (_, fallback) => fallback, set: () => {}, move: () => {} })

/** Keeps folder expansion across the phone sheet remounting the tree, and
 * opens the folders above `reveal`, a file that was opened from outside it. */
export function TreeExpansion({ reveal, children }: { reveal?: Selection | null; children: ReactNode }) {
  const [state, setState] = useState<Record<string, boolean>>({})
  const revealed = reveal ? sourceKey(reveal, reveal.path) : null
  useEffect(() => {
    if (!reveal) return
    const folders = reveal.path.split('/').slice(0, -1)
    const above = folders.map((_, depth) => sourceKey(reveal, folders.slice(0, depth + 1).join('/')))
    setState((s) => ({ ...s, ...Object.fromEntries([sourceKey(reveal, ''), ...above].map((key) => [key, true])) }))
  }, [revealed])
  const value: Expansion = {
    open: (key, fallback) => state[key] ?? fallback,
    set: (key, open) => setState((s) => ({ ...s, [key]: open })),
    move: (from, to) =>
      setState((s) =>
        Object.fromEntries(
          Object.entries(s).map(([key, open]) => [key === from || key.startsWith(`${from}/`) ? to + key.slice(from.length) : key, open]),
        ),
      ),
  }
  return <ExpansionContext.Provider value={value}>{children}</ExpansionContext.Provider>
}

export const useExpansion = () => useContext(ExpansionContext)

export function useExpanded(key: string, fallback: boolean): [boolean, () => void] {
  const { open, set } = useExpansion()
  const expanded = open(key, fallback)
  return [expanded, () => set(key, !expanded)]
}

export const indent = (depth: number) => ({ paddingLeft: 8 + depth * 12 })

export function FileTree({
  workspaces,
  runs,
  home,
  client,
  onSelect,
  selected,
}: {
  workspaces: Workspace[]
  runs: Run[]
  home?: ReactNode
  client: Api
  onSelect: SelectFile
  selected: Selection | null
}) {
  const several = workspaces.length > 1
  return (
    <div className="flex flex-1 flex-col gap-3 p-2">
      {workspaces.map((workspace) => {
        const checkouts = runs.filter((run) => run.workspace_id === workspace.id)
        const base = { kind: 'workspace' as const, workspaceID: workspace.id, runID: '', label: `base: ${workspace.base_branch}`, branch: workspace.base_branch }
        return (
          <section key={workspace.id} aria-label={workspace.name} className="flex flex-col">
            {several && <p className="truncate px-2 pb-1 text-ui-sm font-medium text-muted">{workspace.name}</p>}
            <TreeDirectory source={base} path="" depth={0} client={client} onSelect={onSelect} selected={selected} icon={<GitBranch />} />
            {checkouts.length > 0 && (
              <Disclosure id={`runs:${workspace.id}`} label="Session checkouts" count={checkouts.length}>
                {checkouts.map((run) => (
                  <RunCheckoutTree
                    key={run.id}
                    source={{ kind: 'workspace', workspaceID: workspace.id, runID: run.id, label: runLabel(run) }}
                    depth={1}
                    rootOpen={false}
                    client={client}
                    onSelect={onSelect}
                    selected={selected}
                    touched={touchedPaths(run.id)}
                  />
                ))}
              </Disclosure>
            )}
          </section>
        )
      })}
      {home}
    </div>
  )
}

function Disclosure({ id, label, count, children }: { id: string; label: string; count: number; children: ReactNode }) {
  const [open, toggle] = useExpanded(`disclosure:${id}`, false)
  return (
    <Collapsible open={open} onOpenChange={toggle}>
      <CollapsibleTrigger>
        <span className="min-w-0 flex-1 truncate text-ui-sm font-medium text-muted">{label}</span>
        <span className="pr-1 text-ui-sm text-muted tabular-nums">{count}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>{children}</CollapsibleContent>
    </Collapsible>
  )
}

export function touchedPaths(runID: string): Set<string> {
  const snapshots = useStore.getState().diffs[runID]?.snapshots
  return new Set(snapshots?.[0]?.files.map((file) => file.path) ?? [])
}

const noPaths = new Set<string>()

export const scrollToRow = (row: HTMLElement | null) => row?.scrollIntoView({ block: 'nearest' })

/** Reads one directory while it is expanded and whenever its cached listing
 * is dropped. The last listing this member read stays up while the next is
 * read, so an open folder does not blink. */
export function useListing(source: FileSource, path: string, expanded: boolean, client: Api): FileTreeState | undefined {
  const key = sourceKey(source, path)
  const cached = useStore((s) => s.trees[key])
  const filesEpoch = useStore((s) => s.filesEpoch)
  const identityEpoch = useStore((s) => s.identityEpoch)
  const read = useRef<{ identity: number; tree: FileTreeState } | null>(null)
  if (cached && !cached.loading) read.current = { identity: identityEpoch, tree: cached }
  const setTree = useStore((s) => s.setTree)
  const sourceKind = source.kind
  const workspaceID = source.kind === 'workspace' ? source.workspaceID : undefined
  const runID = source.kind === 'workspace' ? source.runID : undefined
  const requestID = useRef(0)
  useEffect(() => {
    if (!expanded || cached?.loading || cached?.entries || cached?.error) return
    setTree(key, { entries: [], loading: true, error: undefined })
    const pending = useStore.getState().trees[key]
    const requestToken = ++requestID.current
    const requestIdentityEpoch = identityEpoch
    const stale = () =>
      requestID.current !== requestToken ||
      useStore.getState().identityEpoch !== requestIdentityEpoch ||
      useStore.getState().trees[key] !== pending
    const request = source.kind === 'home'
      ? client.homeTree(path)
      : client.filesTree({ workspace_id: source.workspaceID, ...(source.runID ? { run_id: source.runID } : {}), ...(source.repo ? { repo: source.repo } : {}), path })
    void request
      .then((result) => {
        if (!stale()) setTree(key, { entries: result.entries, loading: false, error: undefined })
      })
      .catch((err) => {
        if (!stale()) setTree(key, { entries: [], loading: false, error: message(err) })
      })
  }, [cached, client, expanded, filesEpoch, identityEpoch, key, path, runID, setTree, sourceKind, workspaceID])
  return (!cached || cached.loading) && read.current?.identity === identityEpoch ? read.current.tree : cached
}

export function ListingStatus({ listing, depth }: { listing: FileTreeState | undefined; depth: number }) {
  return (
    <>
      {listing?.loading && <p className="py-1 text-ui-sm text-muted" style={indent(depth + 2)}>Loading files…</p>}
      {listing?.error && <p role="alert" className="py-1 text-ui-sm text-state-failed" style={indent(depth + 2)}>{listing.error}</p>}
    </>
  )
}

export const dropTargetClass = 'bg-accent-soft outline-1 -outline-offset-1 outline-accent'

/** Transfers in progress and what could not be uploaded, pinned under a tree. */
export function UploadStatus({ uploads, downloading = null, dismissFailures }: { uploads: UploadState; downloading?: string | null; dismissFailures: () => void }) {
  const progress = uploads.progress
    ? uploads.progress.of === 0
      ? 'Reading dropped files…'
      : `Uploading ${uploads.progress.at} of ${uploads.progress.of} · ${uploads.progress.name}`
    : downloading && `Downloading ${baseName(downloading)}`
  if (!progress && uploads.failures.length === 0) return null
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
      {progress && (
        <p role="status" className="flex min-w-0 items-center gap-2 text-ui-sm text-muted">
          <Spinner />
          <span className="min-w-0 truncate">{progress}</span>
        </p>
      )}
    </div>
  )
}

export function TreeDirectory({
  source,
  path,
  depth,
  rootOpen = true,
  client,
  onSelect,
  selected,
  touched = noPaths,
  icon,
  action,
}: {
  source: FileSource
  path: string
  depth: number
  rootOpen?: boolean
  client: Api
  onSelect: SelectFile
  selected: Selection | null
  touched?: Set<string>
  icon?: ReactNode
  action?: ReactNode
}) {
  const [expanded, toggle] = useExpanded(sourceKey(source, path), path === '' && rootOpen)
  const listing = useListing(source, path, expanded, client)
  const label = path === '' ? source.label : path.split('/').at(-1) ?? path
  return (
    <div>
      <ListRow
        aria-expanded={expanded}
        style={indent(depth)}
        onClick={toggle}
        leading={
          <>
            <ChevronRight className={cn(chevronTurn, expanded && 'rotate-90')} />
            {icon ?? <Folder />}
          </>
        }
        title={label}
        hoverAction={action}
      >
        {label}
      </ListRow>
      {expanded && (
        <div>
          <ListingStatus listing={listing} depth={depth} />
          {listing?.entries.map((entry) => {
            const childPath = path ? `${path}/${entry.name}` : entry.name
            if (entry.kind === 'dir') {
              return <TreeDirectory key={childPath} source={source} path={childPath} depth={depth + 1} client={client} onSelect={onSelect} selected={selected} touched={touched} />
            }
            const childKey = sourceKey(source, childPath)
            const isSelected = selected ? sourceKey(selected, selected.path) === childKey : false
            return (
              <ListRow
                key={childPath}
                ref={isSelected ? scrollToRow : undefined}
                selected={isSelected}
                style={indent(depth + 1)}
                onClick={() => onSelect(source, childPath)}
                leading={<><span className="w-3.5 shrink-0" /><File /></>}
                trailing={touched.has(childPath) ? <span role="img" aria-label="Changed in this session" title="Changed in this session" className="inline-block size-1.5 rounded-full bg-accent" /> : undefined}
              >
                {entry.name}
              </ListRow>
            )
          })}
        </div>
      )}
    </div>
  )
}

export function RunCheckoutTree({ source, client, ...props }: Omit<ComponentProps<typeof TreeDirectory>, 'source' | 'path' | 'action'> & { source: WorkspaceSource }) {
  const run = useStore((s) => s.runs[source.runID])
  const self = useSelf()
  const steerOthers = useStore((s) => s.workspaces[source.workspaceID]?.steer_others)
  const writable = useCapability().hasMethod('files.upload') && run !== undefined && isLiveRun(run) &&
    allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const uploader = useUpload(
    (path, content) =>
      client
        .filesUpload({ workspace_id: source.workspaceID, run_id: source.runID, ...(source.repo ? { repo: source.repo } : {}), path, content_base64: content })
        .catch((err: unknown) => {
          throw err instanceof ApiError && err.status === 409 ? new Error(`${path} already exists in this session; open it to edit`) : err
        }),
    () => useStore.getState().invalidateRun(source.runID),
  )
  const picker = useRef<HTMLInputElement>(null)
  const [over, setOver] = useState(false)
  useRefuseStrayFileDrops()
  const accepts = (event: DragEvent) => writable && !uploader.busy && event.dataTransfer.types.includes('Files')
  return (
    <div
      className={cn('rounded-control', over && dropTargetClass)}
      onDragOver={(event) => {
        if (!accepts(event)) return
        event.preventDefault()
        event.dataTransfer.dropEffect = 'copy'
        setOver(true)
      }}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOver(false)
      }}
      onDrop={(event) => {
        setOver(false)
        if (!accepts(event)) return
        event.preventDefault()
        uploader.upload('', droppedUploads(event.dataTransfer))
      }}
    >
      <TreeDirectory
        {...props}
        source={source}
        path=""
        client={client}
        action={writable && (
          <Button variant="ghost" size="icon-sm" label="Upload files…" disabled={uploader.busy} onClick={() => picker.current?.click()}>
            <Upload />
          </Button>
        )}
      />
      {writable && (
        <input
          ref={picker}
          type="file"
          multiple
          hidden
          aria-label={`Files to upload to ${source.label}`}
          onChange={(event) => {
            uploader.upload('', [...(event.target.files ?? [])].map((file) => ({ file, path: file.name })))
            event.target.value = ''
          }}
        />
      )}
      <UploadStatus uploads={uploader.state} dismissFailures={uploader.dismissFailures} />
    </div>
  )
}

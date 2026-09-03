import { ChevronDown, ChevronRight, File, Folder, FolderTree } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { message } from '@/components/palette/palette'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import { terminalFontFamily } from '@/lib/term-font'
import type { Run, Workspace } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { FilePatch } from '@/routes/diff/patch-view'
import { parsePatch } from '@/routes/diff/parse'
import { filesKey } from '@/store/files'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

interface FileSource {
  workspaceID: string
  runID: string
  label: string
}

interface Selection extends FileSource {
  path: string
}

type ViewerMode = 'file' | 'diff'

export function FilesRoute({ client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore((s) => s.workspaces)
  const runs = useStore((s) => s.runs)
  const diffs = useStore((s) => s.diffs)
  const invalidateRun = useStore((s) => s.invalidateRun)
  const filesEpoch = useStore((s) => s.filesEpoch)
  const capabilities = useCapability()
  const [selection, setSelection] = useState<Selection | null>(null)
  const [mode, setMode] = useState<ViewerMode>('file')
  const seenRevisions = useRef<Record<string, number>>({})

  useEffect(() => {
    for (const [runID, diff] of Object.entries(diffs)) {
      const previous = seenRevisions.current[runID]
      if (previous !== undefined && previous !== diff.revision) invalidateRun(runID)
      seenRevisions.current[runID] = diff.revision
    }
  }, [diffs, invalidateRun])

  const select = (source: FileSource, path: string) => {
    setSelection({ ...source, path })
    setMode('file')
  }

  if (!capabilities.hasMethod('files.tree')) return null
  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="Files" subtitle="Read-only repository browser" />
      <div className="flex min-h-0 flex-1">
        <aside className="w-72 shrink-0 overflow-y-auto border-r p-2" aria-label="Files">
          {Object.values(workspaces).map((workspace) => (
            <WorkspaceTree
              key={workspace.id}
              workspace={workspace}
              runs={Object.values(runs).filter(
                (run) => run.workspace_id === workspace.id && isLiveRun(run),
              )}
              client={client}
              onSelect={select}
            />
          ))}
          {Object.keys(workspaces).length === 0 && (
            <p className="p-2 text-sm text-muted-foreground">No workspaces available.</p>
          )}
        </aside>
        <FileViewer
          selection={selection}
          mode={mode}
          onMode={setMode}
          client={client}
          epoch={filesEpoch}
        />
      </div>
    </div>
  )
}

function isLiveRun(run: Run): boolean {
  return !['merged', 'abandoned', 'failed', 'interrupted'].includes(run.status)
}

function WorkspaceTree({
  workspace,
  runs,
  client,
  onSelect,
}: {
  workspace: Workspace
  runs: Run[]
  client: Api
  onSelect: (source: FileSource, path: string) => void
}) {
  const [expanded, setExpanded] = useState(true)
  return (
    <section>
      <button
        type="button"
        className="flex w-full items-center gap-1 rounded px-1 py-1 text-left text-sm font-medium hover:bg-accent"
        onClick={() => setExpanded((open) => !open)}
        aria-expanded={expanded}
      >
        {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        <FolderTree className="size-3.5 text-muted-foreground" />
        <span className="truncate">{workspace.name}</span>
      </button>
      {expanded && (
        <div className="ml-2 border-l pl-2">
          <TreeDirectory
            source={{ workspaceID: workspace.id, runID: '', label: `base: ${workspace.base_branch}` }}
            path=""
            client={client}
            onSelect={onSelect}
          />
          {runs.map((run) => (
            <TreeDirectory
              key={run.id}
              source={{ workspaceID: workspace.id, runID: run.id, label: runLabel(run) }}
              path=""
              client={client}
              onSelect={onSelect}
              touched={touchedPaths(run.id)}
            />
          ))}
        </div>
      )}
    </section>
  )
}

function touchedPaths(runID: string): Set<string> {
  const snapshots = useStore.getState().diffs[runID]?.snapshots
  return new Set(snapshots?.[0]?.files.map((file) => file.path) ?? [])
}

function TreeDirectory({
  source,
  path,
  client,
  onSelect,
  touched = new Set<string>(),
}: {
  source: FileSource
  path: string
  client: Api
  onSelect: (source: FileSource, path: string) => void
  touched?: Set<string>
}) {
  const key = filesKey(source.workspaceID, source.runID, path)
  const cached = useStore((s) => s.trees[key])
  const setTree = useStore((s) => s.setTree)
  const [expanded, setExpanded] = useState(path === '')
  useEffect(() => {
    if (!expanded || cached?.loading || cached?.entries || cached?.error) return
    setTree(key, { entries: [], loading: true, error: undefined })
    const params: { workspace_id: string; run_id?: string; path: string } = {
      workspace_id: source.workspaceID,
      path,
    }
    if (source.runID) params.run_id = source.runID
    void client
      .filesTree(params)
      .then((result) => setTree(key, { entries: result.entries, loading: false, error: undefined }))
      .catch((err) => setTree(key, { entries: [], loading: false, error: message(err) }))
  }, [cached, client, expanded, key, path, setTree, source.runID, source.workspaceID])

  const label = path === '' ? source.label : path.split('/').at(-1) ?? path
  const notice = cached?.error
    ? source.runID
      ? "This run's checkout was removed; pull the branch to see its files"
      : 'Link a repository to browse files'
    : null
  return (
    <div>
      <button
        type="button"
        className="flex w-full items-center gap-1 rounded px-1 py-1 text-left text-sm hover:bg-accent"
        onClick={() => setExpanded((open) => !open)}
        aria-expanded={expanded}
      >
        {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        <Folder className="size-3.5 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate">{label}</span>
      </button>
      {expanded && (
        <div className="ml-4 border-l pl-2">
          {cached?.loading && <p className="px-1 py-1 text-xs text-muted-foreground">Loading...</p>}
          {notice && <p className="px-1 py-1 text-xs text-muted-foreground">{notice}</p>}
          {cached?.entries.map((entry) => {
            const childPath = path ? `${path}/${entry.name}` : entry.name
            if (entry.kind === 'dir') {
              return (
                <TreeDirectory
                  key={childPath}
                  source={source}
                  path={childPath}
                  client={client}
                  onSelect={onSelect}
                  touched={touched}
                />
              )
            }
            const marked = touched.has(childPath)
            return (
              <button
                key={childPath}
                type="button"
                className="flex w-full items-center gap-1 rounded px-1 py-1 text-left text-sm hover:bg-accent"
                onClick={() => onSelect(source, childPath)}
              >
                <File className="size-3.5 text-muted-foreground" />
                <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                {marked && <span className="size-1.5 shrink-0 rounded-full bg-primary" title="Changed in this run" />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function FileViewer({
  selection,
  mode,
  onMode,
  client,
  epoch,
}: {
  selection: Selection | null
  mode: ViewerMode
  onMode: (mode: ViewerMode) => void
  client: Api
  epoch: number
}) {
  const [error, setError] = useState<string | null>(null)
  const key = selection ? filesKey(selection.workspaceID, selection.runID, selection.path) : ''
  const document = useStore((s) => (key ? s.documents[key] : undefined))
  const fileDiff = useStore((s) => (key ? s.fileDiffs[key] : undefined))
  const setDocument = useStore((s) => s.setDocument)
  const setFileDiff = useStore((s) => s.setFileDiff)

  useEffect(() => {
    if (!selection || mode !== 'file' || document) return
    setError(null)
    setDocument(key, {
      content: '',
      truncated: false,
      binary: false,
      size: 0,
      loading: true,
      error: undefined,
    })
    const params: { workspace_id: string; run_id?: string; path: string } = {
      workspace_id: selection.workspaceID,
      path: selection.path,
    }
    if (selection.runID) params.run_id = selection.runID
    void client
      .filesRead(params)
      .then((result) => setDocument(key, { ...result, loading: false, error: undefined }))
      .catch((err) => {
        setError(message(err))
        setDocument(key, { content: '', truncated: false, binary: false, size: 0, loading: false, error: message(err) })
      })
  }, [client, document, epoch, key, mode, selection, setDocument])

  useEffect(() => {
    if (!selection || mode !== 'diff' || !selection.runID || fileDiff) return
    setError(null)
    setFileDiff(key, { patch: '', truncated: false, loading: true, error: undefined })
    void client
      .filesDiff(selection.runID, selection.path)
      .then((result) => setFileDiff(key, { ...result, loading: false, error: undefined }))
      .catch((err) => {
        setError(message(err))
        setFileDiff(key, { patch: '', truncated: false, loading: false, error: message(err) })
      })
  }, [client, epoch, fileDiff, key, mode, selection, setFileDiff])

  if (!selection) {
    return <div className="flex min-w-0 flex-1 items-center justify-center p-4 text-sm text-muted-foreground">Select a file to read.</div>
  }
  return (
    <article className="min-w-0 flex-1 overflow-y-auto">
      <header className="flex items-center gap-1 border-b px-3 py-2 text-sm">
        <span className="min-w-0 flex-1 truncate" title={selection.path}>{selection.path}</span>
        {selection.runID && (
          <>
            <button type="button" className={mode === 'file' ? 'rounded bg-accent px-2 py-1' : 'rounded px-2 py-1 text-muted-foreground hover:bg-accent'} onClick={() => onMode('file')}>File</button>
            <button type="button" className={mode === 'diff' ? 'rounded bg-accent px-2 py-1' : 'rounded px-2 py-1 text-muted-foreground hover:bg-accent'} onClick={() => onMode('diff')}>Diff vs base</button>
          </>
        )}
      </header>
      {error && <p className="p-3 text-sm text-destructive">{error}</p>}
      {mode === 'diff' && selection.runID ? <DiffDocument state={fileDiff} /> : <ReadDocument state={document} />}
    </article>
  )
}

function ReadDocument({ state }: { state?: { content: string; truncated: boolean; binary: boolean; loading?: boolean } }) {
  if (!state || state.loading) return <p className="p-3 text-sm text-muted-foreground">Loading...</p>
  if (state.binary) return <p className="p-3 text-sm text-muted-foreground">Binary file</p>
  return (
    <>
      {state.truncated && <p className="border-b px-3 py-2 text-xs text-muted-foreground">Truncated at 512 KiB</p>}
      <NumberedText content={state.content} />
    </>
  )
}

function DiffDocument({ state }: { state?: { patch: string; truncated: boolean; loading?: boolean } }) {
  if (!state || state.loading) return <p className="p-3 text-sm text-muted-foreground">Loading...</p>
  const files = parsePatch(state.patch)
  return (
    <>
      {state.truncated && <p className="border-b px-3 py-2 text-xs text-muted-foreground">Truncated at 512 KiB</p>}
      {files.length === 0 ? <p className="p-3 text-sm text-muted-foreground">No changes.</p> : <div className="space-y-3 p-3">{files.map((file) => <FilePatch key={file.path} file={file} />)}</div>}
    </>
  )
}

function NumberedText({ content }: { content: string }) {
  const lines = content.split('\n')
  return (
    <pre className="overflow-x-auto p-3 text-xs leading-5" style={{ fontFamily: terminalFontFamily }}>
      {lines.map((line, index) => (
        <span key={index} className="flex min-w-max">
          <span className="mr-4 inline-block w-10 select-none text-right text-muted-foreground">{index + 1}</span>
          <span>{line || ' '}</span>
        </span>
      ))}
    </pre>
  )
}

registerRoute('files', FilesRoute)

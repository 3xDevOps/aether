import { ArrowLeft, ChevronDown, ChevronRight, File, Folder, FolderTree } from 'lucide-react'
import { useEffect, useState } from 'react'
import { message } from '@/lib/format'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import { terminalFontFamily } from '@/lib/term-font'
import type { Run, Workspace } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
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
  const filesEpoch = useStore((s) => s.filesEpoch)
  const capabilities = useCapability()
  const [selection, setSelection] = useState<Selection | null>(null)
  const [mode, setMode] = useState<ViewerMode>('file')
  const [mobileView, setMobileView] = useState<'tree' | 'viewer'>('tree')

  const select = (source: FileSource, path: string) => {
    setSelection({ ...source, path })
    setMode('file')
    setMobileView('viewer')
  }

  if (!capabilities.hasMethod('files.tree')) return null
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title="Files" subtitle="Read-only repository browser" />
      <div className="flex min-h-0 min-w-0 flex-1 flex-col md:flex-row">
        <aside
          className={cn(
            'min-h-0 min-w-0 flex-1 overflow-y-auto border-b bg-sidebar md:w-[280px] md:flex-none md:border-b-0 md:border-r',
            mobileView === 'viewer' && 'hidden md:block',
          )}
          aria-label="Files"
        >
          <div className="flex min-h-[35px] items-center justify-between gap-3 border-b px-3">
            <div className="flex min-w-0 items-center gap-2">
              <FolderTree className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <p className="truncate text-[12px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">
                Explorer
              </p>
            </div>
            <span className="shrink-0 text-[11px] text-muted-foreground">
              {Object.keys(workspaces).length} {Object.keys(workspaces).length === 1 ? 'workspace' : 'workspaces'}
            </span>
          </div>
          <div className="space-y-1 p-2">
            {Object.values(workspaces).map((workspace) => (
              <WorkspaceTree
                key={workspace.id}
                workspace={workspace}
                runs={Object.values(runs).filter(
                  (run) => run.workspace_id === workspace.id && isLiveRun(run),
                )}
                client={client}
                onSelect={select}
                selected={selection}
              />
            ))}
          </div>
          {Object.keys(workspaces).length === 0 && (
            <p className="mx-2 border border-dashed px-3 py-3 text-xs text-muted-foreground">
              No workspaces available.
            </p>
          )}
        </aside>
        <div
          className={cn(
            'flex min-h-0 min-w-0 flex-1',
            (!selection || mobileView === 'tree') && 'hidden md:flex',
          )}
        >
          <FileViewer
            selection={selection}
            mode={mode}
            onMode={setMode}
            onBrowse={() => setMobileView('tree')}
            client={client}
            epoch={filesEpoch}
          />
        </div>
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
  selected,
}: {
  workspace: Workspace
  runs: Run[]
  client: Api
  onSelect: (source: FileSource, path: string) => void
  selected: Selection | null
}) {
  const [expanded, setExpanded] = useState(true)
  return (
    <section className="space-y-0.5">
      <button
        type="button"
        className={cn(
          focusRing,
          'flex min-h-7 w-full items-center gap-1.5 px-1.5 text-left text-[13px] font-medium hover:bg-toolbar-hover',
        )}
        onClick={() => setExpanded((open) => !open)}
        aria-expanded={expanded}
      >
        {expanded ? (
          <ChevronDown className="size-3.5 shrink-0" aria-hidden />
        ) : (
          <ChevronRight className="size-3.5 shrink-0" aria-hidden />
        )}
        <FolderTree className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1 truncate">{workspace.name}</span>
      </button>
      {expanded && (
        <div className="ml-2 border-l border-border/70 pl-1.5">
          <TreeDirectory
            source={{ workspaceID: workspace.id, runID: '', label: `base: ${workspace.base_branch}` }}
            path=""
            client={client}
            onSelect={onSelect}
            selected={selected}
          />
          {runs.map((run) => (
            <TreeDirectory
              key={run.id}
              source={{ workspaceID: workspace.id, runID: run.id, label: runLabel(run) }}
              path=""
              client={client}
              onSelect={onSelect}
              selected={selected}
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
  selected,
  touched = new Set<string>(),
}: {
  source: FileSource
  path: string
  client: Api
  onSelect: (source: FileSource, path: string) => void
  selected: Selection | null
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
        className={cn(
          focusRing,
          'flex min-h-7 w-full items-center gap-1.5 px-1.5 text-left text-[12px] hover:bg-toolbar-hover',
        )}
        onClick={() => setExpanded((open) => !open)}
        aria-expanded={expanded}
      >
        {expanded ? (
          <ChevronDown className="size-3 shrink-0" aria-hidden />
        ) : (
          <ChevronRight className="size-3 shrink-0" aria-hidden />
        )}
        <Folder className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1 truncate">{label}</span>
      </button>
      {expanded && (
        <div className="ml-3 border-l border-border/50 pl-1.5">
          {cached?.loading && <p className="px-1.5 py-1 text-[12px] text-muted-foreground">Loading files...</p>}
          {notice && (
            <p className="px-1.5 py-1.5 text-[12px] leading-4 text-state-failed">{notice}</p>
          )}
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
                  selected={selected}
                  touched={touched}
                />
              )
            }
            const marked = touched.has(childPath)
            const isSelected =
              selected?.workspaceID === source.workspaceID &&
              selected.runID === source.runID &&
              selected.path === childPath
            return (
              <button
                key={childPath}
                type="button"
                aria-current={isSelected ? 'page' : undefined}
                className={cn(
                  focusRing,
                  'flex min-h-7 w-full items-center gap-1.5 px-1.5 text-left text-[12px] hover:bg-toolbar-hover',
                  isSelected && 'bg-selection text-selection-foreground',
                )}
                onClick={() => onSelect(source, childPath)}
              >
                <File className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                {marked && (
                  <span
                    className="size-1.5 shrink-0 rounded-full bg-primary"
                    title="Changed in this run"
                    aria-label="Changed in this run"
                  />
                )}
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
  onBrowse,
  client,
  epoch,
}: {
  selection: Selection | null
  mode: ViewerMode
  onMode: (mode: ViewerMode) => void
  onBrowse: () => void
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
        setDocument(key, {
          content: '',
          truncated: false,
          binary: false,
          size: 0,
          loading: false,
          error: message(err),
        })
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
    return (
      <div className="flex min-w-0 flex-1 items-center justify-center border-l text-center text-[12px] text-muted-foreground">
        Select a file from the repository tree.
      </div>
    )
  }
  return (
    <article className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-background">
      <header className="flex min-h-[35px] shrink-0 flex-wrap items-center gap-2 border-b bg-sidebar px-2">
        <button
          type="button"
          className={cn(
            focusRing,
            'inline-flex h-[26px] items-center gap-1.5 border border-input bg-background px-2 text-[12px] font-medium md:hidden',
          )}
          onClick={onBrowse}
        >
          <ArrowLeft className="size-3.5" aria-hidden />
          Browse
        </button>
        <div className="min-w-0 flex-[1_1_14rem]">
          <p className="truncate font-mono text-[12px] font-medium" title={selection.path}>
            {selection.path}
          </p>
          <p className="truncate text-[11px] text-muted-foreground" title={selection.label}>
            {selection.label}
          </p>
        </div>
        {selection.runID && (
          <div
            role="tablist"
            aria-label="File view"
            className="flex max-w-full shrink-0 overflow-x-auto border border-input bg-background p-0.5"
          >
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'file'}
              className={cn(
                focusRing,
                'min-h-[22px] shrink-0 px-2 text-[12px] font-medium',
                mode === 'file'
                  ? 'bg-selection text-selection-foreground'
                  : 'text-muted-foreground hover:bg-toolbar-hover',
              )}
              onClick={() => onMode('file')}
            >
              File
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'diff'}
              className={cn(
                focusRing,
                'min-h-[22px] shrink-0 px-2 text-[12px] font-medium',
                mode === 'diff'
                  ? 'bg-selection text-selection-foreground'
                  : 'text-muted-foreground hover:bg-toolbar-hover',
              )}
              onClick={() => onMode('diff')}
            >
              Diff vs base
            </button>
          </div>
        )}
      </header>
      {error && (
        <p role="alert" className="shrink-0 border-b bg-destructive/10 px-3 py-1.5 text-[12px] text-destructive">
          {error}
        </p>
      )}
      {mode === 'diff' && selection.runID ? (
        <DiffDocument state={fileDiff} />
      ) : (
        <ReadDocument state={document} />
      )}
    </article>
  )
}
function ReadDocument({
  state,
}: {
  state?: { content: string; truncated: boolean; binary: boolean; loading?: boolean }
}) {
  if (!state || state.loading) {
    return (
      <p className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-muted-foreground">
        Loading file...
      </p>
    )
  }
  if (state.binary) {
    return (
      <p className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-muted-foreground">
        Binary file
      </p>
    )
  }
  return (
    <div
      tabIndex={0}
      className={cn(
        focusRing,
        'focus-visible:-outline-offset-2 min-h-0 flex-1 overflow-auto overscroll-contain',
      )}
    >
      {state.truncated && (
        <p className="border-b bg-state-waiting/10 px-3 py-1.5 text-[12px] text-muted-foreground">
          Truncated at 512 KiB
        </p>
      )}
      <NumberedText content={state.content} />
    </div>
  )
}

function DiffDocument({
  state,
}: {
  state?: { patch: string; truncated: boolean; loading?: boolean }
}) {
  if (!state || state.loading) {
    return (
      <p className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-muted-foreground">
        Loading diff...
      </p>
    )
  }
  const files = parsePatch(state.patch)
  return (
    <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden overscroll-contain">
      {state.truncated && (
        <p className="border-b bg-state-waiting/10 px-3 py-1.5 text-[12px] text-muted-foreground">
          Truncated at 512 KiB
        </p>
      )}
      {files.length === 0 ? (
        <p className="p-4 text-[12px] text-muted-foreground">No changes.</p>
      ) : (
        <div>
          {files.map((file) => (
            <FilePatch key={file.path} file={file} />
          ))}
        </div>
      )}
    </div>
  )
}

function NumberedText({ content }: { content: string }) {
  const lines = content.split('\n')
  return (
    <pre
      className="min-w-max px-3 py-2 text-[12px] leading-[22px]"
      style={{ fontFamily: terminalFontFamily }}
    >
      {lines.map((line, index) => (
        <span key={index} className="flex min-w-max">
          <span className="mr-3 inline-block w-10 select-none text-right text-muted-foreground">
            {index + 1}
          </span>
          <span>{line || ' '}</span>
        </span>
      ))}
    </pre>
  )
}

registerRoute('files', FilesRoute)

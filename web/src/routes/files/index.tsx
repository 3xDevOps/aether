import { ArrowLeft, ChevronDown, ChevronRight, File, Folder, FolderPlus, FolderTree, Search, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { javascript } from '@codemirror/lang-javascript'
import { json } from '@codemirror/lang-json'
import { markdown } from '@codemirror/lang-markdown'
import { python } from '@codemirror/lang-python'
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from '@codemirror/language'
import { toml } from '@codemirror/legacy-modes/mode/toml'
import { tags } from '@lezer/highlight'
import { openSearchPanel, searchKeymap } from '@codemirror/search'
import { EditorState } from '@codemirror/state'
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view'
import { message } from '@/lib/format'
import { ApiError, api, type Api } from '@/lib/api'
import { ViewHeader } from '@/components/view-header'
import { runLabel } from '@/lib/status'
import type { ConfigRoot, Run, Workspace } from '@/lib/types'
import { terminalFontFamily } from '@/lib/term-font'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { cn, focusRing } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { FilePatch } from '@/routes/diff/patch-view'
import { parsePatch } from '@/routes/diff/parse'
import { configKey, filesKey, type FileTab } from '@/store/files'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
interface WorkspaceSource {
  kind: 'workspace'
  workspaceID: string
  runID: string
  label: string
  branch?: string
}

interface ConfigSource {
  kind: 'config'
  harness: string
  rootPath: string
  label: string
}

type FileSource = WorkspaceSource | ConfigSource

type Selection = FileSource & {
  path: string
}

type ViewerMode = 'file' | 'diff'

function sourceKey(source: FileSource, path: string): string {
  return source.kind === 'config'
    ? configKey(source.harness, path)
    : filesKey(source.workspaceID, source.runID, path)
}

function sourceLabel(source: FileSource): string {
  return source.kind === 'config' ? `${source.label} · ${source.rootPath}` : source.label
}
function fileTabFromSource(source: FileSource, path: string, key: string): FileTab {
  return source.kind === 'config'
    ? { key, kind: 'config', harness: source.harness, rootPath: source.rootPath, path, label: source.label }
    : { key, kind: 'workspace', workspaceID: source.workspaceID, runID: source.runID, path, label: source.label, branch: source.branch }
}

function fileTabToSelection(tab: FileTab): Selection {
  return tab.kind === 'config'
    ? { kind: 'config', harness: tab.harness, rootPath: tab.rootPath, path: tab.path, label: tab.label }
    : { kind: 'workspace', workspaceID: tab.workspaceID, runID: tab.runID, path: tab.path, label: tab.label, branch: tab.branch }
}

export function FilesRoute({ client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore((s) => s.workspaces)
  const runs = useStore((s) => s.runs)
  const filesEpoch = useStore((s) => s.filesEpoch)
  const identityEpoch = useStore((s) => s.identityEpoch)
  const identityKey = useStore((s) => s.identityKey)
  const fileTabs = useStore((s) => s.fileTabs)
  const activeFileKey = useStore((s) => s.activeFileKey)
  const openFileTab = useStore((s) => s.openFileTab)
  const capabilities = useCapability()
  const closeFileTab = useStore((s) => s.closeFileTab)
  const setActiveFileKey = useStore((s) => s.setActiveFileKey)
  const [mode, setMode] = useState<ViewerMode>('file')
  const [mobileView, setMobileView] = useState<'tree' | 'viewer'>('tree')
  const [configRoots, setConfigRoots] = useState<ConfigRoot[]>([])
  const [configError, setConfigError] = useState<string | null>(null)
  const [newFile, setNewFile] = useState<{ source: ConfigSource; path: string; error?: string } | null>(null)
  const newFileRef = useRef(newFile)
  newFileRef.current = newFile

  useEffect(() => {
    if (!capabilities.hasMethod('config.roots')) return
    let active = true
    setConfigRoots([])
    setConfigError(null)
    void client
      .configRoots()
      .then((result) => {
        if (active) {
          setConfigRoots(result.roots)
          setConfigError(null)
        }
      })
      .catch((err) => {
        if (active) setConfigError(message(err))
      })
    return () => {
      active = false
    }
  }, [capabilities, client, identityKey])

  useEffect(() => {
    setNewFile(null)
    setMobileView('tree')
  }, [identityKey])

  const tabs = fileTabs.map(fileTabToSelection)
  const selection = tabs.find((tab) => sourceKey(tab, tab.path) === activeFileKey) ?? null

  useEffect(() => {
    if (activeFileKey && tabs.some((tab) => sourceKey(tab, tab.path) === activeFileKey)) return
    const nextKey = tabs.at(-1) ? sourceKey(tabs.at(-1)!, tabs.at(-1)!.path) : null
    if (nextKey === activeFileKey) return
    setActiveFileKey(nextKey)
  }, [activeFileKey, setActiveFileKey, tabs])

  const select = (source: FileSource, path: string) => {
    const key = sourceKey(source, path)
    openFileTab(fileTabFromSource(source, path, key))
    setMode('file')
    setMobileView('viewer')
  }

  const closeTab = (tab: Selection) => {
    const key = sourceKey(tab, tab.path)
    const draft = useStore.getState().drafts[key]
    const dirty = Boolean(draft && (draft.content !== draft.baseContent || draft.saving))
    if (dirty && !window.confirm(`Discard unsaved changes to ${tab.path}?`)) return
    if (dirty) useStore.getState().clearDraft(key)
    const currentTabs = useStore.getState().fileTabs
    const currentIndex = currentTabs.findIndex((item) => item.key === key)
    const wasActive = useStore.getState().activeFileKey === key
    closeFileTab(key)
    if (wasActive) {
      const remaining = currentTabs.filter((item) => item.key !== key)
      const next = remaining[Math.min(currentIndex, remaining.length - 1)]
      setActiveFileKey(next?.key ?? null)
      setMode('file')
      if (!next) setMobileView('tree')
    }
  }
  const configSources = configRoots.map((root) => ({
    kind: 'config' as const,
    harness: root.harness,
    rootPath: root.path,
    label: root.harness,
  }))
  const workspaceCount = Object.keys(workspaces).length
  const canBrowseWorkspaces = capabilities.hasMethod('files.tree')
  const canBrowseConfig = capabilities.hasMethod('config.roots')
  const visibleWorkspaceCount = canBrowseWorkspaces ? workspaceCount : 0
  const visibleSourceCount = visibleWorkspaceCount + (canBrowseConfig ? configSources.length : 0)
  if (!canBrowseWorkspaces && !canBrowseConfig) return null
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title="Files" subtitle="Edit workspace files and your agent configuration" />
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
              <p className="truncate text-[12px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">Explorer</p>
            </div>
            <span className="shrink-0 text-[11px] text-muted-foreground">
              {visibleSourceCount} {visibleSourceCount === 1 ? 'source' : 'sources'}
            </span>
          </div>
          <div className="space-y-1 p-2">
            {canBrowseWorkspaces && Object.values(workspaces).map((workspace) => (
              <WorkspaceTree
                key={workspace.id}
                workspace={workspace}
                runs={Object.values(runs).filter((run) => run.workspace_id === workspace.id && isLiveRun(run))}
                client={client}
                onSelect={select}
                selected={selection}
              />
            ))}
            {canBrowseConfig && configSources.map((source) => (
              <TreeDirectory
                key={source.harness}
                source={source}
                path=""
                client={client}
                onSelect={select}
                selected={selection}
                onNewFile={(root) => setNewFile({ source: root, path: '' })}
              />
            ))}
          </div>
          {newFile && (
            <form
              className="mx-2 mb-2 space-y-2 border border-primary/40 bg-background p-2"
              onSubmit={(event) => {
                event.preventDefault()
                const pending = newFile
                const requestEpoch = identityEpoch
                void createConfigFile(pending, client, requestEpoch, (created) => {
                  const current = newFileRef.current
                  if (current !== pending) return
                  setNewFile(null)
                  if (useStore.getState().identityEpoch !== requestEpoch) return
                  select(pending.source, created.path)
                }).catch((err) => {
                  if (useStore.getState().identityEpoch !== requestEpoch) return
                  const current = newFileRef.current
                  if (current === pending) setNewFile({ ...current, error: message(err) })
                })
              }}
            >
              <label className="block text-[11px] font-medium" htmlFor="new-config-file">New config file</label>
              <input
                id="new-config-file"
                autoFocus
                value={newFile.path}
                onChange={(event) => setNewFile({ ...newFile, path: event.target.value, error: undefined })}
                placeholder="settings.json or skills/my-skill.md"
                className="h-8 coarse:h-11 coarse:min-h-11 w-full border border-input bg-background px-2 font-mono text-[12px] outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
              <p className="text-[11px] text-muted-foreground">Relative paths only. Existing files are never overwritten.</p>
              {newFile.error && <p role="alert" className="text-[11px] text-destructive">{newFile.error}</p>}
              <div className="flex justify-end gap-2">
                <button type="button" className={cn(focusRing, 'min-h-7 px-2 py-1 text-[11px] coarse:min-h-11')} onClick={() => setNewFile(null)}>Cancel</button>
                <button type="submit" className={cn(focusRing, 'min-h-7 border border-input bg-primary px-2 py-1 text-[11px] text-primary-foreground coarse:min-h-11')}>Create</button>
              </div>
            </form>
          )}
          {configError && <p role="alert" className="mx-2 border border-dashed px-3 py-2 text-[12px] text-destructive">{configError}</p>}
          {visibleSourceCount === 0 && (
            <p className="mx-2 border border-dashed px-3 py-3 text-xs text-muted-foreground">No workspaces or configuration roots available.</p>
          )}
        </aside>
        <div className={cn('flex min-h-0 min-w-0 flex-1', (!selection || mobileView === 'tree') && 'hidden md:flex')}>
          <FileEditor
            selection={selection}
            tabs={tabs}
            mode={mode}
            onMode={setMode}
            onSelect={select}
            onClose={closeTab}
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
          'flex min-h-7 coarse:min-h-11 w-full items-center gap-1.5 px-1.5 text-left text-[13px] font-medium hover:bg-toolbar-hover',
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
          <TreeDirectory source={{ kind: 'workspace', workspaceID: workspace.id, runID: '', label: `base: ${workspace.base_branch}`, branch: workspace.base_branch }} path="" client={client} onSelect={onSelect} selected={selected} />
          {runs.map((run) => <TreeDirectory key={run.id} source={{ kind: 'workspace', workspaceID: workspace.id, runID: run.id, label: runLabel(run) }} path="" client={client} onSelect={onSelect} selected={selected} touched={touchedPaths(run.id)} />)}
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
  onNewFile,
}: {
  source: FileSource
  path: string
  client: Api
  onSelect: (source: FileSource, path: string) => void
  selected: Selection | null
  touched?: Set<string>
  onNewFile?: (source: ConfigSource) => void
}) {
  const key = sourceKey(source, path)
  const cached = useStore((s) => s.trees[key])
  const filesEpoch = useStore((s) => s.filesEpoch)
  const identityEpoch = useStore((s) => s.identityEpoch)
  const setTree = useStore((s) => s.setTree)
  const sourceKind = source.kind
  const harness = source.kind === 'config' ? source.harness : undefined
  const workspaceID = source.kind === 'workspace' ? source.workspaceID : undefined
  const runID = source.kind === 'workspace' ? source.runID : undefined
  const [expanded, setExpanded] = useState(path === '')
  const requestID = useRef(0)
  useEffect(() => {
    if (!expanded || cached?.loading || cached?.entries || cached?.error) return
    setTree(key, { entries: [], loading: true, error: undefined })
    const pending = useStore.getState().trees[key]
    const requestToken = ++requestID.current
    const requestIdentityEpoch = identityEpoch
    const request = source.kind === 'config'
      ? client.configTree({ harness: source.harness, path })
      : client.filesTree({ workspace_id: source.workspaceID, ...(source.runID ? { run_id: source.runID } : {}), path })
    void request
      .then((result) => {
        if (requestID.current !== requestToken || useStore.getState().identityEpoch !== requestIdentityEpoch || useStore.getState().trees[key] !== pending) return
        setTree(key, { entries: result.entries, loading: false, error: undefined })
      })
      .catch((err) => {
        if (requestID.current !== requestToken || useStore.getState().identityEpoch !== requestIdentityEpoch || useStore.getState().trees[key] !== pending) return
        setTree(key, { entries: [], loading: false, error: message(err) })
      })
  }, [cached, client, expanded, filesEpoch, harness, identityEpoch, key, path, runID, setTree, sourceKind, workspaceID])

  const label = path === '' ? sourceLabel(source) : path.split('/').at(-1) ?? path
  return (
    <div>
      <div className="flex min-h-7 coarse:min-h-11 w-full items-center gap-1.5 px-1.5 text-left text-[12px] hover:bg-toolbar-hover">
        <button type="button" className={cn(focusRing, 'flex min-w-0 flex-1 items-center gap-1.5 text-left')} onClick={() => setExpanded((open) => !open)} aria-expanded={expanded}>
          {expanded ? <ChevronDown className="size-3 shrink-0" aria-hidden /> : <ChevronRight className="size-3 shrink-0" aria-hidden />}
          <Folder className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 flex-1 truncate">{label}</span>
        </button>
        {source.kind === 'config' && path === '' && onNewFile && (
          <button type="button" aria-label={`New file in ${source.label}`} title="New file" className={cn(focusRing, 'inline-flex min-h-7 min-w-7 items-center justify-center p-1 text-muted-foreground hover:text-foreground coarse:min-h-11 coarse:min-w-11')} onClick={() => onNewFile(source)}>
            <FolderPlus className="size-3.5" aria-hidden />
          </button>
        )}
      </div>
      {expanded && (
        <div className="ml-3 border-l border-border/50 pl-1.5">
          {cached?.loading && <p className="px-1.5 py-1 text-[12px] text-muted-foreground">Loading files...</p>}
          {cached?.error && <p role="alert" className="px-1.5 py-1.5 text-[12px] leading-4 text-destructive">{cached.error}</p>}
          {cached?.entries.map((entry) => {
            const childPath = path ? `${path}/${entry.name}` : entry.name
            if (entry.kind === 'dir') {
              return <TreeDirectory key={childPath} source={source} path={childPath} client={client} onSelect={onSelect} selected={selected} touched={touched} />
            }
            const isSelected = selected ? sourceKey(selected, selected.path) === sourceKey(source, childPath) : false
            const marked = touched.has(childPath)
            return (
              <button
                key={childPath}
                type="button"
                aria-current={isSelected ? 'page' : undefined}
                className={cn(
                  focusRing,
                  'flex min-h-7 coarse:min-h-11 w-full items-center gap-1.5 px-1.5 text-left text-[12px] hover:bg-toolbar-hover',
                  isSelected && 'bg-selection text-selection-foreground',
                )}
                onClick={() => onSelect(source, childPath)}
              >
                <File className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                {marked && <span className="size-1.5 shrink-0 rounded-full bg-primary" title="Changed in this run" aria-label="Changed in this run" />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function FileEditor({
  selection,
  tabs,
  mode,
  onMode,
  onSelect,
  onClose,
  onBrowse,
  client,
  epoch,
}: {
  selection: Selection | null
  tabs: Selection[]
  mode: ViewerMode
  onMode: (mode: ViewerMode) => void
  onSelect: (source: FileSource, path: string) => void
  onClose: (selection: Selection) => void
  onBrowse: () => void
  client: Api
  epoch: number
}) {
  const key = selection ? sourceKey(selection, selection.path) : ''
  const identityEpoch = useStore((s) => s.identityEpoch)
  const document = useStore((s) => (key ? s.documents[key] : undefined))
  const draft = useStore((s) => (key ? s.drafts[key] : undefined))
  const setDocument = useStore((s) => s.setDocument)
  const setDraft = useStore((s) => s.setDraft)
  const markDraftSaved = useStore((s) => s.markDraftSaved)
  const markDraftError = useStore((s) => s.markDraftError)
  const clearDraft = useStore((s) => s.clearDraft)
  const updateDraft = useStore((s) => s.updateDraft)
  const handleDraftChange = useCallback((content: string) => updateDraft(key, content), [key, updateDraft])
  const [reload, setReload] = useState(0)
  const loaded = useRef<Record<string, number>>({})
  const request = useRef<Record<string, number>>({})
  const reloadDiscard = useRef<Record<string, number>>({})
  const documentPresent = Boolean(document)
  const documentLoading = document?.loading ?? false
  const selectedHarness = selection?.kind === 'config' ? selection.harness : undefined
  const selectedWorkspaceID = selection?.kind === 'workspace' ? selection.workspaceID : undefined
  const selectedRunID = selection?.kind === 'workspace' ? selection.runID : undefined
  useEffect(() => {
    if (!selection || mode !== 'file' || documentLoading) return
    if (documentPresent && loaded.current[key] === reload) return
    if (documentPresent && reloadDiscard.current[key] !== reload) return
    loaded.current[key] = reload
    const requestID = (request.current[key] ?? 0) + 1
    request.current[key] = requestID
    const current = selection
    const requestEpoch = identityEpoch
    setDocument(key, { content: '', truncated: false, binary: false, size: 0, revision: '', writable: false, loading: true, error: undefined })
    const pending = useStore.getState().documents[key]
    const promise = current.kind === 'config'
      ? client.configRead({ harness: current.harness, path: current.path })
      : client.filesRead({ workspace_id: current.workspaceID, ...(current.runID ? { run_id: current.runID } : {}), path: current.path })
    void promise
      .then((result) => {
        if (request.current[key] !== requestID || useStore.getState().identityEpoch !== requestEpoch || useStore.getState().documents[key] !== pending) return
        setDocument(key, { ...result, loading: false, error: undefined })
        const discard = reloadDiscard.current[key] === reload
        if (discard) {
          delete reloadDiscard.current[key]
          clearDraft(key)
        }
      })
      .catch((err) => {
        if (request.current[key] !== requestID || useStore.getState().identityEpoch !== requestEpoch || useStore.getState().documents[key] !== pending) return
        const detail = message(err)
        setDocument(key, { content: '', truncated: false, binary: false, size: 0, revision: '', writable: false, loading: false, error: detail })
      })
  }, [clearDraft, client, documentLoading, documentPresent, epoch, identityEpoch, key, mode, reload, selectedHarness, selectedRunID, selectedWorkspaceID, selection?.kind, selection?.path, setDocument])

  const save = async () => {
    if (!selection) return
    const currentState = useStore.getState()
    const currentDocument = currentState.documents[key]
    const currentDraft = currentState.drafts[key]
    if (!currentDocument || currentDocument.loading || currentDocument.binary || currentDocument.truncated || !currentDocument.writable || !currentDraft || currentDraft.saving || currentDraft.content === currentDraft.baseContent) return
    const captured = currentDraft.content
    const revision = currentDraft.baseRevision
    const epochAtStart = currentState.identityEpoch
    setDraft(key, { ...currentDraft, content: captured, saving: true, error: undefined, conflict: false })
    try {
      const result = selection.kind === 'config'
        ? await client.configWrite({ harness: selection.harness, path: selection.path, content: captured, revision })
        : await client.filesWrite({ workspace_id: selection.workspaceID, ...(selection.runID ? { run_id: selection.runID } : {}), path: selection.path, content: captured, revision })
      if (useStore.getState().identityEpoch !== epochAtStart) return
      markDraftSaved(key, captured, result)
    } catch (err) {
      if (useStore.getState().identityEpoch !== epochAtStart) return
      const detail = message(err)
      const conflict = err instanceof ApiError && err.status === 409 || /conflict|stale|revision/i.test(detail)
      markDraftError(key, detail, conflict)
    }
  }

  const reloadFromServer = () => {
    if (!selection) return
    setReload((value) => {
      const next = value + 1
      reloadDiscard.current[key] = next
      return next
    })
  }

  if (!selection) return <div className="flex min-w-0 flex-1 items-center justify-center border-l text-center text-[12px] text-muted-foreground">Select a file from the explorer.</div>
  const error = draft?.error ?? document?.error
  const canEdit = Boolean(document && !document.loading && document.writable && !document.binary && !document.truncated)
  return (
    <article className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-background">
      <header className="shrink-0 border-b bg-sidebar">
        <div className="flex min-w-0 flex-wrap items-center gap-2 px-2 py-1">
          <button
            type="button"
            className={cn(
              focusRing,
              'inline-flex h-[26px] coarse:h-11 coarse:min-h-11 items-center gap-1.5 border border-input bg-background px-2 text-[12px] font-medium md:hidden',
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
            <p className="truncate text-[11px] text-muted-foreground" title={sourceLabel(selection)}>
              {sourceLabel(selection)}
            </p>
          </div>
          <button
            type="button"
            aria-label="Find and replace"
            title="Find and replace (Ctrl/Cmd-F)"
            className={cn(
              focusRing,
              'inline-flex h-[26px] coarse:h-11 coarse:min-h-11 items-center gap-1 border border-input bg-background px-2 text-[11px]',
            )}
            onClick={() => editorCommands.openSearch()}
          >
            <Search className="size-3.5" aria-hidden />
            Find
          </button>
          <button
            type="button"
            disabled={!canEdit || !draft || draft.content === draft.baseContent || draft.saving}
            className={cn(
              focusRing,
              'h-[26px] coarse:h-11 coarse:min-h-11 border border-input bg-primary px-2 text-[11px] text-primary-foreground disabled:cursor-not-allowed disabled:opacity-50',
            )}
            onClick={() => void save()}
          >
            {draft?.saving ? 'Saving…' : selection.kind === 'workspace' && !selection.runID ? `Commit to ${selection.branch ?? 'branch'}` : 'Save'}
          </button>
        </div>
        {selection.kind === 'workspace' && !selection.runID && (
          <p className="border-t px-2 py-1 text-[11px] text-muted-foreground">
            Commit creates a workspace commit on {selection.branch ?? 'the base branch'}; it does not push upstream.
          </p>
        )}
        <div role="tablist" aria-label="Open files" className="flex min-w-0 gap-0.5 overflow-x-auto border-t px-2 pt-1">
          {tabs.map((tab) => {
            const tabKey = sourceKey(tab, tab.path)
            const tabDraft = useStore.getState().drafts[tabKey]
            const active = tabKey === key
            return (
              <button
                key={tabKey}
                type="button"
                role="tab"
                aria-selected={active}
                className={cn(
                  focusRing,
                  'group flex min-h-7 max-w-[220px] shrink-0 items-center gap-1 border border-b-0 px-2 text-[11px] coarse:min-h-11',
                  active ? 'bg-background' : 'text-muted-foreground hover:bg-toolbar-hover',
                )}
                onClick={() => onSelect(tab, tab.path)}
              >
                <span className="truncate">{tab.path.split('/').at(-1) ?? tab.path}</span>
                {tabDraft && (tabDraft.content !== tabDraft.baseContent || tabDraft.saving) && <span aria-label="Unsaved changes" title="Unsaved changes">•</span>}
                <span
                  role="button"
                  tabIndex={0}
                  aria-label={`Close ${tab.path}`}
                  className="ml-1 inline-flex min-h-7 min-w-7 items-center justify-center rounded-sm p-0.5 opacity-60 hover:bg-toolbar-hover hover:opacity-100 coarse:min-h-11 coarse:min-w-11"
                  onClick={(event) => { event.stopPropagation(); onClose(tab) }}
                  onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); event.stopPropagation(); onClose(tab) } }}
                >
                  <X className="size-3" aria-hidden />
                </span>
              </button>
            )
          })}
        </div>
        {selection.kind === 'workspace' && selection.runID && (
          <div role="tablist" aria-label="File view" className="flex gap-1 border-t px-2 py-1">
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'file'}
              className={cn(
                focusRing,
                'min-h-[22px] coarse:min-h-11 shrink-0 px-2 text-[11px]',
                mode === 'file' ? 'bg-selection text-selection-foreground' : 'text-muted-foreground hover:bg-toolbar-hover',
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
                'min-h-[22px] coarse:min-h-11 shrink-0 px-2 text-[11px]',
                mode === 'diff' ? 'bg-selection text-selection-foreground' : 'text-muted-foreground hover:bg-toolbar-hover',
              )}
              onClick={() => onMode('diff')}
            >
              Diff vs base
            </button>
          </div>
        )}
      </header>
      {error && <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b bg-destructive/10 px-3 py-2 text-[12px] text-destructive"><span className="min-w-0 flex-1">{error}</span>{draft?.conflict && <><button type="button" className={cn(focusRing, 'border border-input bg-background px-2 py-1 text-foreground')} onClick={reloadFromServer}>Reload from server</button><button type="button" className={cn(focusRing, 'border border-input bg-background px-2 py-1 text-foreground')} onClick={() => clearDraft(key)}>Discard edits</button></>}</div>}
      {mode === 'diff' && selection.kind === 'workspace' && selection.runID ? <DiffDocument client={client} selection={selection} epoch={epoch} /> : document?.error ? <p role="alert" className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-destructive">{document.error}</p> : <EditableDocument path={selection.path} document={document} draft={draft} canEdit={canEdit} onChange={handleDraftChange} onSave={save} />}
    </article>
  )
}

const editorCommands = {
  view: null as EditorView | null,
  openSearch() {
    if (this.view) openSearchPanel(this.view)
  },
}

const editorHighlight = HighlightStyle.define([
  { tag: tags.keyword, color: 'var(--primary)' },
  { tag: tags.string, color: 'var(--state-done)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--state-waiting)' },
  { tag: tags.comment, color: 'var(--muted-foreground)', fontStyle: 'italic' },
  { tag: [tags.typeName, tags.function(tags.variableName)], color: 'var(--state-working)' },
])

function languageFor(path: string) {
  const lower = path.toLowerCase()
  if (lower.endsWith('.json') || lower.endsWith('.jsonc')) return json()
  if (lower.endsWith('.toml')) return StreamLanguage.define(toml)
  if (lower.endsWith('.md') || lower.endsWith('.markdown')) return markdown()
  if (lower.endsWith('.py')) return python()
  if (lower.endsWith('.js') || lower.endsWith('.jsx') || lower.endsWith('.ts') || lower.endsWith('.tsx')) return javascript({ jsx: true, typescript: true })
  return []
}

function EditableDocument({
  path,
  document,
  draft,
  canEdit,
  onChange,
  onSave,
}: {
  path: string
  document?: { content: string; truncated: boolean; binary: boolean; loading?: boolean }
  draft?: { content: string }
  canEdit: boolean
  onChange: (content: string) => void
  onSave: () => void | Promise<void>
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const syncing = useRef(false)
  const saveRef = useRef(onSave)
  saveRef.current = onSave
  const content = draft?.content ?? document?.content ?? ''

  useEffect(() => {
    if (!host.current || !document || document.loading || document.binary || document.truncated) return
    const editor = new EditorView({
      state: EditorState.create({
        doc: content,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightSpecialChars(),
          history(),
          drawSelection(),
          rectangularSelection(),
          syntaxHighlighting(editorHighlight),
          EditorView.contentAttributes.of({ 'aria-label': `Edit ${path}` }),
          closeBrackets(),
          languageFor(path),
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab, { key: 'Mod-s', run: () => { void saveRef.current(); return true } }]),
          highlightActiveLine(),
          EditorView.editable.of(canEdit),
          EditorState.readOnly.of(!canEdit),
          EditorView.theme({
            '&': { height: '100%', fontSize: '12px' },
            '.cm-scroller': { overflow: 'auto', fontFamily: terminalFontFamily },
            '.cm-content': { padding: '10px 0', minHeight: '100%' },
            '.cm-line': { padding: '0 12px', lineHeight: '22px' },
            '.cm-gutters': { backgroundColor: 'transparent', border: 'none', color: 'var(--muted-foreground)' },
            '.cm-activeLineGutter': { backgroundColor: 'transparent' },
            '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--selection) 35%, transparent)' },
          }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged && !syncing.current) onChange(update.state.doc.toString())
          }),
        ],
      }),
      parent: host.current,
    })
    view.current = editor
    editorCommands.view = editor
    return () => {
      if (editorCommands.view === editor) editorCommands.view = null
      editor.destroy()
      view.current = null
    }
  }, [canEdit, document?.binary, document?.loading, document?.truncated, onChange, path])

  useEffect(() => {
    const editor = view.current
    if (!editor || editor.state.doc.toString() === content) return
    syncing.current = true
    editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: content } })
    syncing.current = false
  }, [content])

  if (!document || document.loading) return <p className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-muted-foreground">Loading file…</p>
  if (document.binary) return <p className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-muted-foreground">Binary files cannot be edited.</p>
  if (document.truncated) return <p className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-muted-foreground">This file is larger than the 512 KiB editor limit and cannot be edited.</p>
  return <div ref={host} className="min-h-0 flex-1 overflow-hidden" aria-label="File editor" />
}

function DiffDocument({ selection, client, epoch }: { selection: WorkspaceSource & { path: string }; client: Api; epoch: number }) {
  const key = sourceKey(selection, selection.path)
  const state = useStore((s) => s.fileDiffs[key])
  const setFileDiff = useStore((s) => s.setFileDiff)
  const identityEpoch = useStore((s) => s.identityEpoch)
  const requestID = useRef(0)
  const stored = useStore((s) => s.diffWrap)
  const coarse = useMediaQuery(coarsePointer)
  const wrap = stored ?? coarse
  useEffect(() => {
    if (state || !selection.runID) return
    setFileDiff(key, { patch: '', truncated: false, loading: true, error: undefined })
    const pending = useStore.getState().fileDiffs[key]
    const requestToken = ++requestID.current
    const requestIdentityEpoch = identityEpoch
    void client.filesDiff(selection.runID, selection.path)
      .then((result) => {
        if (requestID.current !== requestToken || useStore.getState().identityEpoch !== requestIdentityEpoch || useStore.getState().fileDiffs[key] !== pending) return
        setFileDiff(key, { ...result, loading: false, error: undefined })
      })
      .catch((err) => {
        if (requestID.current !== requestToken || useStore.getState().identityEpoch !== requestIdentityEpoch || useStore.getState().fileDiffs[key] !== pending) return
        setFileDiff(key, { patch: '', truncated: false, loading: false, error: message(err) })
      })
  }, [client, epoch, identityEpoch, key, selection.path, selection.runID, setFileDiff, state])
  if (!state || state.loading) return <p className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-muted-foreground">Loading diff…</p>
  if (state.error) return <p role="alert" className="flex min-h-0 flex-1 items-center justify-center p-4 text-[12px] text-destructive">{state.error}</p>
  const files = parsePatch(state.patch)
  return <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden overscroll-contain">{state.truncated && <p className="border-b bg-state-waiting/10 px-3 py-1.5 text-[12px] text-muted-foreground">Truncated at 512 KiB</p>}{files.length === 0 ? <p className="p-4 text-[12px] text-muted-foreground">No changes.</p> : <div>{files.map((file) => <FilePatch key={file.path} file={file} wrap={wrap} />)}</div>}</div>
}

async function createConfigFile(
  pending: { source: ConfigSource; path: string },
  client: Api,
  requestEpoch: number,
  onCreated: (result: { path: string }) => void,
): Promise<void> {
  const path = pending.path.trim()
  const pieces = path.split('/')
  if (!path || path.startsWith('/') || path.endsWith('/') || pieces.some((piece) => !piece || piece === '.' || piece === '..')) throw new Error('Use a non-empty relative path without . or .. segments.')
  const result = await client.configWrite({ harness: pending.source.harness, path, content: '', revision: '' })
  const store = useStore.getState()
  if (store.identityEpoch !== requestEpoch) return
  store.setDocument(configKey(pending.source.harness, path), { ...result, loading: false, error: undefined })
  for (let length = pieces.length - 1; length >= 0; length -= 1) {
    store.invalidateTree(configKey(pending.source.harness, pieces.slice(0, length).join('/')))
  }
  onCreated({ path })
}

registerRoute('files', FilesRoute)

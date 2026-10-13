import { useCallback, useEffect, useRef, useState } from 'react'
import { FolderTree, Search, X } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { DiffDocument, EditableDocument, editorCommands } from '@/routes/files/document'
import type { FileTarget } from '@/routes/files/paths'
import { isBaseBranch, type SelectFile, type Selection, sourceKey, tildePath } from '@/routes/files/sources'
import { useStore } from '@/store'
import type { FileDocumentState } from '@/store/files'
import { useHeaderPrimary } from '@/store/hooks'

export type ViewerMode = 'file' | 'diff'

export function FileEditor({
  selection,
  tabs,
  mode,
  onMode,
  onSelect,
  onClose,
  onBrowse,
  reveal,
  shown = true,
  client,
  epoch,
}: {
  selection: Selection
  tabs: Selection[]
  mode: ViewerMode
  onMode: (mode: ViewerMode) => void
  onSelect: SelectFile
  onClose: (selection: Selection) => void
  onBrowse?: () => void
  reveal?: FileTarget | null
  /** False while the editor stays mounted behind another view. */
  shown?: boolean
  client: Api
  epoch: number
}) {
  const key = sourceKey(selection, selection.path)
  const identityEpoch = useStore((s) => s.identityEpoch)
  useHeaderPrimary(shown)
  const stored = useStore((s) => s.documents[key])
  // A file is reread whenever its run's checkout changes, a save included.
  // What was read before stays up meanwhile, so the editor is not rebuilt
  // under the reader.
  const read = useRef<{ key: string; document: FileDocumentState } | null>(null)
  if (stored && !stored.loading && !stored.error) read.current = { key, document: stored }
  const previous = read.current?.key === key ? read.current.document : undefined
  const document = !stored || stored.loading ? previous ?? stored : stored
  const draft = useStore((s) => s.drafts[key])
  const drafts = useStore((s) => s.drafts)
  const setDocument = useStore((s) => s.setDocument)
  const setDraft = useStore((s) => s.setDraft)
  const markDraftSaved = useStore((s) => s.markDraftSaved)
  const markDraftError = useStore((s) => s.markDraftError)
  const clearDraft = useStore((s) => s.clearDraft)
  const updateDraft = useStore((s) => s.updateDraft)
  const handleDraftChange = useCallback((content: string) => updateDraft(key, content), [key, updateDraft])
  const [reload, setReload] = useState(0)
  const [committing, setCommitting] = useState(false)
  const loaded = useRef<Record<string, number>>({})
  const request = useRef<Record<string, number>>({})
  const reloadDiscard = useRef<Record<string, number>>({})
  const documentPresent = Boolean(stored)
  const documentLoading = stored?.loading ?? false
  const selectedWorkspaceID = selection.kind === 'workspace' ? selection.workspaceID : undefined
  const selectedRunID = selection.kind === 'workspace' ? selection.runID : undefined
  useEffect(() => {
    if (mode !== 'file' || documentLoading) return
    if (documentPresent && loaded.current[key] === reload) return
    if (documentPresent && reloadDiscard.current[key] !== reload) return
    loaded.current[key] = reload
    const requestID = (request.current[key] ?? 0) + 1
    request.current[key] = requestID
    const current = selection
    const requestEpoch = identityEpoch
    const blank = { content: '', truncated: false, binary: false, size: 0, revision: '', writable: false }
    // A draft started during a reread is still based on what was read before.
    setDocument(key, { ...(read.current?.key === key ? read.current.document : blank), loading: true, error: undefined })
    const pending = useStore.getState().documents[key]
    const stale = () =>
      request.current[key] !== requestID ||
      useStore.getState().identityEpoch !== requestEpoch ||
      useStore.getState().documents[key] !== pending
    const promise = current.kind === 'home'
      ? client.homeRead(current.path)
      : client.filesRead({ workspace_id: current.workspaceID, ...(current.runID ? { run_id: current.runID } : {}), path: current.path })
    void promise
      .then((result) => {
        if (stale()) return
        setDocument(key, { ...result, loading: false, error: undefined })
        if (reloadDiscard.current[key] === reload) {
          delete reloadDiscard.current[key]
          clearDraft(key)
        }
      })
      .catch((err) => {
        if (stale()) return
        setDocument(key, { ...blank, loading: false, error: message(err) })
      })
  }, [clearDraft, client, documentLoading, documentPresent, epoch, identityEpoch, key, mode, reload, selectedRunID, selectedWorkspaceID, selection.kind, selection.path, setDocument])

  const save = async () => {
    const currentState = useStore.getState()
    const currentDocument = currentState.documents[key]
    const currentDraft = currentState.drafts[key]
    if (!currentDocument || currentDocument.loading || currentDocument.binary || currentDocument.truncated || !currentDocument.writable || !currentDraft || currentDraft.saving || currentDraft.content === currentDraft.baseContent) return false
    const captured = currentDraft.content
    const revision = currentDraft.baseRevision
    const epochAtStart = currentState.identityEpoch
    setDraft(key, { ...currentDraft, content: captured, saving: true, error: undefined, conflict: false })
    try {
      const result = selection.kind === 'home'
        ? await client.homeWrite({ path: selection.path, content: captured, revision })
        : await client.filesWrite({ workspace_id: selection.workspaceID, ...(selection.runID ? { run_id: selection.runID } : {}), path: selection.path, content: captured, revision })
      if (useStore.getState().identityEpoch !== epochAtStart) return false
      markDraftSaved(key, captured, result)
      return true
    } catch (err) {
      if (useStore.getState().identityEpoch !== epochAtStart) return false
      const detail = message(err)
      const conflict = (err instanceof ApiError && err.status === 409) || /conflict|stale|revision/i.test(detail)
      markDraftError(key, detail, conflict)
      return false
    }
  }

  const reloadFromServer = () => {
    setReload((value) => {
      const next = value + 1
      reloadDiscard.current[key] = next
      return next
    })
  }

  const base = isBaseBranch(selection)
  const branch = base ? (selection.branch ?? 'branch') : ''
  const error = draft?.error ?? document?.error
  const text = Boolean(document && !document.loading && !document.error && !document.binary && !document.truncated)
  const canEdit = text && document!.writable
  const unsaved = Boolean(draft && draft.content !== draft.baseContent)
  const canSave = canEdit && unsaved && !draft?.saving && !documentLoading
  const primary = () => (base ? setCommitting(true) : void save())
  const keySave = () => {
    if (canSave) primary()
  }
  const runFile = selection.kind === 'workspace' && Boolean(selection.runID)
  const shownPath = (file: Selection) => (file.kind === 'home' ? tildePath(file.path) : file.path)

  return (
    <article className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-canvas">
      <header className="shrink-0 border-b border-seam">
        <div className="flex h-11 min-w-0 items-center gap-2 px-3 coarse:h-14 coarse:px-2">
          {onBrowse && (
            <Button variant="ghost" size="icon" label="Browse" onClick={onBrowse}>
              <FolderTree />
            </Button>
          )}
          <div className="flex min-w-0 flex-1 flex-col">
            <p className="truncate font-code text-ui-sm text-text" title={shownPath(selection)}>{shownPath(selection)}</p>
            <p className="truncate text-ui-xs text-muted" title={selection.label}>{selection.label}</p>
          </div>
          <Button variant="ghost" size="sm" hint="Find and replace (Ctrl/Cmd-F)" aria-label="Find and replace" onClick={() => editorCommands.openSearch()}>
            <Search />
            <span className="max-sm:hidden">Find</span>
          </Button>
          {text && !canEdit ? (
            <Badge title="You can read this file but not change it.">Read-only</Badge>
          ) : (
            <Button size="sm" disabled={!canSave} onClick={primary}>
              {draft?.saving ? (base ? 'Committing…' : 'Saving…') : base ? `Commit to ${branch}…` : 'Save'}
            </Button>
          )}
        </div>
        <div className="flex min-w-0 items-center gap-2 px-3 coarse:px-2">
          <Tabs value={key} onValueChange={(value) => {
            const tab = tabs.find((item) => sourceKey(item, item.path) === value)
            if (tab) onSelect(tab, tab.path)
          }} className="min-w-0 flex-1">
            <TabsList aria-label="Open files" className="overflow-x-auto [scrollbar-width:none]">
              {tabs.map((tab) => {
                const tabKey = sourceKey(tab, tab.path)
                const tabDraft = drafts[tabKey]
                const name = tab.path.split('/').at(-1) ?? tab.path
                return (
                  <TabsTrigger
                    key={tabKey}
                    value={tabKey}
                    title={`${shownPath(tab)} · ${tab.label}`}
                    aria-keyshortcuts="Delete"
                    onKeyDown={(event) => {
                      if (event.key === 'Delete') onClose(tab)
                    }}
                    onAuxClick={(event) => {
                      if (event.button === 1) onClose(tab)
                    }}
                  >
                    {name}
                    {tabDraft && (tabDraft.content !== tabDraft.baseContent || tabDraft.saving) && (
                      <span role="img" aria-label="Unsaved changes" className="size-1.5 rounded-full bg-text" />
                    )}
                    <span
                      aria-hidden
                      title={`Close ${shownPath(tab)}`}
                      className="-mr-1 grid size-6 place-items-center rounded-control text-muted hover:bg-hover hover:text-text coarse:size-11"
                      onMouseDown={(event) => {
                        event.preventDefault()
                        event.stopPropagation()
                      }}
                      onClick={(event) => {
                        event.stopPropagation()
                        onClose(tab)
                      }}
                    >
                      <X />
                    </span>
                  </TabsTrigger>
                )
              })}
            </TabsList>
          </Tabs>
          {runFile && (
            <Tabs value={mode} onValueChange={(value) => onMode(value as ViewerMode)}>
              <TabsList look="segmented" aria-label="File view">
                <TabsTrigger value="file">File</TabsTrigger>
                <TabsTrigger value="diff">Changes vs base</TabsTrigger>
              </TabsList>
            </Tabs>
          )}
        </div>
      </header>
      {error && (
        <Callout
          tone="failed"
          role="alert"
          className="m-2"
          actions={draft?.conflict && (
            <>
              <Button size="sm" variant="secondary" onClick={reloadFromServer}>Reload from server</Button>
              <Button size="sm" variant="ghost" onClick={() => clearDraft(key)}>Discard edits</Button>
            </>
          )}
        >
          {error}
        </Callout>
      )}
      {mode === 'diff' && runFile && selection.kind === 'workspace'
        ? <DiffDocument client={client} selection={selection} epoch={epoch} />
        : !document?.error && <EditableDocument path={selection.path} document={document} draft={draft} canEdit={canEdit} reveal={reveal} onChange={handleDraftChange} onSave={keySave} />}
      {committing && (
        <CommitDialog
          branch={branch}
          path={selection.path}
          onClose={() => setCommitting(false)}
          error={draft?.error}
          onCommit={async () => {
            if (await save()) setCommitting(false)
          }}
        />
      )}
    </article>
  )
}

function CommitDialog({ branch, path, error, onClose, onCommit }: { branch: string; path: string; error?: string; onClose: () => void; onCommit: () => Promise<void> }) {
  const [busy, setBusy] = useState(false)
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Commit to {branch}</DialogTitle>
          <DialogDescription>
            Commit creates a workspace commit on {branch} with your change to <span className="font-code text-text">{path}</span>; it does not push upstream.
          </DialogDescription>
        </DialogHeader>
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <DialogFooter>
          <Button variant="secondary" disabled={busy} onClick={onClose}>Cancel</Button>
          <Button
            disabled={busy}
            onClick={() => {
              setBusy(true)
              void onCommit().finally(() => setBusy(false))
            }}
          >
            {busy ? 'Committing…' : 'Commit'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

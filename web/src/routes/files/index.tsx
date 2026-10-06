import { useEffect, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { message } from '@/lib/format'
import type { ConfigRoot } from '@/lib/types'
import { FileEditor, type ViewerMode } from '@/routes/files/editor'
import {
  type ConfigSource,
  type FileSource,
  fileTabFromSource,
  fileTabToSelection,
  isLiveRun,
  type Selection,
  sourceKey,
} from '@/routes/files/sources'
import { FileTree, NewConfigFile, TreeExpansion } from '@/routes/files/tree'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

export function FilesRoute({ client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore(useShallow((s) => Object.values(s.workspaces)))
  const liveRuns = useStore(useShallow((s) => Object.values(s.runs).filter(isLiveRun)))
  const filesEpoch = useStore((s) => s.filesEpoch)
  const identityKey = useStore((s) => s.identityKey)
  const fileTabs = useStore((s) => s.fileTabs)
  const activeFileKey = useStore((s) => s.activeFileKey)
  const openFileTab = useStore((s) => s.openFileTab)
  const closeFileTab = useStore((s) => s.closeFileTab)
  const setActiveFileKey = useStore((s) => s.setActiveFileKey)
  const capabilities = useCapability()
  const mobile = useIsMobile()
  const [mode, setMode] = useState<ViewerMode>('file')
  const [browsing, setBrowsing] = useState(false)
  const [configRoots, setConfigRoots] = useState<ConfigRoot[] | null>(null)
  const [configError, setConfigError] = useState<string | null>(null)
  const [newFile, setNewFile] = useState<ConfigSource | null>(null)

  useEffect(() => {
    if (!capabilities.hasMethod('config.roots')) return
    let active = true
    setConfigRoots(null)
    setConfigError(null)
    void client
      .configRoots()
      .then((result) => {
        if (active) setConfigRoots(result.roots)
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
    setBrowsing(false)
  }, [identityKey])

  const tabs = fileTabs.map(fileTabToSelection)
  const selection = tabs.find((tab) => sourceKey(tab, tab.path) === activeFileKey) ?? null

  useEffect(() => {
    if (activeFileKey && tabs.some((tab) => sourceKey(tab, tab.path) === activeFileKey)) return
    const last = tabs.at(-1)
    const nextKey = last ? sourceKey(last, last.path) : null
    if (nextKey !== activeFileKey) setActiveFileKey(nextKey)
  }, [activeFileKey, setActiveFileKey, tabs])

  const select = (source: FileSource, path: string) => {
    openFileTab(fileTabFromSource(source, path, sourceKey(source, path)))
    setMode('file')
    setBrowsing(false)
  }

  const closeTab = (tab: Selection) => {
    const key = sourceKey(tab, tab.path)
    const state = useStore.getState()
    const draft = state.drafts[key]
    const dirty = Boolean(draft && (draft.content !== draft.baseContent || draft.saving))
    if (dirty && !window.confirm(`Discard unsaved changes to ${tab.path}?`)) return
    if (dirty) state.clearDraft(key)
    const index = state.fileTabs.findIndex((item) => item.key === key)
    const wasActive = state.activeFileKey === key
    closeFileTab(key)
    if (!wasActive) return
    const remaining = state.fileTabs.filter((item) => item.key !== key)
    setActiveFileKey(remaining[Math.min(index, remaining.length - 1)]?.key ?? null)
    setMode('file')
  }

  const canBrowseWorkspaces = capabilities.hasMethod('files.tree')
  const canBrowseConfig = capabilities.hasMethod('config.roots')
  if (!canBrowseWorkspaces && !canBrowseConfig) return null
  const shownWorkspaces = canBrowseWorkspaces ? workspaces : []
  const nothing = shownWorkspaces.length === 0 && (!canBrowseConfig || configRoots?.length === 0)

  const tree = (
    <>
      <FileTree
        workspaces={shownWorkspaces}
        runs={canBrowseWorkspaces ? liveRuns : []}
        configRoots={canBrowseConfig ? configRoots : null}
        client={client}
        onSelect={select}
        selected={selection}
        onNewFile={setNewFile}
      />
      {newFile && (
        <NewConfigFile
          key={newFile.harness}
          source={newFile}
          client={client}
          onCancel={() => setNewFile(null)}
          onCreated={(path) => {
            setNewFile(null)
            select(newFile, path)
          }}
        />
      )}
      {configError && <Callout tone="failed" role="alert" className="mx-2 mb-2">{configError}</Callout>}
      {nothing && <p className="px-4 py-3 text-ui text-muted">No workspaces or agent config roots to browse.</p>}
    </>
  )

  const editor = selection && (
    <FileEditor
      selection={selection}
      tabs={tabs}
      mode={mode}
      onMode={setMode}
      onSelect={select}
      onClose={closeTab}
      onBrowse={mobile ? () => setBrowsing(true) : undefined}
      client={client}
      epoch={filesEpoch}
    />
  )

  return (
    <TreeExpansion>
      <div className="flex h-full min-h-0 min-w-0 flex-col">
        <ViewHeader title="Files" />
        {mobile ? (
          <div className="flex min-h-0 flex-1 flex-col">
            {editor ?? <aside aria-label="Files" className="min-h-0 flex-1 overflow-y-auto">{tree}</aside>}
            <Dialog open={browsing && Boolean(selection)} onOpenChange={setBrowsing}>
              <DialogContent variant="side" side="left" aria-describedby={undefined} className="w-[min(320px,85vw)]">
                <div className="flex h-11 shrink-0 items-center border-b border-seam px-4">
                  <DialogTitle>Files</DialogTitle>
                </div>
                <aside aria-label="Files" className="min-h-0 flex-1 overflow-y-auto">{tree}</aside>
              </DialogContent>
            </Dialog>
          </div>
        ) : (
          <div className="flex min-h-0 min-w-0 flex-1">
            <aside aria-label="Files" className="w-[280px] shrink-0 overflow-y-auto border-r border-seam bg-chrome">{tree}</aside>
            {editor ?? (
              <div className="flex min-w-0 flex-1 items-center justify-center">
                <EmptyState title="No file open">Pick a file in the tree to read or edit it.</EmptyState>
              </div>
            )}
          </div>
        )}
      </div>
    </TreeExpansion>
  )
}

registerRoute('files', FilesRoute)
